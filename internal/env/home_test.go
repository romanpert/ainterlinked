package env

import (
	"runtime"
	"testing"
)

// Muse Code runs hooks without USERPROFILE: the data directory must still be
// the user's, not one in whatever directory the hook started in.
func TestHomeSurvivesAClearedEnvironment(t *testing.T) {
	want, err := OSHome()
	if err != nil {
		t.Fatal(err)
	}
	name := "HOME"
	if runtime.GOOS == "windows" {
		name = "USERPROFILE"
	}
	t.Setenv(name, "")
	got, err := OSHome()
	if err != nil || got == "" || got == "." {
		t.Fatalf("no home without %s: %q %v", name, got, err)
	}
	if runtime.GOOS == "windows" && got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
