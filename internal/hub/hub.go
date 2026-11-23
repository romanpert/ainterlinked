// Package hub exposes the core operations through one method table. MCP
// tools, hooks, the CLI and the web dashboard all go through a Client on
// this machine's hub.
package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/romanpert/ainterlinked/internal/config"
	"github.com/romanpert/ainterlinked/internal/core"
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
		// To hands the reservations to that agent instead (it must be
		// queued for them): a cession, not a release.
		To string `json:"to,omitempty"`
	}
	FactsParam struct {
		Viewer string   `json:"viewer"`
		Names  []string `json:"names"`
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
		Full   int `json:"full"`   // delivered in full: named, or a full-mode group/topic
		Direct int `json:"direct"` // addressed to the agent by name or id: may hold a turn
	}
	ThreadsParam struct {
		Agent string `json:"agent"`
		Limit int    `json:"limit"`
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
		"register": op(func(ctx context.Context, r core.RegisterReq) (core.Agent, error) {
			if core.ClaimsHuman(r) {
				return core.Agent{}, errHuman // the operator registers through Local.Human
			}
			return s.Register(ctx, r)
		}),
		"status": op(s.SetStatus),
		"leave": op(func(ctx context.Context, p IDParam) (Empty, error) {
			return Empty{}, s.Leave(ctx, p.ID)
		}),
		"depart": op(func(ctx context.Context, p IDParam) (Empty, error) {
			return Empty{}, s.Depart(ctx, p.ID)
		}),
		"agents":  op(s.List),
		"agent":   op(func(ctx context.Context, p IDParam) (core.Agent, error) { return s.Get(ctx, p.ID) }),
		"resolve": op(func(ctx context.Context, p RefParam) (core.Agent, error) { return s.Resolve(ctx, p.Ref) }),
		"send":    op(s.Send),
		"inbox":   op(s.Inbox),
		"unread": op(func(ctx context.Context, p AgentParam) (UnreadResult, error) {
			t, u, f, d, err := s.UnreadByMode(ctx, p.Agent)
			return UnreadResult{t, u, f, d}, err
		}),
		"prefs":     op(func(ctx context.Context, p AgentParam) (core.Prefs, error) { return s.GetPrefs(ctx, p.Agent) }),
		"set_prefs": op(s.SetPrefs),
		"topics": op(func(ctx context.Context, p ProjectParam) (map[string]int, error) {
			return s.Topics(ctx, p.ProjectPath)
		}),
		"threads": op(func(ctx context.Context, p ThreadsParam) ([]core.ThreadSummary, error) {
			return s.Threads(ctx, p.Agent, p.Limit)
		}),
		"waiters": op(func(ctx context.Context, p ProjectParam) ([]core.Waiter, error) { return s.Waiters(ctx, p.ProjectPath) }),
		"wait": op(func(ctx context.Context, p WaitParam) ([]core.Message, error) {
			return s.Wait(ctx, p.Agent, time.Duration(p.Timeout)*time.Second)
		}),
		"thread":      op(func(ctx context.Context, p ThreadParam) ([]core.Message, error) { return s.Thread(ctx, p.Agent, p.ID) }),
		"messages":    op(func(ctx context.Context, p LimitParam) ([]core.Message, error) { return s.RecentMessages(ctx, p.Limit) }),
		"task_create": op(s.CreateTask),
		"tasks":       op(s.ListTasks),
		"task_update": op(s.UpdateTask),
		"task_claim":  op(func(ctx context.Context, p ClaimParam) (core.Task, error) { return s.Claim(ctx, p.Agent, p.ID) }),
		"task_want":   op(func(ctx context.Context, p ClaimParam) (core.Task, error) { return s.WantTask(ctx, p.Agent, p.ID) }),
		"trespass":    op(s.RecordTrespass),
		"turn":        op(s.MarkTurn),
		"wake":        op(func(ctx context.Context, p AgentParam) (bool, error) { return s.Wake(ctx, p.Agent) }),
		"spend":       op(s.GetSpend),
		"budget":      op(s.SetBudget),
		"get_budget":  op(func(ctx context.Context, _ Empty) (core.Budget, error) { return s.GetBudget(ctx) }),
		"shared":      op(s.SetShared),
		"get_shared": op(func(ctx context.Context, p core.SharedReq) (core.SharedList, error) {
			return s.GetShared(ctx, p.ProjectPath)
		}),
		"handovers":     op(s.Handovers),
		"handover":      op(s.GetHandover),
		"handover_note": op(s.LeaveHandover),
		"facts": op(func(ctx context.Context, p FactsParam) (map[string]core.Facts, error) {
			return s.AgentFacts(ctx, p.Viewer, p.Names)
		}),
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
			if p.To != "" {
				n, err := s.Cede(ctx, p.Agent, p.To, p.Paths)
				return ReleaseResult{n}, err
			}
			n, err := s.Release(ctx, p.Agent, p.Paths)
			return ReleaseResult{n}, err
		}),
		"stale_tasks":    op(s.StaleTasks),
		"locks":          op(func(ctx context.Context, p ProjectParam) ([]core.Lock, error) { return s.Locks(ctx, p.ProjectPath) }),
		"check_file":     op(func(ctx context.Context, p FileParam) (*core.Lock, error) { return s.CheckFile(ctx, p.Agent, p.File) }),
		"touch_file":     op(func(ctx context.Context, p FileParam) (*core.Lock, error) { return s.TouchFile(ctx, p.Agent, p.File) }),
		"feed":           op(s.Feed),
		"machine":        op(func(ctx context.Context, _ Empty) (core.Machine, error) { return s.Machine(ctx) }),
		"machine_sample": op(s.SampleMachine),
		"note": op(func(ctx context.Context, p NoteParam) (Empty, error) {
			return Empty{}, s.Note(ctx, p.Agent, p.Kind, p.Summary)
		}),
	}
}

