package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/romanpert/ainterlinked/internal/transcript"
)

// A session that left hands its work over by reference: the next session
// in the same checkout gets a line (who, how big, where the work stood) and
// reads the body only if it asks; another checkout sees nothing; whoever
// continues the work takes the handover with it.
func TestASessionThatLeftHandsItsWorkOverByReference(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	start := time.Now()
	s.now = func() time.Time { return start }
	old := reg(t, s, RegisterReq{Harness: "claude-code", Project: "shop", Branch: "cart", AnchorPID: 1, SessionID: "s-old", Name: "cart-work"})
	still := reg(t, s, RegisterReq{Harness: "claude-code", Project: "shop", AnchorPID: 2, SessionID: "s-still"})
	var reads []string
	read := func(harness, session string) (transcript.Handover, bool) {
		reads = append(reads, session)
		if session == "s-old" {
			return transcript.Handover{Body: "## Where the work stood\nCheckout button wired; tax rounding fails.", Gist: "Checkout button wired; tax rounding fails."}, true
		}
		return transcript.Handover{}, false
	}
	// Online sessions aren't handed over.
	if n, _ := s.WriteHandovers(ctx, 5, read); n != 0 || len(reads) != 0 {
		t.Fatalf("handed over a live session: %d %v", n, reads)
	}
	_ = s.Leave(ctx, old.ID)
	if n, err := s.WriteHandovers(ctx, 5, read); err != nil || n != 1 {
		t.Fatalf("wrote %d: %v", n, err)
	}
	// Written once: the next tick reads nothing again.
	reads = nil
	if n, _ := s.WriteHandovers(ctx, 5, read); n != 0 || len(reads) != 0 {
		t.Fatalf("read again: %v", reads)
	}

	next := reg(t, s, RegisterReq{Harness: "codex", Project: "shop", Branch: "cart", AnchorPID: 3})
	list, err := s.Handovers(ctx, HandoverReq{Agent: next.ID})
	if err != nil || len(list) != 1 || list[0].Name != "cart-work" || list[0].Body != "" || list[0].Gist != "Checkout button wired; tax rounding fails." || list[0].Chars == 0 {
		t.Fatalf("offer: %+v %v", list, err)
	}
	full, err := s.GetHandover(ctx, HandoverReq{Agent: next.ID, ID: list[0].ID})
	if err != nil || !strings.Contains(full.Body, "tax rounding fails") {
		t.Fatalf("read: %+v %v", full, err)
	}
	elsewhere := reg(t, s, RegisterReq{Harness: "cursor", Project: "shop", ProjectPath: "/other/shop", AnchorPID: 4})
	if got, _ := s.Handovers(ctx, HandoverReq{Agent: elsewhere.ID}); len(got) != 0 {
		t.Fatalf("offered to another checkout: %+v", got)
	}
	if _, err := s.GetHandover(ctx, HandoverReq{Agent: elsewhere.ID, ID: list[0].ID}); err == nil {
		t.Fatal("read from another checkout")
	}

	// Whoever continues the work takes it: no longer on offer to others.
	s.now = func() time.Time { return start.Add(handoverQuiet + time.Minute) }
	if _, err := s.SetStatus(ctx, StatusReq{ID: still.ID, State: "working"}); err != nil {
		t.Fatal(err)
	}
	heir := reg(t, s, RegisterReq{Harness: "claude-code", Project: "shop", AnchorPID: 5, SessionID: "s-heir"})
	if _, err := s.ContinueAs(ctx, heir.ID, "cart-work"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Handovers(ctx, HandoverReq{Agent: next.ID}); len(got) != 0 {
		t.Fatalf("still on offer after it was taken: %+v", got)
	}
	took, _ := s.Handovers(ctx, HandoverReq{Agent: heir.ID, Taken: true})
	if len(took) != 1 || took[0].ID != list[0].ID {
		t.Fatalf("the successor's: %+v", took)
	}
	if _, err := s.GetHandover(ctx, HandoverReq{Agent: heir.ID, ID: list[0].ID}); err != nil {
		t.Fatal(err)
	}
}

// A session's own note is never replaced by the one written from its
// transcript; a later note of its own replaces it.
func TestASessionsOwnNoteWinsOverTheAutomaticOne(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	me := reg(t, s, RegisterReq{Harness: "claude-code", Project: "shop", AnchorPID: 1, SessionID: "s1", Name: "notes"})
	if _, err := s.LeaveHandover(ctx, HandoverNoteReq{Agent: me.ID, Note: "Payments done. Next: refunds, see docs/refunds.md"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LeaveHandover(ctx, HandoverNoteReq{Agent: me.ID, Note: strings.Repeat("x", maxNote+1)}); err == nil {
		t.Fatal("an oversized note was taken")
	}
	_ = s.Leave(ctx, me.ID)
	_, _ = s.WriteHandovers(ctx, 5, func(string, string) (transcript.Handover, bool) {
		return transcript.Handover{Body: "from the transcript", Gist: "auto"}, true
	})
	next := reg(t, s, RegisterReq{Harness: "codex", Project: "shop", AnchorPID: 2})
	list, _ := s.Handovers(ctx, HandoverReq{Agent: next.ID})
	if len(list) != 1 || list[0].Source != "agent" || !strings.HasPrefix(list[0].Gist, "Payments done") {
		t.Fatalf("%+v", list)
	}
}

// Pushed mail longer than the reader's inline length arrives as a gist with
// its full size; asked-for mail comes whole.
func TestLongPushedMailArrivesAsAGist(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a := reg(t, s, RegisterReq{Harness: "codex", Project: "shop", AnchorPID: 1})
	b := reg(t, s, RegisterReq{Harness: "claude-code", Project: "shop", AnchorPID: 2})
	long := "Findings on the cart:\n" + strings.Repeat("the tax rounding is off by a cent in some carts; ", 40)
	_, _ = s.Send(ctx, SendReq{From: a.ID, To: b.Name, Body: "short one"})
	m, _ := s.Send(ctx, SendReq{From: a.ID, To: b.Name, Body: long})
	got, err := s.Inbox(ctx, InboxReq{Agent: b.ID, Mode: ModeFull, Inline: 600})
	if err != nil || len(got) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	if got[0].Body != "short one" || got[0].Shortened != 0 {
		t.Fatalf("a short message was cut: %+v", got[0])
	}
	if got[1].Shortened != len(long) || len(got[1].Body) > 310 || !strings.HasSuffix(got[1].Body, " …") || !strings.HasPrefix(got[1].Body, "Findings on the cart:") {
		t.Fatalf("gist: %d chars, %q", len(got[1].Body), got[1].Body)
	}
	thread, _ := s.Thread(ctx, b.ID, m.ID)
	if len(thread) != 1 || thread[0].Body != long {
		t.Fatal("get_thread didn't give it whole")
	}
	if g := Gist("ñññ", 3); g != "ñ …" {
		t.Fatalf("cut inside a character: %q", g)
	}
}
