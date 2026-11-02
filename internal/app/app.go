// Package app wires the room server, media preparation, library and tunnel
// into the host-facing admin API.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"duetto/internal/media"
	"duetto/internal/server"
	"duetto/internal/tunnel"
)

// App implements server.AdminHooks.
type App struct {
	Log      *slog.Logger
	Server   *server.Server
	Preparer *media.Preparer
	Tunnel   *tunnel.Tunnel
	Library  []string // folders scanned for videos
	ToolsDir string   // where cloudflared is downloaded if missing

	mu       sync.Mutex
	current  string
	gen      int // increments per preparation run; stale runs don't touch status
	prepare  prepareStatus
	cancel   context.CancelFunc
	jobs     sync.WaitGroup // running preparations
	libCache []LibraryFile
	libAt    time.Time
	tunnelUp sync.Mutex // serializes cloudflared download/start
}

type prepareStatus struct {
	State     string        `json:"state"` // idle | running | done | stopped | error
	Step      string        `json:"step,omitempty"`
	StepIndex int           `json:"stepIndex"`
	StepCount int           `json:"stepCount"`
	Progress  float64       `json:"progress"` // of the current step
	ETA       float64       `json:"eta"`      // seconds; < 0 unknown
	Error     string        `json:"error,omitempty"`
	Pending   []pendingInfo `json:"pending"` // variants that can be built on request
}

type pendingInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// LibraryFile is a video found in a library folder.
type LibraryFile struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type tunnelStatus struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

type status struct {
	Link    string        `json:"link"`
	Tunnel  tunnelStatus  `json:"tunnel"`
	Library []LibraryFile `json:"library"`
	Current string        `json:"current"`
	Prepare prepareStatus `json:"prepare"`
}

// Status implements server.AdminHooks.
func (a *App) Status() any {
	st, url, terr := a.Tunnel.Status()
	link := ""
	if st == tunnel.Ready {
		link = url + "/#k=" + a.Server.Secret()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.prepare
	if p.State == "" {
		p.State = "idle"
	}
	return status{
		Link:    link,
		Tunnel:  tunnelStatus{State: string(st), Error: terr},
		Library: a.libraryLocked(),
		Current: a.current,
		Prepare: p,
	}
}

// Action implements server.AdminHooks.
func (a *App) Action(name string, body []byte) (any, error) {
	switch name {
	case "rotate":
		a.Server.RotateSecret()
		return map[string]any{"ok": true}, nil
	case "tunnel":
		go a.StartTunnel()
		return map[string]any{"ok": true}, nil
	case "open":
		var req struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, errors.New("bad request")
		}
		if !a.inLibrary(req.Path) {
			return nil, errors.New("file is not in a library folder")
		}
		a.Open(req.Path, false)
		return map[string]any{"ok": true}, nil
	case "stop":
		a.Stop()
		return map[string]any{"ok": true}, nil
	case "compat":
		a.mu.Lock()
		cur, busy := a.current, a.prepare.State == "running"
		a.mu.Unlock()
		if cur == "" || busy {
			return nil, errors.New("nothing to build right now")
		}
		a.Open(cur, true)
		return map[string]any{"ok": true}, nil
	}
	return nil, fmt.Errorf("unknown action %q", name)
}

