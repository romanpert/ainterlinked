package hooks

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/romanpert/ainterlinked/internal/config"
	"github.com/romanpert/ainterlinked/internal/hub"
)

// TestHookWorkIsCheap times the in-process part of a hook (open hub +
// handle) to separate it from process-spawn cost.
func TestHookWorkIsCheap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("INTERLINKED_HOME", home)
	t.Setenv("INTERLINKED_ANCHOR_PID", "77")
	cfg := config.Default()
	raw, _ := json.Marshal(map[string]any{"session_id": "s", "cwd": t.TempDir(), "tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})
	var times []time.Duration
	for i := 0; i < 15; i++ {
		t0 := time.Now()
		c, err := hub.Open(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Handle(context.Background(), cfg, c, "claude-code", "PostToolUse", ParseInput(raw)); err != nil {
			t.Fatal(err)
		}
		c.Close()
		times = append(times, time.Since(t0))
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	t.Logf("open+handle median %s (min %s, max %s); db %s", times[7], times[0], times[14], filepath.Join(home, "hub.db"))
	if times[7] > 150*time.Millisecond {
		t.Errorf("hook work too slow: %s", times[7])
	}
}
