// Package hub exposes the core operations through one method table, so the
// same calls work against the local SQLite hub or a remote `interlinked
// serve` over HTTP. MCP tools, hooks, the CLI and the web dashboard all go
// through a Client.
package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/romanpert/interlinked/internal/config"
	"github.com/romanpert/interlinked/internal/core"
	"github.com/romanpert/interlinked/internal/proc"
)

// Client calls a hub method.
type Client interface {
	Call(ctx context.Context, method string, params, out any) error
	Close() error
}

type handler func(ctx context.Context, raw json.RawMessage) (any, error)

func op[P any, R any](f func(context.Context, P) (R, error)) handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p P
		if len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, fmt.Errorf("bad params: %w", err)
			}
		}
		return f(ctx, p)
	}
}

// Param structs for methods that take more than one scalar.
type (
	IDParam struct {
		ID string `json:"id"`
	}
	RefParam struct {
		Ref string `json:"ref"`
	}
	AgentParam struct {
		Agent string `json:"agent"`
	}
	WaitParam struct {
		Agent   string `json:"agent"`
		Timeout int    `json:"timeout_seconds"`
	}
	ThreadParam struct {
		Agent string `json:"agent"`
		ID    int64  `json:"id"`
	}
	LimitParam struct {
		Limit int `json:"limit"`
	}
	ClaimParam struct {
		Agent string `json:"agent"`
		ID    int64  `json:"id"`
	}
	NextParam struct {
		Agent string `json:"agent"`
		Board string `json:"board"`
		Label string `json:"label"`
	}
	ReleaseParam struct {
		Agent string   `json:"agent"`
		Paths []string `json:"paths"`
	}
	ProjectParam struct {
		ProjectPath string `json:"project_path"`
	}
	FileParam struct {
		Agent string `json:"agent"`
		File  string `json:"file"`
	}
	NoteParam struct {
		Agent   string `json:"agent"`
		Kind    string `json:"kind"`
		Summary string `json:"summary"`
	}
	UnreadResult struct {
		Total  int `json:"total"`
		Urgent int `json:"urgent"`
	}
	NextResult struct {
		Task *core.Task `json:"task,omitempty"`
		OK   bool       `json:"ok"`
	}
	ReleaseResult struct {
		Released int `json:"released"`
	}
	Empty struct{}
)

// Methods builds the dispatch table over a store.
func Methods(s *core.Store) map[string]handler {
	return map[string]handler{
		"register": op(s.Register),
		"status":   op(s.SetStatus),
		"leave": op(func(ctx context.Context, p IDParam) (Empty, error) {
			return Empty{}, s.Leave(ctx, p.ID)
		}),
		"agents":  op(s.List),
		"agent":   op(func(ctx context.Context, p IDParam) (core.Agent, error) { return s.Get(ctx, p.ID) }),
		"resolve": op(func(ctx context.Context, p RefParam) (core.Agent, error) { return s.Resolve(ctx, p.Ref) }),
		"send":    op(s.Send),
		"inbox":   op(s.Inbox),
		"unread": op(func(ctx context.Context, p AgentParam) (UnreadResult, error) {
			t, u, err := s.Unread(ctx, p.Agent)
			return UnreadResult{t, u}, err
		}),
		"wait": op(func(ctx context.Context, p WaitParam) ([]core.Message, error) {
			return s.Wait(ctx, p.Agent, time.Duration(p.Timeout)*time.Second)
		}),
		"thread":      op(func(ctx context.Context, p ThreadParam) ([]core.Message, error) { return s.Thread(ctx, p.Agent, p.ID) }),
		"messages":    op(func(ctx context.Context, p LimitParam) ([]core.Message, error) { return s.RecentMessages(ctx, p.Limit) }),
		"task_create": op(s.CreateTask),
		"tasks":       op(s.ListTasks),
		"task_update": op(s.UpdateTask),
		"task_claim":  op(func(ctx context.Context, p ClaimParam) (core.Task, error) { return s.Claim(ctx, p.Agent, p.ID) }),
		"task_next": op(func(ctx context.Context, p NextParam) (NextResult, error) {
			t, ok, err := s.ClaimNext(ctx, p.Agent, p.Board, p.Label)
			if !ok {
				return NextResult{}, err
			}
			return NextResult{Task: &t, OK: true}, err
		}),
		"boards":  op(func(ctx context.Context, _ Empty) ([]core.Board, error) { return s.Boards(ctx) }),
		"reserve": op(s.Reserve),
		"release": op(func(ctx context.Context, p ReleaseParam) (ReleaseResult, error) {
			n, err := s.Release(ctx, p.Agent, p.Paths)
			return ReleaseResult{n}, err
		}),
		"locks":      op(func(ctx context.Context, p ProjectParam) ([]core.Lock, error) { return s.Locks(ctx, p.ProjectPath) }),
		"check_file": op(func(ctx context.Context, p FileParam) (*core.Lock, error) { return s.CheckFile(ctx, p.Agent, p.File) }),
		"feed":       op(s.Feed),
		"note": op(func(ctx context.Context, p NoteParam) (Empty, error) {
			return Empty{}, s.Note(ctx, p.Agent, p.Kind, p.Summary)
		}),
	}
}

// Local calls the store in-process.
type Local struct {
	Store   *core.Store
	methods map[string]handler
}

// NewLocal wraps a store.
func NewLocal(s *core.Store) *Local { return &Local{Store: s, methods: Methods(s)} }

// Dispatch runs a method with raw JSON params (used by the HTTP server too).
func (l *Local) Dispatch(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	h, ok := l.methods[method]
	if !ok {
		return nil, fmt.Errorf("unknown method %q", method)
	}
	return h(ctx, raw)
}

// Call implements Client.
func (l *Local) Call(ctx context.Context, method string, params, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	res, err := l.Dispatch(ctx, method, raw)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	b, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// Close closes the store.
func (l *Local) Close() error { return l.Store.Close() }

// Request/Response are the HTTP wire format of /rpc.
type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}
type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Remote calls a hub served by `interlinked serve`.
type Remote struct {
	Base  string
	Token string
	HTTP  *http.Client
}

// Call implements Client.
func (r *Remote) Call(ctx context.Context, method string, params, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(Request{Method: method, Params: raw})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.Base, "/")+"/rpc", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("hub %s unreachable: %w", r.Base, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	var rr Response
	if err := json.Unmarshal(data, &rr); err != nil {
		return fmt.Errorf("hub %s: HTTP %d: %s", r.Base, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if rr.Error != "" {
		return errors.New(rr.Error)
	}
	if out == nil || len(rr.Result) == 0 {
		return nil
	}
	return json.Unmarshal(rr.Result, out)
}

// Close implements Client.
func (r *Remote) Close() error { return nil }

// Open returns the client for the configured hub: remote when cfg.Hub is
// set, otherwise the local database.
func Open(cfg config.Config) (Client, error) {
	if cfg.Hub != "" {
		// Long enough for wait() long-polls; per-call contexts bound it.
		return &Remote{Base: cfg.Hub, Token: cfg.Token, HTTP: &http.Client{Timeout: 35 * time.Minute}}, nil
	}
	st, err := core.Open(config.DBPath(), core.Options{
		Stale:   time.Duration(cfg.Presence.StaleSeconds) * time.Second,
		Machine: cfg.Machine,
		Alive:   proc.AliveSet,
	})
	if err != nil {
		return nil, err
	}
	return NewLocal(st), nil
}
