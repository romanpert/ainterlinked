package hooks

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/romanpert/ainterlinked/internal/core"
)

func TestShellWrites(t *testing.T) {
	cases := map[string][]string{
		`sed -i 's/a/b/' cart.js`:                   {"cart.js"},
		`sed -i.bak -e 's/a/b/' cart.js util.js`:    {"cart.js", "util.js"},
		`sed 's/a/b/' cart.js`:                      nil,
		`echo x > out.txt 2>&1`:                     {"out.txt"},
		`echo x >> "log file.txt"`:                  {"log file.txt"},
		`cat a > /dev/null`:                         nil,
		`echo hi >&2`:                               nil,
		`mv a.js b.js`:                              {"a.js", "b.js"},
		`cp -r src dst`:                             {"dst"},
		`rm -f build.js tmp.js`:                     {"build.js", "tmp.js"},
		`npm test | tee -a test.log`:                {"test.log"},
		`Set-Content -Path cart.js -Value 1`:        {"cart.js"},
		`"x" | Out-File notes.md`:                   {"notes.md"},
		`Copy-Item a.txt -Destination b.txt`:        {"b.txt"},
		`git checkout -- cart.js`:                   {"cart.js"},
		`git restore cart.js`:                       {"cart.js"},
		`git checkout main`:                         nil,
		`ls -la && go test ./...`:                   nil,
		`echo $X > $F`:                              nil,
		`python -c "open('f','w').write('x')"`:      nil,
		`dd if=/dev/zero of=disk.img bs=1M count=1`: {"disk.img"},
	}
	for cmd, want := range cases {
		if got := shellWrites(cmd); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %q, want %q", cmd, got, want)
		}
	}
}

func bash(cmd string) map[string]any {
	return map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}}
}

func TestShellWriteToAReservedFileIsReported(t *testing.T) {
	e := setup(t)
	e.as("1", "claude-code", "SessionStart", nil)
	holder := e.agent("1")
	var res core.LockResult
	if err := e.c.Call(context.Background(), "reserve", core.LockReq{Agent: holder.ID, Paths: []string{"cart.js"}, Reason: "task #1"}, &res); err != nil {
		t.Fatal(err)
	}
	e.as("2", "codex", "SessionStart", nil)
	writer := e.agent("2")
	e.as("2", "codex", "UserPromptSubmit", map[string]any{"prompt": "fix it"})

	// Reading it is fine; so is writing an unreserved file.
	if _, out := e.as("2", "codex", "PostToolUse", bash("cat cart.js > /dev/null && echo x > notes.txt")); out != nil {
		t.Fatalf("no trespass: %v", out)
	}
	_, out := e.as("2", "codex", "PostToolUse", bash("sed -i 's/0.1/0.10/' cart.js"))
	ctx, _ := out["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	if !strings.Contains(ctx, "cart.js (reserved by "+holder.Name) || !strings.Contains(ctx, "say so to them first") {
		t.Fatalf("writer not told: %v", out)
	}
	// The holder is told, by the writer's own name, without the command's arguments.
	_, out = e.as("1", "claude-code", "PostToolUse", bash("ls"))
	got, _ := out["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	if !strings.Contains(got, "my shell command (sed) wrote to cart.js") || !strings.Contains(got, "a turn the user started") || strings.Contains(got, "0.10") {
		t.Fatalf("holder: %s", got)
	}
	var tasks []core.Task
	_ = e.c.Call(context.Background(), "tasks", core.TaskFilter{Agent: writer.ID, Open: true}, &tasks)
	if len(tasks) != 1 || tasks[0].Assignee != writer.Name || tasks[0].Labels[0] != "reconcile" {
		t.Fatalf("task: %+v", tasks)
	}
	// Once per reservation, not per command.
	if _, out = e.as("2", "codex", "PostToolUse", bash("sed -i 's/a/b/' cart.js")); out != nil {
		t.Fatalf("reported twice: %v", out)
	}
}

func TestShellWriteOnAPeerDrivenTurnIsRestitution(t *testing.T) {
	e := setup(t)
	e.as("1", "claude-code", "SessionStart", nil)
	holder := e.agent("1")
	var res core.LockResult
	if err := e.c.Call(context.Background(), "reserve", core.LockReq{Agent: holder.ID, Paths: []string{"src/"}}, &res); err != nil {
		t.Fatal(err)
	}
	e.as("2", "claude-code", "SessionStart", nil)
	writer := e.agent("2")
	BumpHolds(writer.ID) // the turn was kept going by peer mail, not by the user
	_, out := e.as("2", "claude-code", "PostToolUse", bash("echo '//x' >> src/a.js"))
	ctx, _ := out["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	if !strings.Contains(ctx, "src/a.js") || strings.Contains(ctx, "say so to them first") {
		t.Fatalf("writer: %s", ctx)
	}
	var tasks []core.Task
	_ = e.c.Call(context.Background(), "tasks", core.TaskFilter{Agent: writer.ID, Open: true}, &tasks)
	if len(tasks) != 1 || tasks[0].Labels[0] != "restitution" || !strings.Contains(tasks[0].Body, "kept going by peer messages") {
		t.Fatalf("task: %+v", tasks)
	}
}
