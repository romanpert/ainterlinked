package core

import (
	"context"
	"testing"
	"time"
)

// Two agents that write to each other all day can't wake each other without
// end, not even for a user who set no max_holds: it is their money.
func TestWakesHaveAPace(t *testing.T) {
	x := withLimits(t, Limits{}, func(*Limits) {})
	ctx := context.Background()
	clock := x.start
	x.now = func() time.Time { return clock }
	wake := func() bool {
		ok, err := x.Wake(ctx, x.a.ID)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	for i := 0; i < WakesHourly; i++ {
		if !wake() {
			t.Fatalf("wake %d refused", i)
		}
		if clock = clock.Add(WakeSpacing - time.Second); wake() {
			t.Fatalf("wake %d: not spaced out", i)
		}
		clock = clock.Add(time.Second)
	}
	if clock = clock.Add(time.Minute); wake() {
		t.Fatal("over the hour's share")
	}
	if clock = clock.Add(time.Hour); !wake() {
		t.Fatal("the hour is over")
	}
	if n, _ := x.PeerHeld(ctx, x.a.ID); n != WakesHourly+1 {
		t.Fatalf("each one is peers' turn: %d", n)
	}
}

func TestWakesShareMaxHolds(t *testing.T) {
	x := withLimits(t, Limits{}, func(l *Limits) { l.MaxHolds = 2 })
	ctx := context.Background()
	clock := x.start
	x.now = func() time.Time { clock = clock.Add(time.Minute); return clock }
	if _, err := x.MarkTurn(ctx, TurnReq{Agent: x.a.ID}); err != nil { // a stop-hold
		t.Fatal(err)
	}
	if ok, err := x.Wake(ctx, x.a.ID); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := x.Wake(ctx, x.a.ID); ok || err != nil {
		t.Fatal("a third continuation", err)
	}
	if _, err := x.MarkTurn(ctx, TurnReq{Agent: x.a.ID, User: true}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := x.Wake(ctx, x.a.ID); !ok {
		t.Fatal("the user's prompt starts over")
	}
}

// The user prompts while a wake-up is on its way: the turn it starts is
// still peers', and counted once when nobody prompted.
func TestAWokenTurnIsPeersHoweverItRaces(t *testing.T) {
	x := withLimits(t, Limits{}, func(*Limits) {})
	ctx := context.Background()
	woken := func() int {
		n, err := x.MarkTurn(ctx, TurnReq{Agent: x.b.ID, Woken: true})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if ok, err := x.Wake(ctx, x.b.ID); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if n := woken(); n != 1 {
		t.Fatalf("counted twice: %d", n)
	}
	if _, err := x.MarkTurn(ctx, TurnReq{Agent: x.b.ID, User: true}); err != nil {
		t.Fatal(err)
	}
	if n := woken(); n != 1 {
		t.Fatalf("the user's prompt took the woken turn: %d", n)
	}
}
