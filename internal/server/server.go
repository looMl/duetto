// Package server exposes the room over HTTP: the web app, a WebSocket sync
// channel, range-served media and subtitles.
//
// Two handlers are built from one Server:
//   - Public: what the tunnel exposes. Every route but the app shell and
//     /api/join requires a session cookie obtained with the invite secret.
//   - Admin: bound to loopback only. The host is authenticated implicitly and
//     gets the admin API. It rejects foreign Host/Origin headers to defeat
//     DNS rebinding and cross-site requests from other local web pages.
package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"duetto/internal/media"
	"duetto/internal/room"
)

const (
	cookieName = "duetto_session"
	maxNameLen = 32
)

// Server holds sessions, the room and the current media item.
type Server struct {
	log  *slog.Logger
	room *room.Room
	web  fs.FS

	mu       sync.RWMutex
	secret   string
	sessions map[string]session
	item     *media.Item
	joins    map[string][]time.Time // per remote IP, for rate limiting

	connsMu sync.Mutex
	conns   map[*conn]struct{}

	Hooks    AdminHooks // host-only features; required by Admin()
	HostName string
}

type session struct {
	name string
	host bool
}

// AdminHooks lets the application plug host-only features into the admin API.
type AdminHooks interface {
	Status() any                                  // GET  /api/admin/status
	Action(name string, body []byte) (any, error) // POST /api/admin/{action}
	// UploadSubtitle adds a subtitle file for everyone (POST /api/admin/subtitles).
	UploadSubtitle(filename string, data []byte) error
}

// New creates a server serving the given web assets.
func New(log *slog.Logger, r *room.Room, web fs.FS) *Server {
	return &Server{
		log:      log,
		room:     r,
		web:      web,
		secret:   randToken(16),
		sessions: map[string]session{},
		joins:    map[string][]time.Time{},
		conns:    map[*conn]struct{}{},
		HostName: "Host",
	}
}

// Secret returns the invite secret (goes in the link fragment).
func (s *Server) Secret() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.secret
}

// RotateSecret invalidates the invite link and every guest session.
func (s *Server) RotateSecret() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secret = randToken(16)
	for k, v := range s.sessions {
		if !v.host {
			delete(s.sessions, k)
		}
	}
}

// SetItem changes what is playing and notifies everyone. Re-setting the same
// item (e.g. a new variant finished preparing) keeps the playback position.
func (s *Server) SetItem(it *media.Item) {
	s.mu.Lock()
	same := s.item != nil && it != nil && s.item.ID == it.ID
	s.item = it
	s.mu.Unlock()
	if !same {
		dur := 0.0
		if it != nil {
			dur = it.Duration
		}
		s.room.SetDuration(dur)
	}
	s.broadcastMedia()
}

// Item returns the current media item (may be nil).
func (s *Server) Item() *media.Item {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.item
}

// Public returns the handler exposed to the internet.
func (s *Server) Public() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux, false)
	return securityHeaders(mux)
}

// Admin returns the loopback-only host handler. port is the admin port, used
// to validate the Host header.
func (s *Server) Admin(port int) http.Handler {
	mux := http.NewServeMux()
	s.routes(mux, true)
	mux.HandleFunc("GET /api/admin/status", s.handleAdminStatus)
	mux.HandleFunc("POST /api/admin/subtitles", s.handleSubtitleUpload)
	mux.HandleFunc("POST /api/admin/{action}", s.handleAdminAction)
	return securityHeaders(localOnly(port, mux))
}

func (s *Server) routes(mux *http.ServeMux, admin bool) {
	mux.Handle("GET /", s.app())
	mux.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) {
		// Always 200: "not joined yet" is a normal state, not an error.
		sess, ok := s.sessionFor(r, admin)
		writeJSON(w, http.StatusOK, map[string]any{"joined": ok, "name": sess.name, "host": sess.host})
	})
	mux.HandleFunc("POST /api/join", s.handleJoin)
	mux.HandleFunc("GET /ws", s.authed(admin, s.handleWS))
	mux.HandleFunc("GET /media/{variant}", s.authed(admin, s.handleMedia))
	mux.HandleFunc("GET /subs/{sub}", s.authed(admin, s.handleSub))
}

