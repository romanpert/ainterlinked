package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/romanpert/ainterlinked/internal/config"
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
	// What Antigravity CLI 1.5.14 sends, which isn't what its docs say.
	_, out := e.ag("PreToolUse", map[string]any{"toolCall": map[string]any{"id": "call_1", "name": "write_file", "args": map[string]any{
		"Content": "CHANGED", "Overwrite": true, "Path": filepath.Join(e.dir, "src", "main.go")}}})
	if out["decision"] != "deny" {
		t.Errorf("write_file: %v", out)
	}
	_, out = e.ag("PreToolUse", map[string]any{"toolCall": map[string]any{"name": "read_file", "args": map[string]any{"Path": filepath.Join(e.dir, "src", "main.go")}}})
	if out["decision"] != "allow" {
		t.Errorf("a read: %v", out)
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
	for _, p := range []map[string]any{{"terminationReason": "max_steps_exceeded"}, {"terminationReason": "NO_TOOL_CALL", "error": "quota"}} {
		if _, out = e.ag("Stop", p); out["decision"] != "stop" {
			t.Fatalf("%v isn't continued: %v", p, out)
		}
	}
	if _, out = e.ag("Stop", map[string]any{"terminationReason": "NO_TOOL_CALL", "error": "", "fullyIdle": true}); out["decision"] != "continue" {
		t.Fatalf("Stop as the CLI sends it: %v", out)
	}
	e.send("2", "1", "third")
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
	for i := range maxConversations + 1 {
		if err := saveConversation(conversationPath("x", fmt.Sprint(i)), conversation{Seen: int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	userPrompted("x", Input{SessionID: "new", Prompt: "build it"})
	got, _ := conversations("x")
	_, oldest := got[conversationPath("x", "0")]
	if _, latest := got[conversationPath("x", "new")]; oldest || !latest || len(got) != maxConversations+1 {
		t.Fatalf("kept %d, the oldest: %v, the new one: %v", len(got), oldest, latest)
	}
}

func TestAntigravityConversationThatCantBeRead(t *testing.T) {
	t.Setenv("AINTERLINKED_HOME", t.TempDir())
	spoil := func(id string, ago time.Duration) string {
		p := conversationPath("x", id)
		if err := saveConversation(p, conversation{}); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-ago)
		if err := errors.Join(os.WriteFile(p, []byte(`{"input": "ab`), 0o600), os.Chtimes(p, at, at)); err != nil {
			t.Fatal(err)
		}
		return p
	}
	old := spoil("old", 24*time.Hour)
	if !userPrompted("x", Input{SessionID: "first", Prompt: "build it"}) {
		t.Error("a spoilt file from yesterday is a turn under way for ever")
	}
	turnOver("x", Input{SessionID: "first"})
	half := spoil("half", 0)
	if userPrompted("x", Input{SessionID: "new", Prompt: "look into X"}) {
		t.Error("a conversation that appeared meanwhile passed as the user")
	}
	if userPrompted("x", Input{SessionID: "half", Prompt: "now into Y"}) {
		t.Error("the one that can't be read passed as the user")
	}
	if c, _, _ := readConversation(half); !c.Other || c.Input == "" {
		t.Errorf("the spoilt one isn't put right: %+v", c)
	}
	for i := range maxConversations + 1 {
		_ = saveConversation(conversationPath("x", fmt.Sprint(i)), conversation{Seen: time.Now().Unix()})
	}
	userPrompted("x", Input{SessionID: "last", Prompt: "and Z"})
	if _, err := os.Stat(old); err == nil {
		t.Error("the spoilt file is never forgotten")
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(old), "*.tmp")); len(left) > 0 {
		t.Errorf("left behind: %v", left)
	}
}

// A file that can't be read for now is neither the user's nor written over.
func TestAntigravityConversationThatCantBeReadNow(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("nothing to refuse the read with")
	}
	t.Setenv("AINTERLINKED_HOME", t.TempDir())
	p := conversationPath("x", "locked")
	if err := errors.Join(saveConversation(p, conversation{Input: "i", Open: true}), os.Chmod(p, 0)); err != nil {
		t.Fatal(err)
	}
	if userPrompted("x", Input{SessionID: "locked", Prompt: "build it"}) {
		t.Error("passed as the user")
	}
	turnOver("x", Input{SessionID: "locked"})
	_ = os.Chmod(p, 0o600)
	if c, _, err := readConversation(p); err != nil || c.Input != "i" || c.Other {
		t.Errorf("%+v, %v", c, err)
	}
}

