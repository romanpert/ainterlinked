package hub

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/proc"
	"github.com/romanpert/ainterlinked/internal/transcript"
)

// Server is the hub process's side of the socket. Every connection gets its
// identity from the kernel: the PID on the other end, walked up to its
// harness (proc.AnchorOf). What a caller says about itself is not believed:
//
//   - register: the anchor is the connection's, whatever the params say;
//   - acting as an agent: only as one this connection registered (the keys
//     stay inside the hub; nothing a caller can read or copy);
//   - acting as the human: only from a process whose chain of parents
//     reaches the login session without passing an agent harness. The
//     ainterlinked CLI run from an agent's shell is that agent; one whose
//     chain is broken is nobody the hub can vouch for (see notHuman).
type Server struct {
	Store   *core.Store
	methods map[string]handler
	// Anchor resolves a PID to its harness process (proc.AnchorOf by
	// default; tests replace it).
	Anchor func(pid int) (int, string)
	// Ancestry lists a PID's ancestors (proc.Ancestry; tests replace it).
	Ancestry func(pid int) ([]proc.Info, error)

	active atomic.Int64 // requests in flight
	conns  atomic.Int64

	regMu      sync.Mutex
	registered map[string]bool // agents registered through the hub process

	// spender reads harnesses' transcripts for spend, each once and then
	// only what's new: the hub process lives long enough for that.
	spender *transcript.Spender

	wakeIdle    atomic.Bool  // the user's delivery.wake_idle
	archiveDays atomic.Int64 // the user's tasks.archive_after_days (archive.go)
}

// NewServer serves st.
func NewServer(st *core.Store) *Server {
	return &Server{Store: st, methods: Methods(st), Anchor: proc.AnchorOf, Ancestry: proc.Ancestry, registered: map[string]bool{},
		spender: transcript.NewSpender()}
}

func (s *Server) registeredIDs() []string {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	out := make([]string, 0, len(s.registered))
	for id := range s.registered {
		out = append(out, id)
	}
	return out
}

// Busy reports whether anyone is connected or a request is running.
func (s *Server) Busy() bool { return s.conns.Load() > 0 || s.active.Load() > 0 }

// Serve accepts connections until ln is closed.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.serveConn(ctx, c)
	}
}

// peer is what the hub knows about the other end of a connection.
type peer struct {
	pid, anchor int
	harness     string // the anchor's executable name
	local       *Local // keys of the agents this connection registered
}

func (s *Server) serveConn(ctx context.Context, c net.Conn) {
	s.conns.Add(1)
	defer s.conns.Add(-1)
	defer c.Close()
	p := &peer{local: &Local{Store: s.Store, methods: s.methods, keys: map[string]string{}}}
	if uc, ok := c.(*net.UnixConn); ok {
		if pid, err := peerPID(uc); err == nil && pid > 0 {
			p.pid = pid
			p.anchor, p.harness = s.Anchor(pid)
		}
	}
	if p.pid == 0 {
		return // no kernel identity, no service
	}
	var wmu sync.Mutex
	enc := json.NewEncoder(c)
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	var wg sync.WaitGroup
	defer wg.Wait()
	for sc.Scan() {
		var q wireReq
		if err := json.Unmarshal(sc.Bytes(), &q); err != nil {
			continue
		}
		s.active.Add(1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer s.active.Add(-1)
			resp := wireResp{ID: q.ID}
			res, err := s.handle(ctx, p, q.Method, q.Params)
			if err != nil {
				resp.Error = err.Error()
			} else if b, err := json.Marshal(res); err != nil {
				resp.Error = err.Error()
			} else {
				resp.Result = b
			}
			wmu.Lock()
			_ = enc.Encode(resp)
			wmu.Unlock()
		}()
	}
}

// Hello is what the hub process says about itself and about the caller.
type Hello struct {
	Version string `json:"version"`
	PID     int    `json:"pid"`
	Peer    int    `json:"peer"`
	Anchor  int    `json:"anchor"`
	Harness string `json:"harness,omitempty"`
	Agent   bool   `json:"agent"`         // the caller can't act as the human operator
	Why     string `json:"why,omitempty"` // why not, when Agent
}

// Version is the build's version, set by the CLI.
var Version = "dev"

var errHumanFromAgent = errors.New("refused: this process runs inside an agent's harness, so it acts as that agent, not as the human operator")