// sessionFor resolves the caller's session. On the admin handler the caller
// is the host by construction.
func (s *Server) sessionFor(r *http.Request, admin bool) (session, bool) {
	if admin {
		return session{name: s.HostName, host: true}, true
	}
	c, err := r.Cookie(cookieName)
	if err != nil {
		return session{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[c.Value]
	return sess, ok
}

func (s *Server) authed(admin bool, h func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.sessionFor(r, admin)
		if !ok {
			http.Error(w, "not joined", http.StatusUnauthorized)
			return
		}
		h(w, r, sess)
	}
}

func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	if !s.allowJoin(clientIP(r)) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many attempts, wait a minute"})
		return
	}
	var req struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
		return
	}
	name := cleanName(req.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "please enter a name"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Key), []byte(s.Secret())) != 1 {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "this invite link is no longer valid"})
		return
	}
	sid := randToken(32)
	s.mu.Lock()
	s.sessions[sid] = session{name: name}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: sid, Path: "/",
		HttpOnly: true, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteStrictMode, MaxAge: 7 * 24 * 3600,
	})
	writeJSON(w, http.StatusOK, map[string]any{"name": name})
}

// allowJoin permits 10 join attempts per IP per minute.
func (s *Server) allowJoin(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	recent := s.joins[ip][:0]
	for _, t := range s.joins[ip] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= 10 {
		s.joins[ip] = recent
		return false
	}
	s.joins[ip] = append(recent, now)
	return true
}

func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request, _ session) {
	it := s.Item()
	if it == nil {
		http.NotFound(w, r)
		return
	}
	v, ok := it.Variant(r.PathValue("variant"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(v.Path)
	if err != nil {
		s.log.Error("open media", "err", err)
		http.Error(w, "media unavailable", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "media unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	// Private: never let an intermediary (the tunnel's CDN) cache the movie.
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, "", st.ModTime(), f)
}

func (s *Server) handleSub(w http.ResponseWriter, r *http.Request, _ session) {
	it := s.Item()
	if it == nil {
		http.NotFound(w, r)
		return
	}
	sub, ok := it.Subtitle(strings.TrimSuffix(r.PathValue("sub"), ".vtt"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-cache")
	_, _ = w.Write(sub.VTT)
}

func (s *Server) handleAdminStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Hooks.Status())
}

func (s *Server) handleAdminAction(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
		return
	}
	res, err := s.Hooks.Action(r.PathValue("action"), body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleSubtitleUpload takes the raw file as the body and its URL-encoded
// name in X-Filename.
func (s *Server) handleSubtitleUpload(w http.ResponseWriter, r *http.Request) {
	name, err := url.PathUnescape(r.Header.Get("X-Filename"))
	if err != nil || name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing file name"})
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, media.MaxSubtitleSize))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "file too large (max 10 MB)"})
		return
	}
	if err := s.Hooks.UploadSubtitle(name, data); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// AddSubtitle appends a track to the current item and tells everyone.
// The item is replaced, not mutated: readers hold the old pointer safely.
func (s *Server) AddSubtitle(sub media.Subtitle) error {
	s.mu.Lock()
	if s.item == nil {
		s.mu.Unlock()
		return errors.New("pick a movie first")
	}
	next := *s.item
	next.Subs = append(append([]media.Subtitle(nil), s.item.Subs...), sub)
	s.item = &next
	s.mu.Unlock()
	s.broadcastMedia()
	return nil
}

// app serves the embedded single-page app. Unknown paths fall back to
// index.html; hashed assets are cached forever.
func (s *Server) app() http.Handler {
	files := http.FileServerFS(s.web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if st, err := fs.Stat(s.web, p); err == nil && !st.IsDir() {
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		index, err := fs.ReadFile(s.web, "index.html")
		if err != nil {
			http.Error(w, "web app not built", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy",
			"default-src 'self'; media-src 'self' blob:; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
				"connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.ServeHTTP(w, r)
	})
}

// localOnly rejects requests whose Host is not a loopback name on our port
// (DNS rebinding) and state-changing requests from a foreign Origin (CSRF).
func localOnly(port int, h http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, host := range []string{"127.0.0.1", "localhost", "[::1]"} {
		allowed[host+":"+strconv.Itoa(port)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[r.Host] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && r.Method != http.MethodGet {
			if !allowed[strings.TrimPrefix(strings.TrimPrefix(o, "http://"), "https://")] {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

func cleanName(n string) string {
	n = strings.TrimSpace(n)
	n = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, n)
	for utf8.RuneCountInString(n) > maxNameLen {
		_, size := utf8.DecodeLastRuneInString(n)
		n = n[:len(n)-size]
	}
	return strings.TrimSpace(n)
}

func clientIP(r *http.Request) string {
	// Behind cloudflared, the real client IP is in CF-Connecting-IP; the TCP
	// peer is always loopback. Only used for rate limiting.
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
