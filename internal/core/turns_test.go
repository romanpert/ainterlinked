package core

import (
	"context"
	"testing"
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