func (s *Server) handle(ctx context.Context, p *peer, method string, raw json.RawMessage) (any, error) {
	switch method {
	case "hello":
		h := Hello{Version: Version, PID: s.pidSelf(), Peer: p.pid, Anchor: p.anchor, Harness: p.harness}
		if err := s.notHuman(ctx, p); err != nil {
			h.Agent, h.Why = true, err.Error()
		}
		return h, nil
	case "human":
		if err := s.notHuman(ctx, p); err != nil {
			return nil, err
		}
		var q struct {
			Machine string `json:"machine"`
		}
		_ = json.Unmarshal(raw, &q)
		a, err := p.local.Human(ctx, q.Machine)
		a.Key = "" // it stays in the hub
		return a, err
	case "register":
		var r core.RegisterReq
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
		if p.anchor <= 0 {
			return nil, errors.New("refused: the hub can't tell which session this process belongs to")
		}
		r.AnchorPID = p.anchor // the kernel's answer, not the caller's
		if m := s.Store.MachineName(); m != "" {
			r.Machine = m // this hub's machine, not the caller's word
		}
		b, err := json.Marshal(r)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	actor, _, err := actorOf(method, raw)
	if err != nil {
		return nil, err
	}
	if method == "spend" && p.local.key(actor) != "" {
		s.readSpend(ctx, actor) // the agent's own, read up to now
	}
	res, err := p.local.run(ctx, method, raw, p.local.key(actor))
	if a, ok := res.(core.Agent); ok && err == nil && method == "register" {
		p.local.remember(a)
		s.regMu.Lock()
		s.registered[a.ID] = true
		s.regMu.Unlock()
		a.Key = "" // it stays in the hub
		return a, nil
	}
	return res, err
}

func (s *Server) pidSelf() int { return os.Getpid() }

// readSpend records what the agent's harness transcript says it has spent.
// A transcript the hub can't find leaves the last record as it was.
func (s *Server) readSpend(ctx context.Context, agent string) {
	a, err := s.Store.Get(ctx, agent)
	if err != nil || a.SessionID == "" {
		return
	}
	if ledger, ok := s.spender.Read(a.Harness, a.SessionID); ok {
		_ = s.Store.RecordSpend(ctx, a.ID, ledger)
	}
}

// notHuman says why the caller can't act as the human operator, or nil when
// it can. A caller inside an agent's harness is never the human. The whole
// chain of ancestors counts, not just the nearest program: the CLI wrapped
// in node (or anything else) by an agent is still inside that agent's
// harness. And the chain must reach the login session (proc.Rooted): a
// wrapper that breaks it, like Git Bash's timeout, hides the harness above
// the break. A chain that can't be read or is broken is unknown, and
// unknown is not the human.
func (s *Server) notHuman(ctx context.Context, p *peer) error {
	if p.anchor <= 0 || proc.IsHarness(p.harness) {
		return errHumanFromAgent
	}
	chain, err := s.Ancestry(p.pid)
	if err != nil {
		return fmt.Errorf("refused: the hub can't read this process's parents (%v), so it isn't the human operator", err)
	}
	pids := []int{p.anchor}
	for _, a := range chain {
		if proc.IsHarness(a.Name) {
			return errHumanFromAgent
		}
		pids = append(pids, a.PID)
	}
	if !proc.Rooted(chain) {
		return errBrokenChain(chain)
	}
	if taken, err := s.Store.AnchorTaken(ctx, pids...); err != nil || taken {
		return errHumanFromAgent
	}
	return nil
}

func errBrokenChain(chain []proc.Info) error {
	at := "the process itself"
	if n := len(chain); n > 0 {
		at = fmt.Sprintf("%s (pid %d)", chain[n-1].Name, chain[n-1].PID)
	}
	return fmt.Errorf("refused: the hub can't trace this process back to the login session: its parents stop at %s, whose parent is gone "+
		"(%s), so this isn't the human operator.\n%s", at, brokenWhy[runtime.GOOS == "windows"], brokenFix[runtime.GOOS == "windows"])
}

// What breaks a chain, and what works instead, by OS (true: Windows). On
// Unix an orphan goes to pid 1, so a chain breaks only in another PID
// namespace, such as a container.
var (
	brokenWhy = map[bool]string{
		true:  "Git Bash's timeout, env or window break the chain like that, and an agent's process looks the same",
		false: "a container or another PID namespace hides the parents, and an agent's process may look the same",
	}
	brokenFix = map[bool]string{
		true: "A terminal opened from the Start menu or taskbar (Windows Terminal, PowerShell, cmd) works, and so does an IDE started from the Start menu; " +
			"an IDE launched with `code .` or relaunched by its updater may not: reopen it from the Start menu",
		false: "Run it from a terminal of your own login session, not inside a container",
	}
)
