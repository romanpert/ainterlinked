package config

import (
	"os"
	"strings"
	"testing"
)

// Notices about forgotten tasks are off until the user sets a number of days.
func TestStaleAfterDaysIsOffUntilSet(t *testing.T) {
	t.Setenv("AINTERLINKED_HOME", t.TempDir())
	if got, err := Load(); err != nil || got.Tasks.StaleAfterDays != 0 {
		t.Fatalf("off by default: %+v %v", got.Tasks, err)
	}
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(Path(), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("[tasks]\nstale_after_days = 14\n")
	got, err := Load()
	if err != nil || got.Tasks.StaleAfterDays != 14 {
		t.Fatalf("%+v %v", got.Tasks, err)
	}
	if err := Save(got); err != nil {
		t.Fatal(err)
	}
	if got, err = Load(); err != nil || got.Tasks.StaleAfterDays != 14 {
		t.Fatalf("after a save: %+v %v", got.Tasks, err)
	}
	write("[tasks]\nstale_after_days = -1\n")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "tasks.stale_after_days") {
		t.Fatalf("refused by name: %v", err)
	}
}
