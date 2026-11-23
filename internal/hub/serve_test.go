package hub

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/proc"
)

// login ends a fake chain at the login session on every OS: proc.Rooted
// reads the name on Windows and the PID elsewhere.
var login = proc.Info{PID: 1, Name: "explorer"}

// startServer runs a hub process server in-process on a short socket path
// (Windows and Unix cap it near 100 bytes). anchors maps a peer PID to the
// harness the test pretends it runs under; the kernel still reports the
// real PID, which is this test process for every client.
func startServer(t *testing.T, anchor func(pid int) (int, string)) (*Server, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "ilh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := core.Open(filepath.Join(dir, "hub.db"), core.Options{Stale: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.SingleConn()
	srv := NewServer(st)
	srv.Anchor = anchor
	srv.Ancestry = func(int) ([]proc.Info, error) { return []proc.Info{login}, nil } // tests set the chain
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("no local sockets here: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); ln.Close() })
	go func() { _ = srv.Serve(ctx, ln) }()
	return srv, sock
}

func dial(t *testing.T, sock string) *Remote {
	t.Helper()
	r, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestHubProcessTakesIdentityFromTheKernel(t *testing.T) {
	harness := "claude"
	_, sock := startServer(t, func(pid int) (int, string) { return 4242, harness })
	ctx := context.Background()
	me := os.Getpid()

	a := dial(t, sock)
	var hello Hello
	if err := a.Call(ctx, "hello", nil, &hello); err != nil || hello.Peer != me || hello.Anchor != 4242 || !hello.Agent {
		t.Fatalf("hello: %+v %v", hello, err)
	}
	// The anchor a caller claims is ignored: it's the kernel's.
	var agent core.Agent
	if err := a.Call(ctx, "register", core.RegisterReq{Harness: "claude-code", Project: "shop", ProjectPath: "/w/shop", AnchorPID: 1, SessionID: "s1"}, &agent); err != nil {
		t.Fatal(err)
	}
	if agent.Key != "" {
		t.Fatal("the key stays in the hub")
	}
	if err := a.Call(ctx, "status", core.StatusReq{ID: agent.ID, Activity: "hi"}, nil); err != nil {
		t.Fatalf("acting as yourself: %v", err)
	}
	// Another connection can't act as that agent without registering as it.
	b := dial(t, sock)
	if err := b.Call(ctx, "status", core.StatusReq{ID: agent.ID, Activity: "spoof"}, nil); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("acting as another: %v", err)
	}
	// A process inside an agent's harness is never the human.
	if _, err := b.Human(ctx, "box"); err == nil || !strings.Contains(err.Error(), "inside an agent's harness") {
		t.Fatalf("agent as human: %v", err)
	}
}

func TestTheHumanIsWhoeverIsOutsideEveryHarness(t *testing.T) {
	anchor := 7000 // a terminal, not a harness
	_, sock := startServer(t, func(pid int) (int, string) { return anchor, "windowsterminal" })
	ctx := context.Background()
	h := dial(t, sock)
	human, err := h.Human(ctx, "box")
	if err != nil || human.Name != "human" {
		t.Fatalf("human: %+v %v", human, err)
	}
	if err := h.Call(ctx, "status", core.StatusReq{ID: human.ID, Activity: "here"}, nil); err != nil {
		t.Fatal(err)
	}
	// Once an agent registers under that same process tree, it isn't the
	// human's any more (an agent's shell running the CLI).
	var a core.Agent
	if err := h.Call(ctx, "register", core.RegisterReq{Harness: "codex", Project: "shop", ProjectPath: "/w/shop", SessionID: "c1"}, &a); err != nil {
		t.Fatal(err)
	}
	if _, err := dial(t, sock).Human(ctx, "box"); err == nil {
		t.Fatal("a registered agent's tree became the human")
	}
}

// The whole chain of ancestors counts, not the nearest program: an agent
// wrapping the CLI in node (or any program ainterlinked doesn't skip) is
// still inside its harness, and so is a process under a registered agent's
// harness whatever that harness's executable is called.
func TestTheHumanCantBeAnAgentsGrandchild(t *testing.T) {
	var mu sync.Mutex
	anchor, chain := 7100, []proc.Info{{PID: 7100, PPID: 4242, Name: "node"}, {PID: 4242, PPID: 1, Name: "claude"}, login}
	var chainErr error
	set := func(a int, c []proc.Info, err error) { mu.Lock(); anchor, chain, chainErr = a, c, err; mu.Unlock() }
	srv, sock := startServer(t, func(int) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		for _, p := range chain {
			if p.PID == anchor {
				return anchor, p.Name
			}
		}
		return anchor, ""
	})
	srv.Ancestry = func(int) ([]proc.Info, error) { mu.Lock(); defer mu.Unlock(); return chain, chainErr }
	ctx := context.Background()
	if _, err := dial(t, sock).Human(ctx, "box"); err == nil || !strings.Contains(err.Error(), "inside an agent's harness") {
		t.Fatalf("the CLI wrapped in node under claude became the human: %v", err)
	}
	// A harness ainterlinked doesn't know by name, but registered: gemini
	// runs as node (pid 5000). A process under it is that agent's.
	set(5000, []proc.Info{{PID: 5000, PPID: 1, Name: "node"}, login}, nil)
	var gem core.Agent
	if err := dial(t, sock).Call(ctx, "register", core.RegisterReq{Harness: "gemini", Project: "shop", ProjectPath: "/w/shop", SessionID: "g1"}, &gem); err != nil {
		t.Fatalf("gemini: %+v %v", gem, err)
	}
	set(7200, []proc.Info{{PID: 7200, PPID: 5000, Name: "go"}, {PID: 5000, PPID: 1, Name: "node"}, login}, nil)
	if _, err := dial(t, sock).Human(ctx, "box"); err == nil {
		t.Fatal("a process under a registered agent's harness became the human")
	}
	// A chain that can't be read is not the human's either.
	set(7300, nil, errors.New("gone"))
	if _, err := dial(t, sock).Human(ctx, "box"); err == nil {
		t.Fatal("an unreadable chain became the human")
	}
	// A terminal outside every harness is.
	set(7400, []proc.Info{{PID: 7400, PPID: 1, Name: "windowsterminal"}, login}, nil)
	if h, err := dial(t, sock).Human(ctx, "box"); err != nil || h.Name != "human" {
		t.Fatalf("the human's own terminal: %+v %v", h, err)
	}
}

