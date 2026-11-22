package config

import (
	"os"
	"strings"
	"testing"
)

// The limits start at known values; the user changes or turns off any one,
// and what they wrote survives a save. A value that means nothing is refused.
func TestLimitsAreTheUsers(t *testing.T) {
	t.Setenv("AINTERLINKED_HOME", t.TempDir())
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(Path(), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("machine = \"box\"\n") // a config from before [limits]
	if got, err := Load(); err != nil || got.Limits != Default().Limits || got.Limits.MaxHolds != 6 {
		t.Fatalf("%+v %v", got.Limits, err)
	}
	write("[limits]\nmax_holds = 0\nqueue_minutes = 5\n")
	want := Default().Limits
	want.MaxHolds, want.QueueMinutes = 0, 5
	got, err := Load()
	if err != nil || got.Limits != want {
		t.Fatalf("%+v %v", got.Limits, err)
	}
	if err := Save(got); err != nil {
		t.Fatal(err)
	}
	if got, err = Load(); err != nil || got.Limits != want {
		t.Fatalf("after a save: %+v %v", got.Limits, err)
	}
	write("[limits]\nbrief_hours = -2\n")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "limits.brief_hours") {
		t.Fatalf("refused by name: %v", err)
	}
}
