package core

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/romanpert/ainterlinked/internal/transcript"
)

// What a session spent on a turn held open by peer mail is charged to the
// senders of that mail, split evenly, and shows in their facts; only mail
// the hub delivered to the spender counts, so nobody is charged for a
// message it never sent that agent. The owner's cap counts billed tokens.
func TestSpendOnPeersRequestsIsChargedToTheSenders(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	worker := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1, SessionID: "w1"})
	asker := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2})
	other := reg(t, s, RegisterReq{Harness: "gemini", Project: "api", AnchorPID: 3})
	bystander := reg(t, s, RegisterReq{Harness: "codex", Project: "web", AnchorPID: 4})
	send := func(from Agent, to Agent) int64 {
		m, err := s.Send(ctx, SendReq{From: from.ID, To: to.Name, Body: "please look at the cart"})
		if err != nil {
			t.Fatal(err)
		}
		return m.ID
	}
	m1, m2 := send(asker, worker), send(other, worker)
	notToWorker := send(asker, bystander)

	ledger := transcript.Ledger{
		transcript.CauseOwn:             {In: 100, CacheWrite: 1000, CacheRead: 50000, Out: 900},
		fmt.Sprintf("#%d,#%d", m1, m2):  {In: 11, CacheWrite: 400, CacheRead: 9000, Out: 201},
		fmt.Sprintf("#%d", notToWorker): {In: 5, Out: 5},
	}
	if err := s.RecordSpend(ctx, worker.ID, ledger); err != nil {
		t.Fatal(err)
	}
	sp, err := s.GetSpend(ctx, SpendReq{Agent: worker.ID})
	if err != nil || !sp.Known {
		t.Fatalf("spend: %+v %v", sp, err)
	}
	if sp.Own.Billed() != 2000 || sp.ForPeers.Billed() != 622 || sp.Billed() != 2622 {
		t.Fatalf("own %d, for peers %d", sp.Own.Billed(), sp.ForPeers.Billed())
	}
	facts, err := s.AgentFacts(ctx, "", []string{worker.Name, asker.Name, other.Name, bystander.Name})
	if err != nil {
		t.Fatal(err)
	}
	w, a, o := facts[strings.ToLower(worker.Name)], facts[strings.ToLower(asker.Name)], facts[strings.ToLower(other.Name)]
	if w.Tokens != 2622 || w.TokensForPeers != 622 {
		t.Fatalf("worker: %+v", w)
	}
	// 612 billed for #m1,#m2 split between the two senders; the message
	// that never went to the worker charges nobody.
	if a.TokensCaused+o.TokensCaused != 612 || a.TokensCaused < 305 || o.TokensCaused < 305 {
		t.Fatalf("asker %+v, other %+v", a, o)
	}
	if facts[strings.ToLower(bystander.Name)].TokensCaused != 0 {
		t.Fatal("a bystander was charged")
	}

	// Recording again replaces, it doesn't add (a restarted hub reads the
	// transcript from the start).
	_ = s.RecordSpend(ctx, worker.ID, ledger)
	if again, _ := s.GetSpend(ctx, SpendReq{Agent: worker.ID}); again.Billed() != 2622 {
		t.Fatalf("recorded twice: %d", again.Billed())
	}

	// The owner's cap: the owner's to set, not an agent's.
	if _, err := s.SetBudget(ctx, BudgetReq{By: worker.ID, SessionTokens: 1 << 40}); err == nil {
		t.Fatal("an agent set the owner's budget")
	}
	owner := reg(t, s, RegisterReq{Harness: "human", Name: "human", Project: "api", SessionID: "human@box"})
	if _, err := s.SetBudget(ctx, BudgetReq{By: owner.ID, SessionTokens: 2622}); err != nil {
		t.Fatal(err)
	}
	if sp, _ = s.GetSpend(ctx, SpendReq{Agent: worker.ID}); !sp.Over || sp.Cap != 2622 {
		t.Fatalf("at the cap: %+v", sp)
	}
	_, _ = s.SetBudget(ctx, BudgetReq{By: owner.ID, SessionTokens: 0})
	if sp, _ = s.GetSpend(ctx, SpendReq{Agent: worker.ID}); sp.Over || sp.Cap != 0 {
		t.Fatalf("cap removed: %+v", sp)
	}
	if _, err := s.SetBudget(ctx, BudgetReq{By: owner.ID, SessionTokens: -1}); err == nil {
		t.Fatal("a negative cap")
	}
}

func TestSplitAddsUp(t *testing.T) {
	tok := transcript.Tokens{In: 10, CacheWrite: 7, CacheRead: 1, Out: 5}
	var sum transcript.Tokens
	for _, p := range split(tok, 3) {
		sum = sum.Add(p)
	}
	if sum != tok {
		t.Fatalf("%+v != %+v", sum, tok)
	}
	if FormatTokens(950) != "950" || FormatTokens(12_400) != "12k" || FormatTokens(3_400_000) != "3.4M" || FormatTokens(1500) != "1.5k" {
		t.Fatal(FormatTokens(950), FormatTokens(12_400), FormatTokens(3_400_000), FormatTokens(1500))
	}
}

// A transcript only grows: a read that comes out smaller (a swapped file,
// a forged smaller total) doesn't lower what was recorded, so it can't
// bring a session back under the owner's cap.
func TestASmallerReadDoesntLowerTheRecord(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 1, SessionID: "c1"})
	big := transcript.Ledger{transcript.CauseOwn: {In: 900_000, Out: 100_000}}
	_ = s.RecordSpend(ctx, a.ID, big)
	_ = s.RecordSpend(ctx, a.ID, transcript.Ledger{transcript.CauseOwn: {In: 10}})
	if sp, _ := s.GetSpend(ctx, SpendReq{Agent: a.ID}); sp.Billed() != 1_000_000 {
		t.Fatalf("lowered to %d", sp.Billed())
	}
	_ = s.RecordSpend(ctx, a.ID, transcript.Ledger{transcript.CauseOwn: {In: 900_000, Out: 100_500}})
	if sp, _ := s.GetSpend(ctx, SpendReq{Agent: a.ID}); sp.Billed() != 1_000_500 {
		t.Fatalf("a larger read wasn't recorded: %d", sp.Billed())
	}
}

// Trading billed tokens for cache reads (a forged read with more cache
// reads and less output) doesn't get past "never lower": every figure is
// compared on its own.
func TestAReadCantTradeBilledTokensForCacheReads(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 1, SessionID: "c1"})
	_ = s.RecordSpend(ctx, a.ID, transcript.Ledger{transcript.CauseOwn: {In: 900_000, Out: 100_000}})
	_ = s.RecordSpend(ctx, a.ID, transcript.Ledger{transcript.CauseOwn: {In: 10, CacheRead: 50_000_000}})
	if sp, _ := s.GetSpend(ctx, SpendReq{Agent: a.ID}); sp.Billed() != 1_000_000 || sp.Own.CacheRead != 0 {
		t.Fatalf("traded: %+v", sp.Own)
	}
}