// A chain that stops before the login session is unknown, and unknown is
// not the human: an agent's `timeout 30 ainterlinked` in Git Bash stops at
// timeout, whose parent (a forked bash) has exited.
func TestABrokenChainIsNotTheHuman(t *testing.T) {
	var mu sync.Mutex
	chain := []proc.Info{{PID: 52228, PPID: 54200, Name: "timeout"}, {PID: 54200, PPID: 27848, Name: "timeout"}}
	srv, sock := startServer(t, func(int) (int, string) { return 52228, "timeout" })
	srv.Ancestry = func(int) ([]proc.Info, error) { mu.Lock(); defer mu.Unlock(); return chain, nil }
	ctx := context.Background()
	if _, err := dial(t, sock).Human(ctx, "box"); err == nil || !strings.Contains(err.Error(), "stop at timeout (pid 54200)") {
		t.Fatalf("a chain broken by a wrapper became the human: %v", err)
	}
	var hello Hello
	if err := dial(t, sock).Call(ctx, "hello", nil, &hello); err != nil || !hello.Agent || !strings.Contains(hello.Why, "login session") {
		t.Fatalf("hello on a broken chain: %+v %v", hello, err)
	}
	// No ancestors at all is no chain to the session either.
	mu.Lock()
	chain = nil
	mu.Unlock()
	if _, err := dial(t, sock).Human(ctx, "box"); err == nil {
		t.Fatal("a process with no visible parent became the human")
	}
	// The same wrapper with its chain whole (the human's own terminal,
	// where timeout's parent is still running) is the human.
	mu.Lock()
	chain = []proc.Info{{PID: 52228, PPID: 54200, Name: "timeout"}, {PID: 54200, PPID: 7000, Name: "bash"}, {PID: 7000, PPID: 1, Name: "windowsterminal"}, login}
	mu.Unlock()
	if h, err := dial(t, sock).Human(ctx, "box"); err != nil || h.Name != "human" {
		t.Fatalf("the human's whole chain: %+v %v", h, err)
	}
}

func TestHubProcessClientsSurviveARestart(t *testing.T) {
	srv, sock := startServer(t, func(pid int) (int, string) { return 4242, "claude" })
	ctx := context.Background()
	r := dial(t, sock)
	var agent core.Agent
	if err := r.Call(ctx, "register", core.RegisterReq{Harness: "claude-code", Project: "shop", ProjectPath: "/w/shop", SessionID: "s1"}, &agent); err != nil {
		t.Fatal(err)
	}
	// A long wait and a quick call share the connection.
	done := make(chan error, 1)
	go func() {
		var out any
		done <- r.Call(ctx, "wait", map[string]any{"agent": agent.ID, "timeout_seconds": 1}, &out)
	}()
	var agents []core.Agent
	if err := r.Call(ctx, "agents", core.AgentFilter{}, &agents); err != nil || len(agents) != 1 {
		t.Fatalf("a call while another waits: %v %v", agents, err)
	}
	<-done
	// The connection drops (the hub process restarted): the client comes
	// back as the same agent without the caller doing anything.
	r.mu.Lock()
	r.conn.Close()
	r.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	if err := r.Call(ctx, "agents", core.AgentFilter{}, &agents); err != nil {
		t.Fatal(err)
	}
	if err := r.Call(ctx, "status", core.StatusReq{ID: agent.ID, Activity: "still me"}, nil); err != nil {
		t.Fatalf("after reconnect: %v", err)
	}
	_ = srv
}
