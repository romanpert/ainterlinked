package hub

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/romanpert/ainterlinked/internal/core"
)

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