// UploadSubtitle converts an uploaded subtitle file, saves it with the
// current item (so it survives restarts) and makes it available to everyone.
func (a *App) UploadSubtitle(filename string, data []byte) error {
	it := a.Server.Item()
	if it == nil {
		return errors.New("pick a movie first")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	vtt, err := media.ToVTT(ctx, a.Preparer.Tools.FFmpeg, filename, data)
	if err != nil {
		return err
	}
	lang := media.GuessLang(filename)
	sub := media.Subtitle{ID: media.NextUploadID(it.Subs), Lang: lang, Label: media.UploadLabel(lang, it.Subs), VTT: vtt}
	if err := media.SaveUpload(a.Preparer.ItemDir(it.ID), sub); err != nil {
		a.Log.Warn("subtitle not saved for next time", "err", err)
	}
	a.Log.Info("subtitle added", "file", filename, "label", sub.Label)
	return a.Server.AddSubtitle(sub)
}

// StartTunnel locates (or downloads) cloudflared and starts the tunnel.
func (a *App) StartTunnel() {
	a.tunnelUp.Lock()
	defer a.tunnelUp.Unlock()
	if a.Tunnel.Bin == "" {
		a.Tunnel.SetPreparing()
		bin, ok := tunnel.Locate(a.ToolsDir)
		if !ok {
			a.Log.Info("downloading cloudflared", "version", tunnel.Version)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			var err error
			if bin, err = tunnel.Download(ctx, a.ToolsDir); err != nil {
				a.Log.Error("cloudflared", "err", err)
				a.Tunnel.SetError(err.Error())
				return
			}
		}
		a.Tunnel.Bin = bin
	}
	if err := a.Tunnel.Start(); err != nil {
		a.Log.Error("tunnel", "err", err)
		a.Tunnel.SetError(err.Error())
	}
}

// Open prepares path in the background and switches the room to it as soon
// as the first (lossless) variant is ready. withCompat also builds the slow
// compatibility encode. A running preparation is cancelled first.
func (a *App) Open(path string, withCompat bool) {
	a.mu.Lock()
	if a.cancel != nil {
		a.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.gen++
	gen := a.gen
	pending := a.prepare.Pending
	if a.current != path {
		pending = nil
	}
	a.current = path
	a.prepare = prepareStatus{State: "running", Step: "Starting", ETA: -1, Pending: pending}
	a.mu.Unlock()

	a.jobs.Add(1)
	go func() {
		defer a.jobs.Done()
		start := time.Now()
		a.Log.Info("preparing", "file", path, "compat", withCompat)
		res, err := a.Preparer.Prepare(ctx, path, withCompat,
			func(p media.Progress) {
				a.mu.Lock()
				if a.gen == gen && a.prepare.State == "running" {
					a.prepare.Step, a.prepare.StepIndex, a.prepare.StepCount = p.Step, p.StepIndex, p.StepCount
					a.prepare.Progress, a.prepare.ETA = p.Fraction, p.ETA
				}
				a.mu.Unlock()
			},
			func(it *media.Item) {
				if ctx.Err() == nil {
					a.Server.SetItem(it)
				}
			})
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.gen != gen {
			return // superseded by a newer run
		}
		switch {
		case errors.Is(err, context.Canceled):
			a.Log.Info("preparation stopped", "file", path)
			a.prepare.State, a.prepare.Step, a.prepare.ETA = "stopped", "", -1
		case err != nil:
			a.Log.Error("prepare failed", "file", path, "err", err)
			a.prepare = prepareStatus{State: "error", Error: err.Error(), Pending: a.prepare.Pending}
		default:
			a.Log.Info("prepared", "file", path, "took", time.Since(start).Round(time.Second))
			var p []pendingInfo
			for _, v := range res.Pending {
				p = append(p, pendingInfo{ID: v.ID, Label: v.Label})
			}
			a.prepare = prepareStatus{State: "done", Progress: 1, Pending: p}
		}
	}()
}

// Stop cancels the running preparation. Variants already finished stay
// playable; the interrupted one leaves no partial file behind.
func (a *App) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil && a.prepare.State == "running" {
		a.cancel()
	}
}

// Close cancels background work and waits for it, so ffmpeg is stopped and
// its partial output deleted before the process exits.
func (a *App) Close() {
	a.mu.Lock()
	if a.cancel != nil {
		a.cancel()
	}
	a.mu.Unlock()
	done := make(chan struct{})
	go func() { a.jobs.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		a.Log.Warn("preparation did not stop in time")
	}
	a.Tunnel.Stop()
}

func (a *App) inLibrary(path string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, f := range a.libraryLocked() {
		if f.Path == path {
			return true
		}
	}
	return false
}

var videoExt = map[string]bool{".mkv": true, ".mp4": true, ".m4v": true, ".mov": true, ".webm": true, ".avi": true, ".ts": true}

// libraryLocked scans library folders (depth ≤ 3), cached for 10 s.
func (a *App) libraryLocked() []LibraryFile {
	if time.Since(a.libAt) < 10*time.Second && a.libCache != nil {
		return a.libCache
	}
	out := []LibraryFile{} // never null in JSON
	cache := filepath.Clean(a.Preparer.CacheDir)
	for _, root := range a.Library {
		root = filepath.Clean(root)
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				rel, _ := filepath.Rel(root, p)
				if p == cache || strings.HasPrefix(d.Name(), ".") || strings.Count(rel, string(filepath.Separator)) >= 3 {
					return filepath.SkipDir
				}
				return nil
			}
			if !videoExt[strings.ToLower(filepath.Ext(p))] {
				return nil
			}
			info, err := d.Info()
			if err != nil || info.Size() < 50<<20 { // skip samples and clips
				return nil
			}
			out = append(out, LibraryFile{Path: p, Name: filepath.Base(p), Size: info.Size()})
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	a.libCache, a.libAt = out, time.Now()
	return out
}

// DefaultLibrary returns the user's Videos and Downloads folders.
func DefaultLibrary() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var dirs []string
	for _, d := range []string{"Videos", "Downloads", "Movies"} {
		p := filepath.Join(home, d)
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			dirs = append(dirs, p)
		}
	}
	return dirs
}
