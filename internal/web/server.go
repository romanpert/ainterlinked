// Package web serves this machine's dashboard: the page and its /api, on
// loopback only.
package web

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/hub"
)

//go:embed dashboard.html
var dashboard []byte

// Server is the HTTP front of a local hub.
type Server struct {
	Hub     hub.Client
	Machine string
	Version string
	// Scan reports the harnesses on this machine (re-run at most once a
	// minute) so the dashboard shows what's detected but not wired.
	Scan func() []HarnessStatus

	scanMu   sync.Mutex
	scanAt   time.Time
	scanLast []HarnessStatus
}

// HarnessStatus is one row of the machine scan.
type HarnessStatus struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Detected bool   `json:"detected"`
	MCP      bool   `json:"mcp"`
	Hooks    bool   `json:"hooks"`
	HasHooks bool   `json:"has_hooks"`
	Tier     string `json:"tier"`
	Enabled  bool   `json:"enabled"`
}

func (s *Server) harnesses() []HarnessStatus {
	if s.Scan == nil {
		return []HarnessStatus{}
	}
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if s.scanLast == nil || time.Since(s.scanAt) > time.Minute {
		s.scanLast, s.scanAt = s.Scan(), time.Now()
	}
	return s.scanLast
}

// IsLoopback reports whether addr only listens on the local machine.
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Listen serves until ctx is cancelled, on loopback only: the dashboard is
// this machine's (connecting devices is the hosted service's job).
func (s *Server) Listen(ctx context.Context, addr string) error {
	if !IsLoopback(addr) {
		return errors.New("the dashboard listens on this machine only (127.0.0.1 or localhost)")
	}
	srv := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	err := srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Handler builds the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'")
		w.Write(dashboard)
	})
	mux.HandleFunc("/api/state", local(s.state))
	mux.HandleFunc("/api/send", local(s.send))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	return mux
}

// local admits only requests from this machine's own browser: a loopback
// Host (so a DNS-rebinding page can't reach it), nothing that came through
// a proxy (to it, every client looks like loopback), and no other site's
// page (CSRF).
func local(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && !strings.HasSuffix(origin, "://"+r.Host) {
			writeJSON(w, http.StatusForbidden, hub.Response{Error: "cross-origin request refused"})
			return
		}
		if !loopbackHost(r.Host) || proxied(r) {
			writeJSON(w, http.StatusForbidden, hub.Response{Error: "host not allowed"})
			return
		}
		next(w, r)
	}
}

// proxied reports whether a request was forwarded by a proxy.
func proxied(r *http.Request) bool {
	for _, h := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Real-Ip"} {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	return false
}

func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

type state struct {
	Machine   string          `json:"machine"`
	Load      *core.Machine   `json:"load,omitempty"` // nil: no session publishes it
	Version   string          `json:"version"`
	Agents    []core.Agent    `json:"agents"`
	Tasks     []core.Task     `json:"tasks"`
	Boards    []core.Board    `json:"boards"`
	Locks     []core.Lock     `json:"locks"`
	Messages  []core.Message  `json:"messages"`
	Feed      []core.Event    `json:"feed"`
	Harnesses []HarnessStatus `json:"harnesses"`
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c := s.Hub // a client like any other: the hub process decides who it is
	out := state{Machine: s.Machine, Version: s.Version}
	if err := c.Call(ctx, "agents", core.AgentFilter{IncludeOffline: true}, &out.Agents); err != nil {
		writeJSON(w, 500, hub.Response{Error: err.Error()})
		return
	}
	_ = c.Call(ctx, "tasks", core.TaskFilter{Board: "*"}, &out.Tasks)
	_ = c.Call(ctx, "boards", hub.Empty{}, &out.Boards)
	_ = c.Call(ctx, "locks", hub.ProjectParam{}, &out.Locks)
	_ = c.Call(ctx, "messages", hub.LimitParam{Limit: 40}, &out.Messages)
	_ = c.Call(ctx, "feed", core.FeedReq{Limit: 80}, &out.Feed)
	out.Harnesses = s.harnesses()
	var m core.Machine
	if err := c.Call(ctx, "machine", hub.Empty{}, &m); err == nil && m.Fresh {
		out.Load = &m
	}
	if out.Agents == nil {
		out.Agents = []core.Agent{}
	}
	if out.Tasks == nil {
		out.Tasks = []core.Task{}
	}
	if out.Boards == nil {
		out.Boards = []core.Board{}
	}
	if out.Locks == nil {
		out.Locks = []core.Lock{}
	}
	if out.Messages == nil {
		out.Messages = []core.Message{}
	}
	if out.Feed == nil {
		out.Feed = []core.Event{}
	}
	writeJSON(w, http.StatusOK, out)
}

// Human registers (or refreshes) the "human" agent the dashboard and CLI send as.
// Agents can't register as the human through the hub's "register" method.
func Human(ctx context.Context, c hub.Client, machine string) (core.Agent, error) {
	return hub.Human(ctx, c, machine)
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, hub.Response{Error: "POST only"})
		return
	}
	var req struct {
		To   string `json:"to"`
		Body string `json:"body"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, hub.Response{Error: "bad request"})
		return
	}
	me, err := Human(r.Context(), s.Hub, s.Machine)
	if err != nil {
		writeJSON(w, http.StatusOK, hub.Response{Error: err.Error()})
		return
	}
	var m core.Message
	if err := s.Hub.Call(r.Context(), "send", core.SendReq{From: me.ID, To: req.To, Body: req.Body}, &m); err != nil {
		writeJSON(w, http.StatusOK, hub.Response{Error: err.Error()})
		return
	}
	b, _ := json.Marshal(m)
	writeJSON(w, http.StatusOK, hub.Response{Result: b})
}
