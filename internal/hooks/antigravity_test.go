package hooks

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/hub"
)

const conv = "ec33ebf9-0cba-4100-8142-c61503f6c587"

// ag runs an Antigravity hook as its docs say it is called.
func (e *env) ag(event string, payload map[string]any) (Result, map[string]any) {
	e.t.Helper()
	p := map[string]any{"cwd": "", "conversationId": conv, "workspacePaths": []any{e.dir}, "modelName": "gemini"}
	for k, v := range payload {
		p[k] = v
	}
	return e.as("1", "antigravity", event, p)
}

func (e *env) invoke(input string) string {
	e.t.Helper()
	res, _ := e.ag("PreInvocation", map[string]any{"lastUserInput": input})
	return res.Context
}

func TestAntigravityIsRefusedAReservedFile(t *testing.T) {
	e := setup(t)
	e.as("2", "claude-code", "SessionStart", nil)
	var r core.LockResult
	if err := e.c.Call(context.Background(), "reserve", core.LockReq{Agent: e.agent("2").ID, Paths: []string{"src/"}, Reason: "task #4"}, &r); err != nil || len(r.Granted) != 1 {
		t.Fatal(err, r)
	}
	call := func(tool, file string) map[string]any {
		return map[string]any{"toolCall": map[string]any{"name": tool, "args": map[string]any{"TargetFile": filepath.Join(e.dir, file)}}}
	}
	for _, tool := range []string{"write_to_file", "replace_file_content", "multi_replace_file_content"} {
		_, out := e.ag("PreToolUse", call(tool, "src/main.go"))
		if s, _ := out["reason"].(string); out["decision"] != "deny" || !strings.Contains(s, "reserved") {
			t.Errorf("%s: %v", tool, out)
		}
	}
	// Anything else is left to the user's own permissions: never auto_approve.
	if _, out := e.ag("PreToolUse", call("write_to_file", "free.go")); out["decision"] != "allow" {
		t.Errorf("free file: %v", out)
	}
	if a := e.agent("1"); a.Harness != "antigravity" || a.SessionID != conv || a.Model != "gemini" {
		t.Errorf("agent: %+v", a)
	}
}

// The model writes a tool's Cwd: it says where a command runs, never whose
// project the reservations are.
func TestAntigravityToolCwdPicksNoProject(t *testing.T) {
	e := setup(t)
	e.as("2", "claude-code", "SessionStart", nil)
	var r core.LockResult
	if err := e.c.Call(context.Background(), "reserve", core.LockReq{Agent: e.agent("2").ID, Paths: []string{"src/"}, Reason: "task #4"}, &r); err != nil || len(r.Granted) != 1 {
		t.Fatal(err, r)
	}
	e.ag("SessionStart", nil)
	want := e.agent("1").ProjectPath
	tool := func(name string, args map[string]any) map[string]any {
		return map[string]any{"toolCall": map[string]any{"name": name, "args": args}}
	}
	_, out := e.ag("PreToolUse", tool("write_to_file", map[string]any{"TargetFile": filepath.Join(e.dir, "src", "main.go"), "Cwd": t.TempDir()}))
	if out["decision"] != "deny" {
		t.Errorf("another Cwd let the edit through: %v", out)
	}
	e.ag("PostToolUse", tool("run_command", map[string]any{"CommandLine": "ls", "Cwd": t.TempDir()}))
	if got := e.agent("1").ProjectPath; got != want {
		t.Errorf("a command's Cwd moved the agent to %s", got)
	}
	e.ag("PostToolUse", tool("run_command", map[string]any{"CommandLine": "echo x > main.go", "Cwd": filepath.Join(e.dir, "src")}))
	var u hub.UnreadResult
	_ = e.c.Call(context.Background(), "unread", hub.AgentParam{Agent: e.agent("2").ID}, &u)
	if u.Total == 0 {
		t.Error("a relative write wasn't resolved from the command's Cwd")
	}
}

