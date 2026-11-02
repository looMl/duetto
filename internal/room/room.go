// Package room implements the authoritative playback state shared by all
// viewers, and the rules that keep it consistent (commands, stall barrier).
//
// It is transport-agnostic: broadcasts go through each Member's Send.
// All methods are safe for concurrent use.
package room

import (
	"math"
	"slices"
	"sort"
	"sync"
	"time"
)

// StartDelay is how far in the future a "play" is scheduled, so every viewer
// receives the command before the shared start instant.
const StartDelay = 400 * time.Millisecond

// ResumeDelay is the countdown used when resuming after a stall barrier.
const ResumeDelay = 1500 * time.Millisecond

// State is the authoritative playback state. When Paused is false, the media
// position at server time t (unix ms) is Position + (t-At)/1000, clamped to
// Position for t < At (scheduled start).
type State struct {
	Seq      uint64  `json:"seq"`
	Paused   bool    `json:"paused"`
	Position float64 `json:"position"` // seconds
	At       int64   `json:"at"`       // server unix ms
	// Waiting lists names of viewers the room is paused for (stall barrier).
	Waiting []string `json:"waiting,omitempty"`
	// By is the name of the viewer who issued the last command.
	By string `json:"by,omitempty"`
}

// PositionAt returns the media position at server time nowMs.
func (s State) PositionAt(nowMs int64) float64 {
	if s.Paused || nowMs <= s.At {
		return s.Position
	}
	return s.Position + float64(nowMs-s.At)/1000
}

// Viewer is the public view of a member, broadcast to everyone.
type Viewer struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Host      bool    `json:"host"`
	Buffering bool    `json:"buffering"`
	Ahead     float64 `json:"ahead"` // seconds buffered ahead of the playhead
	Drift     float64 `json:"drift"` // seconds, viewer minus target
	// Unsupported: this viewer's browser cannot decode the current version.
	Unsupported bool `json:"unsupported"`
}

// Member is a connected viewer.
type Member struct {
	Viewer
	// Send must not block: drop or disconnect slow consumers.
	Send func(msg any)
}

// Status is a periodic report from a viewer.
type Status struct {
	Buffering   bool    `json:"buffering"`
	Ahead       float64 `json:"ahead"`
	Drift       float64 `json:"drift"`
	Unsupported bool    `json:"unsupported"`
}

// Op is a playback command.
type Op string

const (
	OpPlay  Op = "play"
	OpPause Op = "pause"
	OpSeek  Op = "seek"
)

// Room holds the shared state and the connected members.
type Room struct {
	mu          sync.Mutex
	now         func() time.Time
	duration    float64 // seconds; 0 if unknown
	state       State
	autoPaused  bool
	members     map[string]*Member
	lastViewers []Viewer
}

// New returns an empty, paused room. now may be nil (time.Now).
func New(now func() time.Time) *Room {
	if now == nil {
		now = time.Now
	}
	r := &Room{now: now, members: map[string]*Member{}}
	r.state = State{Paused: true, At: r.nowMs()}
	return r
}

func (r *Room) nowMs() int64 { return r.now().UnixMilli() }

// NowMs exposes the room clock (used for clock sync).
func (r *Room) NowMs() int64 { return r.nowMs() }

// SetDuration sets the media duration used to clamp seeks, and resets
// playback to the beginning.
func (r *Room) SetDuration(seconds float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.duration = seconds
	r.autoPaused = false
	r.state = State{Seq: r.state.Seq + 1, Paused: true, At: r.nowMs()}
	r.broadcastStateLocked()
}

// State returns a copy of the current state.
func (r *Room) State() State {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// Join adds a member; the caller sends the welcome. A newcomer joining
// mid-playback pauses the room through its own buffering report.
func (r *Room) Join(m *Member) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.members[m.ID] = m
	r.broadcastViewersLocked()
}

// Leave removes a member. A departing member can never hold the room paused.
func (r *Room) Leave(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.members, id)
	r.reevaluateBarrierLocked()
	r.broadcastViewersLocked()
}

// Viewers returns the current viewers sorted by name.
func (r *Room) Viewers() []Viewer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.viewersLocked()
}

