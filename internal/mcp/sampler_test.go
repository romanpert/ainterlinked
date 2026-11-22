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

// Two sessions are open; exactly one samples the machine, and list_agents
// shows the load once a sample exists.
func TestOneSessionSamplesTheMachine(t *testing.T) {
	sampleOverride.Store(int64(50 * time.Millisecond))
	defer sampleOverride.Store(0)
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
	var m core.Machine
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if m, _ = st.Machine(ctx); m.Fresh && (m.CPU >= 0 || m.Approx) {
			break
		}
	}
	if !m.Fresh || m.MemTotal == 0 || m.DiskTotal == 0 || m.Cores < 1 {
		t.Skipf("no reading on this OS: %+v", m)
	}
	if m.CPU < 0 {
		t.Fatalf("the sampler measures CPU from its second read: %+v", m)
	}
	first := m.Sampler
	time.Sleep(10 * sampleEvery())
	if m, _ = st.Machine(ctx); m.Sampler != first {
		t.Fatalf("the lease holder keeps sampling: was %s, now %s", first, m.Sampler)
	}
	list, _ := codex.tool("list_agents", nil)
	if !strings.Contains(list, "This machine: CPU ") || !strings.Contains(list, "GB free") {
		t.Fatalf("list_agents shows the load: %s", list)
	}
}
