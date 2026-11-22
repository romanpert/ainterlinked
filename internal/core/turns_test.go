package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The hub keeps who started a turn, and its record wins over the caller's
// claim: a write can't be passed off as the user's order once the hub knows
// peers held the turn open.
func TestTheHubsTurnRecordDecidesWhoseActItIs(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	holder := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1})
	writer := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2})
	held, err := s.Reserve(ctx, LockReq{Agent: holder.ID, Paths: []string{"cart.js"}})
	if err != nil {
		t.Fatal(err)
	}
	lock := held.Granted[0].ID

	if n, _ := s.MarkTurn(ctx, TurnReq{Agent: writer.ID}); n != 1 {
		t.Fatalf("held once: %d", n)
	}
	if n, _ := s.MarkTurn(ctx, TurnReq{Agent: writer.ID}); n != 2 {
		t.Fatalf("held twice: %d", n)
	}
	task, err := s.RecordTrespass(ctx, TrespassReq{Writer: writer.ID, LockID: lock, File: "cart.js", ByUser: true})
	if err != nil || task.Labels[0] != LabelRestitution {
		t.Fatalf("claimed the user's order on a peer-held turn: %+v %v", task.Labels, err)
	}
	// The user prompts: from now on it is the user's turn again.
	if n, _ := s.MarkTurn(ctx, TurnReq{Agent: writer.ID, User: true}); n != 0 {
		t.Fatalf("user turn: %d", n)
	}
	if n, _ := s.PeerHeld(ctx, writer.ID); n != 0 {
		t.Fatalf("after the prompt: %d", n)
	}
	task, err = s.RecordTrespass(ctx, TrespassReq{Writer: writer.ID, LockID: lock, File: "cart.js", ByUser: true})
	if err != nil || task.Labels[0] != LabelReconcile {
		t.Fatalf("the user's turn: %+v %v", task.Labels, err)
	}
	// The hub never promotes a claim: no ByUser, no user's order.
	task, _ = s.RecordTrespass(ctx, TrespassReq{Writer: writer.ID, LockID: lock, File: "cart.js"})
	if task.Labels[0] != LabelRestitution {
		t.Fatalf("promoted: %+v", task.Labels)
	}
}

// Claude Code writes who started each turn into its transcript, and the hub
// reads it there: an agent that runs the UserPromptSubmit hook from its own
// shell, after peers held its turn open, still can't pass its act off as the
// user's order. A prompt the user really types afterwards can.
func TestTheTranscriptNotTheHookSaysTheUserPrompted(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	const sid = "0c1d2e3f-aaaa-bbbb-cccc-111122223333"
	holder := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 1})
	writer := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 2, SessionID: sid})
	held, err := s.Reserve(ctx, LockReq{Agent: holder.ID, Paths: []string{"cart.js"}})
	if err != nil {
		t.Fatal(err)
	}
	lock := held.Granted[0].ID
	dir := filepath.Join(t.TempDir(), ".claude", "projects", "c--api")
	_ = os.MkdirAll(dir, 0o700)
	path := filepath.Join(dir, sid+".jsonl")
	prompt := func(at time.Time) {
		f, _ := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		fmt.Fprintf(f, `{"type":"user","message":{"role":"user","content":"fix cart"},"timestamp":%q,"origin":{"kind":"human"}}`+"\n", at.UTC().Format(time.RFC3339Nano))
		f.Close()
	}
	label := func(by bool, p string) string {
		t.Helper()
		task, err := s.RecordTrespass(ctx, TrespassReq{Writer: writer.ID, LockID: lock, File: "cart.js", ByUser: by, Transcript: p})
		if err != nil {
			t.Fatal(err)
		}
		return task.Labels[0]
	}

	prompt(time.Now().Add(-time.Minute))
	if l := label(true, path); l != LabelReconcile {
		t.Fatalf("the user's own turn: %s", l)
	}
	_, _ = s.MarkTurn(ctx, TurnReq{Agent: writer.ID})             // peers hold the turn open
	_, _ = s.MarkTurn(ctx, TurnReq{Agent: writer.ID, User: true}) // the agent runs the hook itself
	if l := label(true, path); l != LabelRestitution {
		t.Fatalf("a self-reported prompt passed for the user's: %s", l)
	}
	fake := filepath.Join(t.TempDir(), sid+".jsonl") // its own "transcript", in the repo
	data, _ := os.ReadFile(path)
	_ = os.WriteFile(fake, append(data, []byte(fmt.Sprintf(`{"type":"user","timestamp":%q,"origin":{"kind":"human"}}`+"\n", time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)))...), 0o600)
	if l := label(true, fake); l != LabelRestitution {
		t.Fatalf("a transcript outside the harness's tree was believed: %s", l)
	}
	time.Sleep(5 * time.Millisecond)
	prompt(time.Now()) // the user really types
	if l := label(true, path); l != LabelReconcile {
		t.Fatalf("the user's new prompt: %s", l)
	}
	if l := label(false, path); l != LabelRestitution {
		t.Fatalf("promoted without a claim: %s", l)
	}
}
