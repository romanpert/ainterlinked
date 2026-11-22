package env

import (
	"os"
	"testing"
)

func TestGetPrefersNewSpelling(t *testing.T) {
	t.Setenv("AINTERLINKED_X_TEST", "new")
	t.Setenv("INTERLINKED_X_TEST", "old")
	if got := Get("X_TEST"); got != "new" {
		t.Fatalf("Get = %q, want new", got)
	}
}

func TestGetFallsBackToLegacy(t *testing.T) {
	t.Setenv("INTERLINKED_X_TEST", "old")
	os.Unsetenv("AINTERLINKED_X_TEST")
	if got := Get("X_TEST"); got != "old" {
		t.Fatalf("Get = %q, want old", got)
	}
}

func TestEmptyNewHidesLegacy(t *testing.T) {
	t.Setenv("AINTERLINKED_X_TEST", "")
	t.Setenv("INTERLINKED_X_TEST", "old")
	if got := Get("X_TEST"); got != "" {
		t.Fatalf("Get = %q, want empty", got)
	}
}