// What is under way is told from the files whatever the folder is called, and
// a folder that can't be listed says nothing about it.
func TestAntigravityFolderThatCantBeListed(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home[1]")
	t.Setenv("AINTERLINKED_HOME", home)
	if !userPrompted("x", Input{SessionID: "main", Prompt: "build it"}) || userPrompted("x", Input{SessionID: "sub", Prompt: "look into X"}) {
		t.Error("a subagent passed as the user")
	}
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		return // nothing to refuse the listing with
	}
	dir := filepath.Dir(conversationPath("x", ""))
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	turnOver("x", Input{SessionID: "main"})
	if userPrompted("x", Input{SessionID: "sub2", Prompt: "look into X"}) {
		t.Error("passed as the user")
	}
}

// An app runs hooks of several conversations at the same time: none may lose
// another's mark, or a subagent could pass as the user.
func TestAntigravityHooksAtTheSameTime(t *testing.T) {
	t.Setenv("AINTERLINKED_HOME", t.TempDir())
	const n = 12
	if !userPrompted("x", Input{SessionID: "main", Prompt: "build it"}) {
		t.Fatal("the user's first prompt")
	}
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if userPrompted("x", Input{SessionID: fmt.Sprint("sub", i), Prompt: "look into it"}) {
				t.Errorf("subagent %d passed as the user", i)
			}
		}()
	}
	wg.Wait()
	for i := range n {
		if c, ok, _ := readConversation(conversationPath("x", fmt.Sprint("sub", i))); !ok || !c.Other {
			t.Errorf("subagent %d: %+v, known: %v", i, c, ok)
		}
	}
	if c, _, _ := readConversation(conversationPath("x", "main")); c.Other || !c.Open {
		t.Errorf("the user's conversation: %+v", c)
	}
}

// The briefing put in on SessionStart is the last input from then on, so a
// conversation's first prompt is seen on SessionStart or not at all.
func TestAntigravityFirstPromptIsTheUsers(t *testing.T) {
	e := setup(t)
	e.ag("SessionStart", map[string]any{"lastUserInput": "build it"})
	id := e.agent("1").ID
	if c, _, _ := readConversation(conversationPath(id, conv)); c.Input == "" || c.Other {
		t.Fatalf("the first prompt didn't count: %+v", c)
	}
	if a := e.agent("1"); a.Activity != "on a new request from the user" {
		t.Fatalf("activity %q", a.Activity)
	}
	BumpHolds(id)
	for _, in := range []string{"[ainterlinked] You are ...", "build it"} {
		if e.invoke(in); Holds(id) != 1 {
			t.Fatalf("%q counted again", in)
		}
	}
	// A subagent's start is nobody's prompt.
	e.ag("SessionStart", map[string]any{"conversationId": "sub", "lastUserInput": "look into X"})
	if c, _, _ := readConversation(conversationPath(id, "sub")); !c.Other || Holds(id) != 1 {
		t.Fatalf("a subagent passed as the user: %+v", c)
	}
}

// The hub learns whose turn it is from SessionStart too.
func TestAntigravityFirstPromptStartsTheUsersTurn(t *testing.T) {
	e := setup(t)
	e.ag("SessionStart", nil)
	id := e.agent("1").ID
	if n, _ := e.c.Store.MarkTurn(context.Background(), core.TurnReq{Agent: id}); n != 1 { // peers held the last one open
		t.Fatal(n)
	}
	// The agent's only conversation, so nothing else holds it.
	e.ag("SessionStart", map[string]any{"lastUserInput": "build it"})
	if n, _ := e.c.Store.MarkTurn(context.Background(), core.TurnReq{Agent: id}); n != 1 {
		t.Fatalf("what it does now isn't on the user's order: %d", n)
	}
}