// Command applies a playback command from member id.
func (r *Room) Command(id string, op Op, pos float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.members[id]
	if !ok {
		return
	}
	now := r.nowMs()
	cur := r.state.PositionAt(now)
	switch op {
	case OpPlay:
		if !r.state.Paused || r.autoPaused {
			return // already playing, or already "playing once everyone is ready"
		}
		r.state.Position = cur
		if waiting := r.bufferingLocked(); len(waiting) > 0 {
			// Express the intent; the barrier starts playback when all are ready.
			r.autoPaused = true
			r.state.At = now
			r.state.Waiting = waiting
			break
		}
		r.state.Paused = false
		r.state.At = now + StartDelay.Milliseconds()
		r.state.Waiting = nil
	case OpPause:
		if r.state.Paused && !r.autoPaused {
			return
		}
		r.autoPaused = false
		r.state.Paused = true
		r.state.Position = cur
		r.state.At = now
		r.state.Waiting = nil
	case OpSeek:
		if math.IsNaN(pos) || math.IsInf(pos, 0) {
			return
		}
		r.state.Position = r.clamp(pos)
		if r.state.Paused {
			r.state.At = now
		} else {
			r.state.At = now + StartDelay.Milliseconds()
		}
	default:
		return
	}
	r.state.By = m.Name
	r.state.Seq++
	r.broadcastStateLocked()
}

// Report records a viewer's status and applies the stall barrier.
func (r *Room) Report(id string, s Status) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.members[id]
	if !ok {
		return
	}
	changed := m.Buffering != s.Buffering
	m.Buffering = s.Buffering
	m.Ahead = finite(s.Ahead)
	m.Drift = finite(s.Drift)
	m.Unsupported = s.Unsupported
	if changed {
		r.reevaluateBarrierLocked()
	}
	r.broadcastViewersLocked()
}

// reevaluateBarrierLocked pauses everyone while any member is buffering, and
// resumes (with a countdown) once nobody is, if the pause was automatic.
func (r *Room) reevaluateBarrierLocked() {
	waiting := r.bufferingLocked()
	now := r.nowMs()
	switch {
	case len(waiting) > 0 && !r.state.Paused:
		r.state.Position = r.state.PositionAt(now)
		r.state.Paused = true
		r.state.At = now
		r.state.Waiting = waiting
		r.state.By = ""
		r.autoPaused = true
	case len(waiting) > 0 && r.autoPaused:
		if slices.Equal(waiting, r.state.Waiting) {
			return
		}
		r.state.Waiting = waiting
	case len(waiting) == 0 && r.autoPaused:
		r.autoPaused = false
		r.state.Paused = false
		r.state.At = now + ResumeDelay.Milliseconds()
		r.state.Waiting = nil
		r.state.By = ""
	default:
		return
	}
	r.state.Seq++
	r.broadcastStateLocked()
}

func (r *Room) bufferingLocked() []string {
	var names []string
	for _, m := range r.members {
		if m.Buffering {
			names = append(names, m.Name)
		}
	}
	sort.Strings(names)
	return names
}

func (r *Room) clamp(pos float64) float64 {
	if pos < 0 {
		return 0
	}
	if r.duration > 0 && pos > r.duration {
		return r.duration
	}
	return pos
}

func (r *Room) viewersLocked() []Viewer {
	vs := make([]Viewer, 0, len(r.members))
	for _, m := range r.members {
		vs = append(vs, m.Viewer)
	}
	sort.Slice(vs, func(i, j int) bool {
		if vs[i].Name != vs[j].Name {
			return vs[i].Name < vs[j].Name
		}
		return vs[i].ID < vs[j].ID
	})
	return vs
}

// StateMsg and ViewersMsg are the broadcast envelopes.
type StateMsg struct {
	T     string `json:"t"`
	State State  `json:"state"`
}

type ViewersMsg struct {
	T       string   `json:"t"`
	Viewers []Viewer `json:"viewers"`
}

func (r *Room) broadcastStateLocked() {
	msg := StateMsg{T: "state", State: r.state}
	for _, m := range r.members {
		m.Send(msg)
	}
}

func (r *Room) broadcastViewersLocked() {
	vs := r.viewersLocked()
	if viewersEqualRounded(vs, r.lastViewers) {
		return
	}
	r.lastViewers = vs
	msg := ViewersMsg{T: "viewers", Viewers: vs}
	for _, m := range r.members {
		m.Send(msg)
	}
}

// viewersEqualRounded suppresses broadcasts for sub-perceptible changes.
func viewersEqualRounded(a, b []Viewer) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x.ID != y.ID || x.Name != y.Name || x.Buffering != y.Buffering || x.Host != y.Host || x.Unsupported != y.Unsupported ||
			math.Round(x.Ahead) != math.Round(y.Ahead) ||
			math.Round(x.Drift*20) != math.Round(y.Drift*20) { // 50 ms buckets
			return false
		}
	}
	return true
}

func finite(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}
