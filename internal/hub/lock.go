package hub

import (
	"errors"
	"io"
	"time"
)

// Lock takes an exclusive OS lock on path, waiting for up to wait while
// another process (or another Lock of this one) holds it. Close frees it.
func Lock(path string, wait time.Duration) (io.Closer, error) {
	end := time.Now().Add(wait)
	for {
		f, err := lockFile(path)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, errLocked) || time.Now().After(end) {
			return nil, err
		}
		time.Sleep(5 * time.Millisecond)
	}
}
