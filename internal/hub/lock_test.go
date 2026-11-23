package hub

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestLockWaitsForItsHolder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.lock")
	a, err := Lock(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(p, 20*time.Millisecond); !errors.Is(err, errLocked) {
		t.Fatalf("held, and got %v", err)
	}
	time.AfterFunc(30*time.Millisecond, func() { a.Close() })
	b, err := Lock(p, 5*time.Second)
	if err != nil {
		t.Fatalf("freed, and got %v", err)
	}
	b.Close()
	if _, err := Lock(filepath.Join(p, "nowhere", "x"), time.Second); err == nil || errors.Is(err, errLocked) {
		t.Fatalf("no such folder, and got %v", err)
	}
}
