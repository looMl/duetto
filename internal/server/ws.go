package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"duetto/internal/room"
)

// Wire protocol (JSON text frames).
//
// client → server
//   {"t":"ping","c":<client ms>}
//   {"t":"cmd","op":"play"|"pause"|"seek","pos":<seconds>}
//   {"t":"status","buffering":bool,"ahead":s,"drift":s,"unsupported":bool}
// server → client
//   {"t":"hello","you":id,"name":..,"host":bool,"state":{..},"viewers":[..],"media":{..}|null}
//   {"t":"pong","c":<echo>,"s":<server ms>}
//   {"t":"state","state":{..}}  {"t":"viewers","viewers":[..]}  {"t":"media","media":{..}|null}

type inMsg struct {
	T           string  `json:"t"`
	C           float64 `json:"c"`
	Op          string  `json:"op"`
	Pos         float64 `json:"pos"`
	Buffering   bool    `json:"buffering"`
	Ahead       float64 `json:"ahead"`
	Drift       float64 `json:"drift"`
	Unsupported bool    `json:"unsupported"`
}

type conn struct {
	out    chan []byte
	cancel context.CancelFunc
	once   sync.Once
}

// send enqueues a message; a client too slow to drain 256 messages is
// disconnected rather than allowed to stall the room.
func (c *conn) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	select {
	case c.out <- b:
	default:
		c.once.Do(c.cancel)
	}
}

func (s *Server) addConn(c *conn) {
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	s.conns[c] = struct{}{}
}

func (s *Server) removeConn(c *conn) {
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	delete(s.conns, c)
}

func (s *Server) broadcastMedia() {
	msg := map[string]any{"t": "media", "media": s.Item()}
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	for c := range s.conns {
		c.send(msg)
	}
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request, sess session) {
	ws, err := websocket.Accept(w, r, nil) // default: same-origin only
	if err != nil {
		return
	}
	ws.SetReadLimit(4096)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	c := &conn{out: make(chan []byte, 256), cancel: cancel}
	id := randToken(9)
	member := &room.Member{
		Viewer: room.Viewer{ID: id, Name: sess.name, Host: sess.host},
		Send:   c.send,
	}

	// Register first, then snapshot: an update landing in between is queued
	// before the welcome, which then carries the same or newer state.
	s.addConn(c)
	s.room.Join(member)
	c.send(map[string]any{
		"t": "hello", "you": id, "name": sess.name, "host": sess.host,
		"state": s.room.State(), "viewers": s.room.Viewers(), "media": s.Item(),
	})
	s.log.Info("viewer joined", "name", sess.name, "host", sess.host)
	defer func() {
		s.removeConn(c)
		s.room.Leave(id)
		s.log.Info("viewer left", "name", sess.name)
	}()

	go s.writeLoop(ctx, ws, c)

	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			if !errors.Is(err, context.Canceled) && websocket.CloseStatus(err) == -1 && !errors.Is(err, io.EOF) {
				s.log.Debug("ws read", "err", err)
			}
			ws.CloseNow()
			return
		}
		var m inMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.T {
		case "ping":
			c.send(map[string]any{"t": "pong", "c": m.C, "s": s.room.NowMs()})
		case "cmd":
			s.room.Command(id, room.Op(m.Op), m.Pos)
		case "status":
			s.room.Report(id, room.Status{Buffering: m.Buffering, Ahead: m.Ahead, Drift: m.Drift, Unsupported: m.Unsupported})
		}
	}
}

func (s *Server) writeLoop(ctx context.Context, ws *websocket.Conn, c *conn) {
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			ws.CloseNow()
			return
		case b := <-c.out:
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := ws.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				c.once.Do(c.cancel)
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := ws.Ping(pctx)
			cancel()
			if err != nil {
				c.once.Do(c.cancel)
				return
			}
		}
	}
}
