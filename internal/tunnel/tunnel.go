// Package tunnel runs a Cloudflare quick tunnel (no account needed) that
// publishes the local public listener on an https://*.trycloudflare.com URL.
package tunnel

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"duetto/internal/procgroup"
)

// Pinned release. Bump together with the checksum (from the release notes).
const (
	Version    = "2026.10.7"
	winURL     = "https://github.com/cloudflare/cloudflared/releases/download/" + Version + "/cloudflared-windows-amd64.exe"
	winSHA256  = "2493dbc168923bb3e59e5e7716f71deced92992eea9acd6e9d74f84ac7751343"
	maxBinSize = 200 << 20
)

// State of the tunnel.
type State string

const (
	Off      State = "off"
	Starting State = "starting"
	Ready    State = "ready"
	Failed   State = "error"
)

var (
	urlRe        = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)
	registeredRe = regexp.MustCompile(`(?i)registered tunnel connection`)
)

// Tunnel manages one cloudflared child process.
type Tunnel struct {
	Bin    string // cloudflared executable
	Target string // e.g. http://127.0.0.1:7791

	mu     sync.Mutex
	state  State
	url    string
	err    string
	cancel context.CancelFunc
	done   chan struct{}
}

// Status returns the state, public URL (when ready) and last error.
func (t *Tunnel) Status() (State, string, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state == "" {
		return Off, "", ""
	}
	return t.state, t.url, t.err
}

// Start launches cloudflared if it isn't running. It returns immediately;
// poll Status. The tunnel is Ready once Cloudflare reports a registered
// connection (the URL is printed earlier but would answer 502/1033 until then).
func (t *Tunnel) Start() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancel != nil {
		return nil // already running (state may say Starting before the process exists)
	}
	// An existing ~/.cloudflared/config.yml makes quick tunnels refuse to
	// start; point cloudflared at an empty config instead.
	cfg, err := os.CreateTemp("", "duetto-cloudflared-*.yml")
	if err != nil {
		return err
	}
	cfg.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, t.Bin, "--config", cfg.Name(), "tunnel", "--no-autoupdate", "--url", t.Target)
	procgroup.Hide(cmd)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return err
	}
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("starting cloudflared: %w", err)
	}
	t.state, t.url, t.err = Starting, "", ""
	t.cancel = cancel
	t.done = make(chan struct{})
	done := t.done

	go func() {
		defer close(done)
		defer os.Remove(cfg.Name())
		var last string
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			last = line
			t.mu.Lock()
			if u := urlRe.FindString(line); u != "" && t.url == "" {
				t.url = u
			}
			if registeredRe.MatchString(line) && t.url != "" {
				t.state = Ready
			}
			t.mu.Unlock()
		}
		err := cmd.Wait()
		stopped := ctx.Err() != nil // Stop() asked for this exit
		cancel()
		t.mu.Lock()
		defer t.mu.Unlock()
		t.cancel = nil // so a later Start (Retry) launches again
		if stopped {
			t.state, t.url = Off, ""
			return
		}
		t.state, t.url = Failed, ""
		t.err = "Tunnel stopped unexpectedly"
		if msg := cleanLog(last); msg != "" {
			t.err += ": " + msg
		} else if err != nil {
			t.err += ": " + err.Error()
		}
	}()

	go func() { // fail loudly instead of spinning forever
		select {
		case <-done:
		case <-time.After(45 * time.Second):
			t.mu.Lock()
			stuck := t.state == Starting
			t.mu.Unlock()
			if stuck {
				t.Stop()
				t.mu.Lock()
				t.state, t.err = Failed, "Could not reach Cloudflare within 45 s. Check your internet connection."
				t.mu.Unlock()
			}
		}
	}()
	return nil
}

// Stop terminates the tunnel and waits for the process to exit.
func (t *Tunnel) Stop() {
	t.mu.Lock()
	cancel, done := t.cancel, t.done
	t.cancel = nil
	t.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// cleanLog strips cloudflared's "2026-…Z ERR " prefix.
func cleanLog(line string) string {
	if i := strings.Index(line, " ERR "); i >= 0 {
		return strings.TrimSpace(line[i+5:])
	}
	return ""
}

// Locate finds cloudflared next to the executable, in dirs, or on PATH.
func Locate(dirs ...string) (string, bool) {
	name := "cloudflared"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, true
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, true
	}
	return "", false
}

// Download fetches the pinned Windows build into dir and verifies its
// SHA-256 before making it available. Only Windows is supported; on other
// systems install cloudflared with the package manager.
func Download(ctx context.Context, dir string) (string, error) {
	if runtime.GOOS != "windows" {
		return "", errors.New("cloudflared not found: install it and put it on PATH")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, winURL, nil)
	if err != nil {
		return "", err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading cloudflared: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading cloudflared: HTTP %d", res.StatusCode)
	}
	tmp, err := os.CreateTemp(dir, "cloudflared-*.part")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), io.LimitReader(res.Body, maxBinSize))
	tmp.Close()
	if err != nil {
		return "", fmt.Errorf("downloading cloudflared: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != winSHA256 {
		return "", fmt.Errorf("cloudflared checksum mismatch (got %s): refusing to run it", got)
	}
	dst := filepath.Join(dir, "cloudflared.exe")
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	return dst, nil
}

// SetPreparing marks the tunnel as starting while the binary is fetched.
func (t *Tunnel) SetPreparing() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state != Ready {
		t.state, t.err = Starting, ""
	}
}

// SetError records a failure that happened before cloudflared could run.
func (t *Tunnel) SetError(msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state, t.url, t.err = Failed, "", msg
}
