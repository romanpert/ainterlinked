package hub

import (
	"context"
	"os"
	"testing"

	"github.com/romanpert/ainterlinked/internal/config"
	"github.com/romanpert/ainterlinked/internal/core"
)

// The hub process is long-lived: it takes up the user's [limits] when the
// file changes, and keeps the ones it has while the file can't be read.
func TestTheHubFollowsTheUsersLimits(t *testing.T) {
	t.Setenv("AINTERLINKED_HOME", t.TempDir())
	srv, _ := startServer(t, func(int) (int, string) { return 4242, "claude" })
	ctx := context.Background()
	a, err := srv.Store.Register(ctx, core.RegisterReq{Harness: "claude-code", Project: "shop", ProjectPath: "/w/shop", Machine: "box", AnchorPID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Store.Register(ctx, core.RegisterReq{Harness: "codex", Project: "shop", ProjectPath: "/w/shop", Machine: "box", AnchorPID: 2}); err != nil {
		t.Fatal(err)
	}
	refused := func() bool {
		for i := 0; i < 2*core.BroadcastBurst; i++ {
			if _, err := srv.Store.Send(ctx, core.SendReq{From: a.ID, To: "project", Body: "news"}); err != nil {
				return true
			}
		}
		return false
	}
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(config.Path(), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !refused() {
		t.Fatal("the starting limit applies")
	}
	write("[limits]\nbroadcast_burst = 0\n")
	stamp := srv.followLimits("")
	if refused() {
		t.Fatal("the user turned it off")
	}
	write("[limits]\nbroadcast_burst = -1 # a typo\n")
	stamp = srv.followLimits(stamp)
	if refused() {
		t.Fatal("a bad file changes nothing")
	}
	write("[limits]\nbroadcast_burst = 1\nbroadcast_refill_minutes = 10\n")
	srv.followLimits(stamp)
	if !refused() {
		t.Fatal("the user turned it back on")
	}
}
