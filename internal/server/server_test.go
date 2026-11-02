package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"duetto/internal/media"
	"duetto/internal/room"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.mp4")
	if err := os.WriteFile(path, []byte(strings.Repeat("0123456789", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	web := fstest.MapFS{"index.html": {Data: []byte("<!doctype html>app")}}
	s := New(slog.New(slog.DiscardHandler), room.New(nil), web)
	s.SetItem(&media.Item{
		ID: "m", Title: "Movie", Duration: 60,
		Variants: []media.Variant{{ID: "v1", Path: path}},
		Subs:     []media.Subtitle{{ID: "en", VTT: []byte("WEBVTT\n\n")}},
	})
	ts := httptest.NewServer(s.Public())
	t.Cleanup(ts.Close)
	return s, ts
}

func client(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

func join(t *testing.T, c *http.Client, base, key, name string) int {
	t.Helper()
	res, err := c.Post(base+"/api/join", "application/json",
		strings.NewReader(`{"key":"`+key+`","name":"`+name+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func get(t *testing.T, c *http.Client, url string, hdr ...string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func TestMediaRequiresSession(t *testing.T) {
	_, ts := newTestServer(t)
	c := client(t)
	for _, p := range []string{"/media/v1", "/subs/en.vtt"} {
		if got := get(t, c, ts.URL+p).StatusCode; got != http.StatusUnauthorized {
			t.Errorf("%s without session: %d", p, got)
		}
	}
}

func TestJoinRejectsWrongKeyAndEmptyName(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	if got := join(t, c, ts.URL, "nope", "Ana"); got != http.StatusForbidden {
		t.Fatalf("wrong key: %d", got)
	}
	if got := join(t, c, ts.URL, s.Secret(), "   "); got != http.StatusBadRequest {
		t.Fatalf("empty name: %d", got)
	}
}

func TestJoinThenRangeRequest(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	if got := join(t, c, ts.URL, s.Secret(), "Ana"); got != http.StatusOK {
		t.Fatalf("join: %d", got)
	}
	res := get(t, c, ts.URL+"/media/v1", "Range", "bytes=10-19")
	if res.StatusCode != http.StatusPartialContent {
		t.Fatalf("range: %d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if string(body) != "0123456789" {
		t.Fatalf("range body %q", body)
	}
	if get(t, c, ts.URL+"/media/nope").StatusCode != http.StatusNotFound {
		t.Fatal("unknown variant must 404")
	}
	if get(t, c, ts.URL+"/media/..%2f..%2fetc%2fpasswd").StatusCode != http.StatusNotFound {
		t.Fatal("traversal attempt must 404")
	}
}

func TestRotateSecretRevokesGuests(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	join(t, c, ts.URL, s.Secret(), "Ana")
	s.RotateSecret()
	if get(t, c, ts.URL+"/media/v1").StatusCode != http.StatusUnauthorized {
		t.Fatal("guest session must be revoked")
	}
}

func TestJoinRateLimited(t *testing.T) {
	_, ts := newTestServer(t)
	c := client(t)
	var last int
	for i := 0; i < 11; i++ {
		last = join(t, c, ts.URL, "wrong", "x")
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("11th attempt: %d", last)
	}
}

func TestAdminRejectsForeignHostAndOrigin(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Admin(7790)

	req := httptest.NewRequest("GET", "/api/session", nil)
	req.Host = "evil.example:7790"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("rebinding host: %d", rec.Code)
	}

	req = httptest.NewRequest("POST", "/api/admin/rotate", nil)
	req.Host = "127.0.0.1:7790"
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/api/session", nil)
	req.Host = "127.0.0.1:7790"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"joined":true`) || !strings.Contains(rec.Body.String(), `"host":true`) {
		t.Fatalf("host session: %d %s", rec.Code, rec.Body)
	}
}

func TestSecurityHeadersAndSPAFallback(t *testing.T) {
	_, ts := newTestServer(t)
	res := get(t, client(t), ts.URL+"/some/deep/link")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("spa fallback: %d", res.StatusCode)
	}
	if res.Header.Get("Content-Security-Policy") == "" || res.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("missing security headers")
	}
}

type fakeHooks struct {
	s   *Server
	got string
}

func (f *fakeHooks) Status() any                        { return nil }
func (f *fakeHooks) Action(string, []byte) (any, error) { return nil, nil }
func (f *fakeHooks) UploadSubtitle(name string, data []byte) error {
	f.got = name
	return f.s.AddSubtitle(media.Subtitle{ID: "u1", Label: "Italiano", VTT: data})
}

func TestSubtitleUploadIsAdminOnlyAndBroadcast(t *testing.T) {
	s, ts := newTestServer(t)
	hooks := &fakeHooks{s: s}
	s.Hooks = hooks

	// Not reachable on the public listener, even with a session.
	c := client(t)
	join(t, c, ts.URL, s.Secret(), "Ana")
	res, err := c.Post(ts.URL+"/api/admin/subtitles", "application/octet-stream", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Fatal("guests must not upload subtitles")
	}

	req := httptest.NewRequest("POST", "/api/admin/subtitles", strings.NewReader("WEBVTT\n"))
	req.Host = "127.0.0.1:7790"
	req.Header.Set("X-Filename", "Shining%20ita.srt")
	rec := httptest.NewRecorder()
	s.Admin(7790).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || hooks.got != "Shining ita.srt" {
		t.Fatalf("upload: %d %s name=%q", rec.Code, rec.Body, hooks.got)
	}
	if sub, ok := s.Item().Subtitle("u1"); !ok || sub.Label != "Italiano" {
		t.Fatal("subtitle not added to the item")
	}
	if get(t, c, ts.URL+"/subs/u1.vtt").StatusCode != http.StatusOK {
		t.Fatal("guest cannot fetch the uploaded track")
	}
}