// Local calls the store in-process.
type Local struct {
	Store   *core.Store
	methods map[string]handler

	mu   sync.Mutex
	keys map[string]string // agent id → key, for the agents registered through this Local
}

// NewLocal wraps a store.
func NewLocal(s *core.Store) *Local {
	return &Local{Store: s, methods: Methods(s), keys: map[string]string{}}
}

// Dispatch runs a method with raw JSON params; a method that acts as an
// agent needs that agent's key in params.key (keys.go).
func (l *Local) Dispatch(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	return l.run(ctx, method, raw, keyParam(raw))
}

// run is the one place keys are checked.
func (l *Local) run(ctx context.Context, method string, raw json.RawMessage, key string) (any, error) {
	h, ok := l.methods[method]
	if !ok {
		return nil, fmt.Errorf("unknown method %q", method)
	}
	actor, acts, err := actorOf(method, raw)
	if err != nil {
		return nil, err
	}
	if acts {
		if err := l.Store.Authorize(ctx, actor, key); err != nil {
			return nil, err
		}
	}
	return h(ctx, raw)
}

// Call implements Client, carrying the key of the agent the call acts as
// when this Local registered it.
func (l *Local) Call(ctx context.Context, method string, params, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	var key string
	if actor, acts, _ := actorOf(method, raw); acts { // run refuses malformed params
		key = l.key(actor)
	}
	res, err := l.run(ctx, method, raw, key)
	if err != nil {
		return err
	}
	if a, ok := res.(core.Agent); ok && method == "register" {
		l.remember(a)
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

// Response is the JSON envelope of the dashboard's API.
type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Open returns the client for this machine's hub (its local database).
func Open(cfg config.Config) (Client, error) { return OpenWithin(cfg, 0) }

// Depart says goodbye for a session that may come straight back (see
// core.Depart); a hub too old to know "depart" gets a plain leave.
func Depart(ctx context.Context, c Client, id string) error {
	if err := c.Call(ctx, "depart", IDParam{ID: id}, nil); err != nil {
		return c.Call(ctx, "leave", IDParam{ID: id}, nil)
	}
	return nil
}

// OpenWithin is Open for a caller with a deadline (a hook): it waits at most
// busy for the database write lock instead of the default 10 s, so under
// contention it fails fast instead of stalling the agent's tool call.
func OpenWithin(cfg config.Config, busy time.Duration) (Client, error) {
	return OpenAuto(cfg, busy)
}
