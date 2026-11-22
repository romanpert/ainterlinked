// Package web serves the hub over HTTP: /rpc for remote machines, /api for
// the dashboard, and the dashboard itself.
package web

import (
	"context"
	"crypto/subtle"
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
	Hub     *hub.Local
	Token   string
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

// Listen serves until ctx is cancelled. A non-loopback address requires a token.
func (s *Server) Listen(ctx context.Context, addr string) error {
	if !IsLoopback(addr) && s.Token == "" {
		return errors.New("refusing to listen on a non-loopback address without a token (set --token or token in config.toml)")
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
	mux.HandleFunc("/rpc", s.auth(s.rpc))
	mux.HandleFunc("/api/state", s.auth(s.state))
	mux.HandleFunc("/api/send", s.auth(s.send))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Browsers on other sites must not drive a local hub (CSRF / DNS rebinding).
		if origin := r.Header.Get("Origin"); origin != "" && !strings.HasSuffix(origin, "://"+r.Host) {
			writeJSON(w, http.StatusForbidden, hub.Response{Error: "cross-origin request refused"})
			return
		}
		// Without a token the hub is loopback-only; also refuse foreign Host
		// headers so a DNS-rebinding page can't reach it.
		if s.Token == "" && !loopbackHost(r.Host) {
			writeJSON(w, http.StatusForbidden, hub.Response{Error: "host not allowed"})
			return
		}
		if s.Token != "" {
			// Header only: a token in the URL would end up in history and logs.
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
				writeJSON(w, http.StatusUnauthorized, hub.Response{Error: "bad or missing token"})
				return
			}
		}
		next(w, r)
	}
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

func (s *Server) rpc(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, hub.Response{Error: "POST only"})
		return
	}
	var req hub.Request
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, hub.Response{Error: "bad request: " + err.Error()})
		return
	}
	res, err := s.Hub.Dispatch(r.Context(), req.Method, req.Params)
	if err != nil {
		writeJSON(w, http.StatusOK, hub.Response{Error: err.Error()})
		return
	}
	b, _ := json.Marshal(res)
	writeJSON(w, http.StatusOK, hub.Response{Result: b})
}

type state struct {
	Machine   string          `json:"machine"`
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
	st := s.Hub.Store
	out := state{Machine: s.Machine, Version: s.Version}
	var err error
	if out.Agents, err = st.List(ctx, core.AgentFilter{IncludeOffline: true}); err != nil {
		writeJSON(w, 500, hub.Response{Error: err.Error()})
		return
	}
	out.Tasks, _ = st.ListTasks(ctx, core.TaskFilter{Board: "*"})
	out.Boards, _ = st.Boards(ctx)
	out.Locks, _ = st.Locks(ctx, "")
	out.Messages, _ = st.RecentMessages(ctx, 40)
	out.Feed, _ = st.Feed(ctx, core.FeedReq{Limit: 80})
	out.Harnesses = s.harnesses()
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
func Human(ctx context.Context, c hub.Client, machine string) (core.Agent, error) {
	var a core.Agent
	err := c.Call(ctx, "register", core.RegisterReq{
		Harness: "human", Name: "human", Machine: machine, Project: "", SessionID: "human@" + machine, Via: "cli", Delivery: "pull",
	}, &a)
	return a, err
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
	m, err := s.Hub.Store.Send(r.Context(), core.SendReq{From: me.ID, To: req.To, Body: req.Body})
	if err != nil {
		writeJSON(w, http.StatusOK, hub.Response{Error: err.Error()})
		return
	}
	b, _ := json.Marshal(m)
	writeJSON(w, http.StatusOK, hub.Response{Result: b})
}
