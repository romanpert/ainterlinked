package mcp

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/hub"
)

// Models copy ids as they see them ("#12", "12"): those still thread.
func TestIdsWrittenAsTextStillCount(t *testing.T) {
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
	claude.tool("whoami", nil)
	codex := start(t, h, "codex", "22", project)
	codex.tool("whoami", nil)

	sent, _ := claude.tool("send_message", map[string]any{"to": "codex@shop", "body": "PING"})
	id := regexp.MustCompile(`#(\d+)`).FindStringSubmatch(sent)
	if id == nil {
		t.Fatalf("no id in %q", sent)
	}
	inbox, _ := codex.tool("read_inbox", nil)
	if !strings.Contains(inbox, "<<peer-message #"+id[1]+" from claude@shop") {
		t.Fatalf("the id must lead the header: %s", inbox)
	}
	if _, isErr := codex.tool("send_message", map[string]any{"to": "claude@shop", "body": "PONG", "reply_to": "#" + id[1]}); isErr {
		t.Fatal("reply failed")
	}
	thread, _ := codex.tool("get_thread", map[string]any{"message_id": id[1]})
	if !strings.Contains(thread, "PING") || !strings.Contains(thread, "PONG") {
		t.Fatalf("\"#%s\" as reply_to must keep the thread: %s", id[1], thread)
	}
}
