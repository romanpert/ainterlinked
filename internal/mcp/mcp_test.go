package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/romanpert/interlinked/internal/config"
	"github.com/romanpert/interlinked/internal/core"
	"github.com/romanpert/interlinked/internal/harness"
	"github.com/romanpert/interlinked/internal/hub"
)

// client drives one in-process MCP server over pipes.
type client struct {
	t      *testing.T
	in     *io.PipeWriter
	out    *bufio.Scanner
	nextID int
	done   chan struct{}
	roots  string
}

func start(t *testing.T, h *hub.Local, harness, pid, root string) *client {
	t.Helper()
	t.Setenv("INTERLINKED_ANCHOR_PID", pid)
	cfg := config.Default()
	cfg.Machine = "box"
	srv := New(cfg, h, harness)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &client{t: t, in: inW, out: bufio.NewScanner(outR), done: make(chan struct{}), roots: root}
	c.out.Buffer(make([]byte, 1<<20), 1<<24)
	go func() { _ = srv.Serve(context.Background(), inR, outW); outW.Close(); close(c.done) }()
	res := c.call("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"roots": map[string]any{}},
		"clientInfo": map[string]any{"name": "test-client", "version": "1"}})
	if res["serverInfo"].(map[string]any)["name"] != "interlinked" || res["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize: %v", res)
	}
	c.notify("notifications/initialized", nil)
	return c
}

func (c *client) write(v any) {
	b, _ := json.Marshal(v)
	if _, err := c.in.Write(append(b, '\n')); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) notify(method string, params any) {
	c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// call sends a request and reads until its response, answering roots/list.
func (c *client) call(method string, params any) map[string]any {
	c.t.Helper()
	c.nextID++
	id := c.nextID
	c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	for c.out.Scan() {
		var m map[string]any
		if err := json.Unmarshal(c.out.Bytes(), &m); err != nil {
			c.t.Fatalf("bad JSON from server: %s", c.out.Text())
		}
		if m["method"] == "roots/list" {
			u := url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(c.roots)}
			c.write(map[string]any{"jsonrpc": "2.0", "id": m["id"], "result": map[string]any{"roots": []any{map[string]any{"uri": u.String()}}}})
			continue
		}
		if m["id"] == nil {
			continue // notification
		}
		if fmt.Sprint(m["id"]) != fmt.Sprint(id) {
			continue
		}
		if e, ok := m["error"]; ok {
			c.t.Fatalf("%s error: %v", method, e)
		}
		return m["result"].(map[string]any)
	}
	c.t.Fatalf("server closed while waiting for %s", method)
	return nil
}

func (c *client) tool(name string, args map[string]any) (string, bool) {
	c.t.Helper()
	res := c.call("tools/call", map[string]any{"name": name, "arguments": args})
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	return text, res["isError"] == true
}