// A conversation that only opened isn't a turn under way.
func TestAntigravityStartWithNoPrompt(t *testing.T) {
	e := setup(t)
	e.ag("SessionStart", map[string]any{"lastUserInput": "[ainterlinked] You are ..."}) // resumed
	e.ag("SessionStart", map[string]any{"conversationId": "second", "lastUserInput": "build it"})
	if c, _, _ := readConversation(conversationPath(e.agent("1").ID, "second")); c.Other {
		t.Fatal("the user's conversation was taken for a subagent's")
	}
}

// With no briefing the first model call is the user's prompt, mail included.
func TestAntigravityFirstCallWithNoBriefing(t *testing.T) {
	e := setup(t)
	e.cfg.Delivery.OnSessionStart, e.cfg.Delivery.OnTool = false, false
	e.ag("SessionStart", map[string]any{"lastUserInput": "build it"})
	e.as("2", "claude-code", "SessionStart", nil)
	e.send("2", "1", "first")
	if got := e.invoke("build it"); !strings.Contains(got, "first") {
		t.Fatalf("injected %q", got)
	}
}

// Antigravity refuses the tool when a hook on it doesn't answer, so a hook
// with nothing to do still has to.
func TestAntigravityAlwaysGetsAnAnswer(t *testing.T) {
	e := setup(t)
	off := e.cfg
	off.Harnesses = map[string]config.HarnessCfg{"antigravity": {Enabled: false}}
	for event, want := range map[string]string{"PreToolUse": "allow", "Stop": "stop"} {
		var out bytes.Buffer
		Run(context.Background(), off, "antigravity", event, strings.NewReader("{}"), &out)
		if !strings.Contains(out.String(), want) {
			t.Errorf("%s of a harness switched off: %q", event, out.String())
		}
	}
	var out bytes.Buffer
	Run(context.Background(), off, "claude-code", "PreToolUse", strings.NewReader("{}"), &out)
	if strings.Contains(out.String(), "decision") {
		t.Errorf("others need none: %q", out.String())
	}
	// Nor does one that can't reach the hub.
	t.Setenv("AINTERLINKED_HOME", filepath.Join(t.TempDir(), "file"))
	if err := os.WriteFile(config.Home(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	Run(context.Background(), e.cfg, "antigravity", "PreToolUse", strings.NewReader("{}"), &out)
	if !strings.Contains(out.String(), "allow") {
		t.Errorf("with no hub: %q", out.String())
	}
}

// Other harnesses' hooks see every tool: a read isn't an edit for its name.
func TestSedFileIsAnEdit(t *testing.T) {
	for tool, want := range map[string]int{"sed_file": 1, "mcp__x__sed_file": 1, "read_parsed_file": 0, "list_used_files": 0, "get_compressed_file": 0} {
		if got := EditedFiles(Input{ToolName: tool, ToolInput: map[string]any{"file_path": "a.go"}}); len(got) != want {
			t.Errorf("%s: %v", tool, got)
		}
	}
}

// A prompt the bus caused is work too, though not the user's.
func TestAutoPromptIsWork(t *testing.T) {
	e := setup(t)
	e.as("1", "opencode", "SessionStart", nil)
	id := e.agent("1").ID
	_ = e.c.Call(context.Background(), "status", core.StatusReq{ID: id, State: "idle", Quiet: true}, nil)
	BumpHolds(id)
	e.as("1", "opencode", "UserPromptSubmit", map[string]any{"prompt": "go on", "ainterlinked_auto": true})
	if a := e.agent("1"); a.State != "working" || Holds(id) != 1 {
		t.Fatalf("state %q, holds %d", a.State, Holds(id))
	}
}
