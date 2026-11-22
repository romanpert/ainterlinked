package hub

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/proc"
)

// Server is the hub process's side of the socket. Every connection gets its
// identity from the kernel: the PID on the other end, walked up to its
// harness (proc.AnchorOf). What a caller says about itself is not believed:
//
//   - register: the anchor is the connection's, whatever the params say;
//   - acting as an agent: only as one this connection registered (the keys
//     stay inside the hub; nothing a caller can read or copy);
//   - acting as the human: only from a process that isn't inside an agent
//     harness. The ainterlinked CLI run from an agent's shell is that agent.
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
}

// NewServer serves st.
func NewServer(st *core.Store) *Server {
	return &Server{Store: st, methods: Methods(st), Anchor: proc.AnchorOf, Ancestry: proc.Ancestry, registered: map[string]bool{}}
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
	Agent   bool   `json:"agent"` // the caller is inside an agent harness
}

// Version is the build's version, set by the CLI.
var Version = "dev"

var errHumanFromAgent = errors.New("refused: this process runs inside an agent's harness, so it acts as that agent, not as the human operator")

func (s *Server) handle(ctx context.Context, p *peer, method string, raw json.RawMessage) (any, error) {
	switch method {
	case "hello":
		return Hello{Version: Version, PID: s.pidSelf(), Peer: p.pid, Anchor: p.anchor, Harness: p.harness, Agent: s.agentAnchored(ctx, p)}, nil
	case "human":
		if s.agentAnchored(ctx, p) {
			return nil, errHumanFromAgent
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

// agentAnchored: the caller runs inside an agent's harness. Such a caller
// is never the human. The whole chain of ancestors counts, not just the
// nearest program: the CLI wrapped in node (or anything else) by an agent
// is still inside that agent's harness. When the chain can't be read, the
// answer is "agent": unknown is not the human.
func (s *Server) agentAnchored(ctx context.Context, p *peer) bool {
	if p.anchor <= 0 || proc.IsHarness(p.harness) {
		return true
	}
	chain, err := s.Ancestry(p.pid)
	if err != nil {
		return true
	}
	pids := []int{p.anchor}
	for _, a := range chain {
		if proc.IsHarness(a.Name) {
			return true
		}
		pids = append(pids, a.PID)
	}
	taken, err := s.Store.AnchorTaken(ctx, pids...)
	return err != nil || taken
}