func TestAntigravityMail(t *testing.T) {
	e := setup(t)
	e.ag("SessionStart", nil)
	e.as("2", "claude-code", "SessionStart", nil)
	e.invoke("build it")
	e.send("2", "1", "first")
	// Its PostToolUse shows nothing: mail waits there for the next model call.
	_, out := e.ag("PostToolUse", map[string]any{"toolCall": map[string]any{"name": "run_command", "args": map[string]any{"CommandLine": "go test ./..."}}})
	if len(out) != 0 {
		t.Fatalf("PostToolUse: %v", out)
	}
	if a := e.agent("1"); a.Activity != "run_command: go test" {
		t.Fatalf("activity %q", a.Activity)
	}
	_, out = e.ag("PreInvocation", map[string]any{"lastUserInput": "build it"})
	steps, _ := out["injectSteps"].([]any)
	if len(steps) != 1 {
		t.Fatalf("PreInvocation: %v", out)
	}
	step := steps[0].(map[string]any)
	if s, _ := step["userMessage"].(string); !strings.Contains(s, "first") || len(step) != 1 {
		t.Fatalf("step: %v", step)
	}
	e.send("2", "1", "second")
	if _, out = e.ag("Stop", map[string]any{"terminationReason": "error"}); out["decision"] != "stop" {
		t.Fatalf("an error isn't continued: %v", out)
	}
	if _, out = e.ag("Stop", map[string]any{"terminationReason": "model_stop"}); out["decision"] != "continue" {
		t.Fatalf("Stop: %v", out)
	}
	if _, out = e.ag("Stop", map[string]any{"terminationReason": "model_stop"}); out["decision"] != "stop" {
		t.Fatalf("no mail: %v", out)
	}
	var u hub.UnreadResult
	_ = e.c.Call(context.Background(), "unread", hub.AgentParam{Agent: e.agent("1").ID}, &u)
	if u.Total != 0 {
		t.Fatalf("unread %d", u.Total)
	}
}

// Antigravity has no prompt hook: what resets the count of turns peers keep
// going must be the user's words and nothing peers or the agent bring about.
func TestWhatCountsAsAnAntigravityPrompt(t *testing.T) {
	e := setup(t)
	e.ag("SessionStart", nil)
	e.invoke("build it")
	id := e.agent("1").ID
	holds := func() int { return Holds(id) }
	BumpHolds(id)
	for name, do := range map[string]func(){
		"another call of the same turn": func() { e.invoke("build it") },
		"no input":                      func() { e.invoke("") },
		"our own text":                  func() { e.invoke("[ainterlinked] 1 new message(s). build more") },
		"a subagent's conversation": func() {
			e.ag("PreInvocation", map[string]any{"conversationId": "sub", "lastUserInput": "look into X"})
			e.ag("PreInvocation", map[string]any{"conversationId": "sub", "lastUserInput": "now into Y"})
		},
	} {
		if do(); holds() != 1 {
			t.Fatalf("%s passed as the user's prompt", name)
		}
	}
	if e.invoke("thanks, now the docs"); holds() != 0 {
		t.Fatal("the user's prompt starts over")
	}
	// A subagent from the user's own turn is a subagent in a later one too,
	// whether or not the turn it came from is over.
	talk := func(id, input string) {
		e.ag("PreInvocation", map[string]any{"conversationId": id, "lastUserInput": input})
	}
	stop := func(id string, idle bool) {
		e.ag("Stop", map[string]any{"conversationId": id, "terminationReason": "model_stop", "fullyIdle": idle})
	}
	stop("sub", true)
	talk("early", "look into X")
	stop("early", true)
	stop(conv, false) // its background work goes on
	talk("background", "look into W")
	stop("background", true)
	stop(conv, true)
	BumpHolds(id)
	talk("held", "look into V") // nothing under way, but peers hold the turn
	stop("held", true)
	for _, c := range []string{"early", "background", "held"} {
		if talk(c, "now into Z"); holds() != 1 {
			t.Fatalf("the %s subagent passed as the user", c)
		}
		stop(c, true)
	}
	// The user's next conversation, with nothing running, is the user's.
	ResetHolds(id)
	talk("second", "another thing")
	BumpHolds(id)
	if talk("second", "and one more"); holds() != 0 {
		t.Fatal("the user's second conversation doesn't count")
	}
}

func TestAntigravityTurnsThatNeverStopped(t *testing.T) {
	t.Setenv("AINTERLINKED_HOME", t.TempDir())
	now := time.Now()
	turn := func(ago time.Duration) map[string]conversation {
		return map[string]conversation{"a": {Open: true, Seen: now.Add(-ago).Unix()}}
	}
	if !underWay(turn(openFor-time.Minute), now) || underWay(turn(openFor+time.Minute), now) {
		t.Fatal("a turn with no Stop is under way for openFor")
	}
	seen := map[string]conversation{}
	for i := range maxConversations + 1 {
		seen[fmt.Sprint(i)] = conversation{Seen: int64(i)}
	}
	if err := saveConversations("x", seen); err != nil {
		t.Fatal(err)
	}
	got := conversations("x")
	if _, oldest := got["0"]; oldest || len(got) != maxConversations {
		t.Fatalf("kept %d, the oldest: %v", len(got), oldest)
	}
}
