package mcp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/hub"
)

// No session reads the machine's load unless the user asks one to; that one
// publishes on the interval given, until told to stop.
func TestASessionPublishesTheLoadOnlyWhenAsked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AINTERLINKED_HOME", home)
	st, err := core.Open(filepath.Join(home, "hub.db"), core.Options{Stale: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := hub.NewLocal(st)
	project := filepath.Join(t.TempDir(), "shop")
	claude := start(t, h, "claude-code", "11", project)
	codex := start(t, h, "codex", "22", project)
	_, _ = claude.tool("whoami", nil)
	_, _ = codex.tool("whoami", nil)
	ctx := context.Background()
	if m, _ := st.Machine(ctx); m.SampledAt != 0 {
		t.Fatalf("nobody asked: %+v", m)
	}
	if list, _ := codex.tool("list_agents", nil); strings.Contains(list, "This machine") {
		t.Fatalf("nobody asked: %s", list)
	}

	if out, isErr := claude.tool("set_status", map[string]any{"share_load_seconds": -1}); !isErr {
		t.Fatalf("a negative interval is refused: %s", out)
	}
	out, isErr := claude.tool("set_status", map[string]any{"share_load_seconds": 1})
	if isErr && strings.Contains(out, "not read on this OS") {
		t.Skip(out)
	}
	if isErr || !strings.Contains(out, "every 1s") {
		t.Fatalf("publish: %s", out)
	}
	first, _ := st.Machine(ctx)
	if !first.Fresh || first.By != "claude@shop" || first.MemTotal == 0 || first.DiskTotal == 0 || first.Cores < 1 || first.CPU < 0 {
		t.Fatalf("published at once, CPU measured: %+v", first)
	}
	if out, isErr := codex.tool("set_status", map[string]any{"share_load_seconds": 1}); !isErr || !strings.Contains(out, "claude@shop is already publishing") {
		t.Fatalf("one publishes at a time: %s", out)
	}
	for _, tool := range []string{"list_agents", "activity_feed"} {
		if got, _ := codex.tool(tool, nil); !strings.Contains(got, "This machine (by claude@shop") || !strings.Contains(got, "GB free") {
			t.Fatalf("%s shows it to whoever asks: %s", tool, got)
		}
	}
	// A status change without the argument leaves the publication alone.
	_, _ = claude.tool("set_status", map[string]any{"activity": "testing"})
	var m core.Machine
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if m, _ = st.Machine(ctx); m.SampledAt > first.SampledAt {
			break
		}
	}
	if m.SampledAt <= first.SampledAt || m.Sampler != first.Sampler {
		t.Fatalf("it keeps publishing: %+v", m)
	}

	if out, isErr := claude.tool("set_status", map[string]any{"share_load_seconds": 0}); isErr || !strings.Contains(out, "no longer") {
		t.Fatalf("stop: %s", out)
	}
	stopped, _ := st.Machine(ctx)
	time.Sleep(1300 * time.Millisecond)
	if m, _ = st.Machine(ctx); m.Fresh || m.SampledAt != stopped.SampledAt {
		t.Fatalf("stopped: %+v", m)
	}
	if list, _ := codex.tool("list_agents", nil); strings.Contains(list, "This machine") {
		t.Fatalf("stopped: %s", list)
	}
}
