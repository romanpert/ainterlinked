package hub

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/romanpert/ainterlinked/internal/config"
	"github.com/romanpert/ainterlinked/internal/core"
)

// The hub takes [tasks] stale_after_days up like [limits], and looks for
// forgotten tasks at most once an hour.
func TestTheHubFollowsStaleAfterDays(t *testing.T) {
	t.Setenv("AINTERLINKED_HOME", t.TempDir())
	srv, _ := startServer(t, func(int) (int, string) { return 4242, "claude" })
	if srv.Store.StaleAfterDays() != 0 {
		t.Fatal("off by default")
	}
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(config.Path(), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("[tasks]\nstale_after_days = 5\n")
	stamp := srv.followLimits("")
	if srv.Store.StaleAfterDays() != 5 {
		t.Fatalf("taken up: %d", srv.Store.StaleAfterDays())
	}
	write("[tasks]\nstale_after_days = -5 # a typo\n")
	if srv.followLimits(stamp); srv.Store.StaleAfterDays() != 5 {
		t.Fatal("a bad file changes nothing")
	}

	recent := time.Now().Add(-time.Minute)
	if got := srv.noticeStale(context.Background(), recent); !got.Equal(recent) {
		t.Fatal("looked again within the hour")
	}
	if got := srv.noticeStale(context.Background(), time.Now().Add(-staleEvery)); time.Since(got) > time.Minute {
		t.Fatal("an hour later it looks")
	}
	if got := srv.noticeStale(context.Background(), time.Time{}); got.IsZero() {
		t.Fatal("the first tick looks")
	}
}

// storeOptions carries the setting to a store opened without the hub process.
func TestStoreOptionsCarryStaleAfterDays(t *testing.T) {
	cfg := config.Default()
	cfg.Tasks.StaleAfterDays = 3
	if o := storeOptions(cfg, 0); o.StaleAfterDays != 3 {
		t.Fatalf("%+v", o)
	}
	_ = core.DefaultStaleDays
}
