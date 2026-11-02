//go:build !windows

package tunnel

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Regression: after SetPreparing (download path) Start must still launch.
func TestStartAfterPreparingReachesReady(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "cloudflared")
	script := "#!/bin/sh\necho 'INF |  https://abc-def.trycloudflare.com  |' >&2\necho 'INF Registered tunnel connection' >&2\nexec sleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	tn := &Tunnel{Bin: bin, Target: "http://127.0.0.1:1"}
	tn.SetPreparing()
	if err := tn.Start(); err != nil {
		t.Fatal(err)
	}
	defer tn.Stop()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if st, url, _ := tn.Status(); st == Ready {
			if url != "https://abc-def.trycloudflare.com" {
				t.Fatalf("url %q", url)
			}
			return
		}
	}
	st, _, err := tn.Status()
	t.Fatalf("never ready: %s %s", st, err)
}

// Regression: after cloudflared dies, Start (the Retry button) must relaunch.
func TestRetryAfterUnexpectedExit(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "cloudflared")
	write := func(script string) {
		if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("echo '2026-11-02T00:00:00Z ERR network unreachable' >&2\nexit 1\n")
	tn := &Tunnel{Bin: bin, Target: "http://127.0.0.1:1"}
	if err := tn.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, tn, Failed)
	if _, _, msg := tn.Status(); msg != "Tunnel stopped unexpectedly: network unreachable" {
		t.Fatalf("error %q", msg)
	}

	write("echo 'https://x-y.trycloudflare.com' >&2\necho 'Registered tunnel connection' >&2\nexec sleep 30\n")
	if err := tn.Start(); err != nil {
		t.Fatal(err)
	}
	defer tn.Stop()
	waitState(t, tn, Ready)
}

func waitState(t *testing.T, tn *Tunnel, want State) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if st, _, _ := tn.Status(); st == want {
			return
		}
	}
	st, _, err := tn.Status()
	t.Fatalf("want %s, got %s (%s)", want, st, err)
}