func TestTwoAgentsTalkOverMCP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("INTERLINKED_HOME", home)
	st, err := core.Open(filepath.Join(home, "hub.db"), core.Options{Stale: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := hub.NewLocal(st)
	project := filepath.Join(t.TempDir(), "shop")

	claude := start(t, h, "claude-code", "11", project)
	tools := claude.call("tools/list", nil)["tools"].([]any)
	if len(tools) != 17 {
		t.Fatalf("expected 17 tools, got %d", len(tools))
	}
	who, isErr := claude.tool("whoami", nil)
	if isErr || !strings.Contains(who, "claude@shop") {
		t.Fatalf("whoami: %s", who)
	}

	codex := start(t, h, "codex", "22", project)
	list, _ := codex.tool("list_agents", nil)
	if !strings.Contains(list, "claude@shop") || !strings.Contains(list, "codex@shop") {
		t.Fatalf("list_agents: %s", list)
	}

	// codex waits; claude sends; codex's wait returns the message.
	got := make(chan string, 1)
	go func() {
		text, _ := codex.tool("wait_for_messages", map[string]any{"timeout_seconds": 20})
		got <- text
	}()
	time.Sleep(500 * time.Millisecond)
	sent, isErr := claude.tool("send_message", map[string]any{"to": "codex@shop", "body": "take task #1 please", "subject": "split"})
	if isErr || !strings.Contains(sent, "codex@shop") {
		t.Fatalf("send: %s", sent)
	}
	select {
	case text := <-got:
		if !strings.Contains(text, "take task #1 please") {
			t.Fatalf("wait returned %q", text)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wait_for_messages did not return")
	}

	// Task board round trip.
	created, _ := claude.tool("create_task", map[string]any{"title": "write checkout API", "priority": 1, "labels": []string{"backend"}})
	if !strings.Contains(created, "#1 [todo] P1 write checkout API") {
		t.Fatalf("create_task: %s", created)
	}
	claimed, _ := codex.tool("claim_task", map[string]any{"label": "backend"})
	if !strings.Contains(claimed, "[doing]") || !strings.Contains(claimed, "@codex@shop") {
		t.Fatalf("claim_task: %s", claimed)
	}
	if text, isErr := claude.tool("claim_task", map[string]any{"id": 1}); !isErr {
		t.Fatalf("second claim should fail: %s", text)
	}
	codex.tool("update_task", map[string]any{"id": 1, "status": "done", "result": "POST /checkout added"})
	inbox, _ := claude.tool("read_inbox", nil)
	if !strings.Contains(inbox, "POST /checkout added") {
		t.Fatalf("creator should be notified: %s", inbox)
	}

	// Reservations.
	r, _ := claude.tool("reserve_files", map[string]any{"paths": []string{"api/"}})
	if !strings.Contains(r, "reserved") {
		t.Fatalf("reserve: %s", r)
	}
	r, _ = codex.tool("reserve_files", map[string]any{"paths": []string{"api/checkout.go"}})
	if !strings.Contains(r, "NOT reserved") {
		t.Fatalf("conflict expected: %s", r)
	}

	// Queue for a held file: the holder is told, the file passes on release.
	q, _ := codex.tool("reserve_files", map[string]any{"paths": []string{"api/checkout.go"}, "wait": true, "reason": "fix totals"})
	if !strings.Contains(q, "queued") || !strings.Contains(q, "#1 in line") {
		t.Fatalf("queue: %s", q)
	}
	told, _ := claude.tool("read_inbox", nil)
	if !strings.Contains(told, "fix totals") || !strings.Contains(told, "from interlinked") {
		t.Fatalf("holder should be told who waits: %s", told)
	}
	claude.tool("release_files", nil)
	mine, _ := codex.tool("list_reservations", nil)
	if !strings.Contains(mine, "api/checkout.go — codex@shop") {
		t.Fatalf("file should pass to codex: %s", mine)
	}

	// Each agent tunes its own delivery; group mail arrives as briefs.
	set, isErr := codex.tool("set_delivery", map[string]any{"inject": "turn", "subscribe": []string{"reload"}})
	if isErr || !strings.Contains(set, "inject turn") || !strings.Contains(set, "#reload") {
		t.Fatalf("set_delivery: %s", set)
	}
	if show, _ := claude.tool("set_delivery", nil); !strings.Contains(show, "broadcasts brief (default)") || !strings.Contains(show, "#reload(1)") {
		t.Fatalf("set_delivery with no args shows settings and topics: %s", show)
	}
	sent, _ = claude.tool("send_message", map[string]any{"to": "project", "subject": "core reload at 10:00", "body": strings.Repeat("details ", 200)})
	if !strings.Contains(sent, "one-line brief to 1") {
		t.Fatalf("project mail is a brief: %s", sent)
	}
	brief, _ := codex.tool("read_inbox", nil)
	if !strings.Contains(brief, "core reload at 10:00") || strings.Count(brief, "details") > 30 {
		t.Fatalf("brief should be one short line: %s", brief)
	}
	if th, _ := codex.tool("read_inbox", map[string]any{"threads": true}); !strings.Contains(th, "thread #") {
		t.Fatalf("threads: %s", th)
	}

	// Unknown tool → error result, not a crash.
	if _, isErr := claude.tool("nope", nil); !isErr {
		t.Fatal("unknown tool should be an error")
	}

	// Closing stdin ends the session and takes the agent offline.
	claude.in.Close()
	select {
	case <-claude.done:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not exit on EOF")
	}
	list, _ = codex.tool("list_agents", nil)
	if !strings.Contains(list, "claude@shop [claude-code] leaving") {
		t.Fatalf("claude should be leaving: %s", list)
	}
	codex.in.Close()
	<-codex.done
}

// Harnesses that pre-approve tools by name must know every tool.
func TestToolNamesMatchHarnessAllowList(t *testing.T) {
	allowed := map[string]bool{}
	for _, n := range harness.ToolNames {
		allowed[n] = true
	}
	for _, d := range toolDefs() {
		if !allowed[d.Name] {
			t.Errorf("tool %q is missing from harness.ToolNames", d.Name)
		}
		delete(allowed, d.Name)
	}
	for n := range allowed {
		t.Errorf("harness.ToolNames lists %q, which is not a tool", n)
	}
}
