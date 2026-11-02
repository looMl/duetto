package room

import (
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type inbox struct {
	mu   sync.Mutex
	msgs []any
}

func (b *inbox) send(m any) { b.mu.Lock(); b.msgs = append(b.msgs, m); b.mu.Unlock() }
func (b *inbox) lastState(t *testing.T) State {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.msgs) - 1; i >= 0; i-- {
		if s, ok := b.msgs[i].(StateMsg); ok {
			return s.State
		}
	}
	t.Fatal("no state message")
	return State{}
}

func setup(t *testing.T) (*Room, *clock, *inbox, *inbox) {
	t.Helper()
	c := &clock{t: time.Unix(1_000_000, 0)}
	r := New(c.now)
	r.SetDuration(3600)
	a, b := &inbox{}, &inbox{}
	r.Join(&Member{Viewer: Viewer{ID: "a", Name: "Luca"}, Send: a.send})
	r.Join(&Member{Viewer: Viewer{ID: "b", Name: "Ana"}, Send: b.send})
	return r, c, a, b
}

func near(t *testing.T, got, want float64) {
	t.Helper()
	if d := got - want; d > 1e-9 || d < -1e-9 {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestPlayIsScheduledAndAdvances(t *testing.T) {
	r, c, _, b := setup(t)
	r.Command("a", OpPlay, 0)
	s := b.lastState(t)
	if s.Paused || s.By != "Luca" {
		t.Fatalf("unexpected state %+v", s)
	}
	if s.At != c.now().UnixMilli()+StartDelay.Milliseconds() {
		t.Fatalf("play not scheduled in the future: %+v", s)
	}
	near(t, s.PositionAt(c.now().UnixMilli()), 0) // before start
	c.add(StartDelay + 10*time.Second)
	near(t, r.State().PositionAt(c.now().UnixMilli()), 10)
}

func TestPauseFreezesPosition(t *testing.T) {
	r, c, a, _ := setup(t)
	r.Command("a", OpPlay, 0)
	c.add(StartDelay + 5*time.Second)
	r.Command("b", OpPause, 0)
	c.add(time.Minute)
	s := a.lastState(t)
	if !s.Paused || s.By != "Ana" {
		t.Fatalf("unexpected %+v", s)
	}
	near(t, s.PositionAt(c.now().UnixMilli()), 5)
}

func TestSeekClampsAndRejectsNaN(t *testing.T) {
	r, _, _, _ := setup(t)
	r.Command("a", OpSeek, -5)
	near(t, r.State().Position, 0)
	r.Command("a", OpSeek, 99999)
	near(t, r.State().Position, 3600)
	seq := r.State().Seq
	r.Command("a", OpSeek, nan())
	if r.State().Seq != seq {
		t.Fatal("NaN seek must be ignored")
	}
}

func TestUnknownMemberIgnored(t *testing.T) {
	r, _, _, _ := setup(t)
	seq := r.State().Seq
	r.Command("zz", OpPlay, 0)
	if r.State().Seq != seq {
		t.Fatal("command from non-member applied")
	}
}

func TestStallBarrierPausesAndResumes(t *testing.T) {
	r, c, a, _ := setup(t)
	r.Command("a", OpPlay, 0)
	c.add(StartDelay + 30*time.Second)

	r.Report("b", Status{Buffering: true})
	s := a.lastState(t)
	if !s.Paused || len(s.Waiting) != 1 || s.Waiting[0] != "Ana" {
		t.Fatalf("barrier did not pause: %+v", s)
	}
	near(t, s.Position, 30)

	c.add(4 * time.Second)
	r.Report("b", Status{Buffering: false, Ahead: 10})
	s = a.lastState(t)
	if s.Paused || s.Waiting != nil {
		t.Fatalf("barrier did not resume: %+v", s)
	}
	if s.At != c.now().UnixMilli()+ResumeDelay.Milliseconds() {
		t.Fatalf("resume not counted down: %+v", s)
	}
	near(t, s.Position, 30)
}

func TestManualPauseDuringBarrierCancelsAutoResume(t *testing.T) {
	r, c, _, _ := setup(t)
	r.Command("a", OpPlay, 0)
	c.add(time.Second)
	r.Report("b", Status{Buffering: true})
	r.Command("a", OpPause, 0)
	r.Report("b", Status{Buffering: false})
	if !r.State().Paused {
		t.Fatal("manual pause must win over auto-resume")
	}
}

func TestPlayWhileSomeoneBufferingWaitsForThem(t *testing.T) {
	r, _, _, _ := setup(t)
	r.Report("b", Status{Buffering: true})
	r.Command("a", OpPlay, 0)
	if s := r.State(); !s.Paused || len(s.Waiting) != 1 {
		t.Fatalf("play should wait for Ana: %+v", s)
	}
	r.Report("b", Status{Buffering: false})
	if r.State().Paused {
		t.Fatal("should start once Ana is ready")
	}
}

func TestLeavingReleasesBarrier(t *testing.T) {
	r, _, _, _ := setup(t)
	r.Command("a", OpPlay, 0)
	r.Report("b", Status{Buffering: true})
	r.Leave("b")
	if r.State().Paused {
		t.Fatal("a departed viewer must not hold the room")
	}
}

func TestConcurrentUse(t *testing.T) {
	r, _, _, _ := setup(t)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ops := []Op{OpPlay, OpPause, OpSeek}
			r.Command("a", ops[i%3], float64(i))
			r.Report("b", Status{Buffering: i%2 == 0})
			_ = r.Viewers()
		}(i)
	}
	wg.Wait()
}

func nan() float64 { var z float64; return z / z }
