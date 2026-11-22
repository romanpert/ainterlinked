// Package transcript reads, from a harness's own record of a session, who
// started the latest turn: the user or a peer. Hooks can report a turn, but
// an agent can run a hook command from its own shell; the transcript is
// written by the harness, and forging it means tampering with the harness's
// files, not running a command.
package transcript

import (
	"bytes"
	"github.com/romanpert/ainterlinked/internal/env"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Origin is what the transcript says about the latest turn's start.
type Origin struct {
	User int64 // unix ms of the latest prompt the user typed (0: none found)
	Peer int64 // unix ms of a later turn started by a peer (0: none)
}

// Budget is how much of a transcript is read, from its end, looking for
// the latest turn's start. Past it, no user prompt is found.
const Budget = 64 << 20

// Read returns the latest turn's origin in the session's transcript. known
// is false when ainterlinked can't read this harness's transcripts. The
// file is looked up under the harness's own directory; hint (the path a
// hook reported) only saves the lookup when it is that very file. With no
// such file, the answer is known, with no user prompt.
func Read(harness, hint, session string) (o Origin, known bool) {
	r, ok := readers[harness]
	if !ok {
		return Origin{}, false
	}
	path := r.find(hint, session)
	if path == "" {
		return Origin{}, true
	}
	f, err := os.Open(path)
	if err != nil {
		return Origin{}, true
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Origin{}, true
	}
	_ = scanBack(f, fi.Size(), Budget, func(line []byte) bool {
		at, kind := r.classify(line)
		switch kind {
		case user:
			o.User = at
			return false
		case peer:
			o.Peer = at
			return false
		}
		return true
	})
	return o, true
}

type kind int

const (
	other kind = iota
	user
	peer
)

type reader struct {
	find     func(hint, session string) string
	classify func(line []byte) (int64, kind)
}

var readers = map[string]reader{
	"claude-code": {find: claudeFind, classify: claudeLine},
}

// claudeRoots are Claude Code's configuration directories: ~/.claude first,
// so a session found there is never looked up anywhere else, then
// CLAUDE_CONFIG_DIR as the hub process found it when it started. (The
// hub's environment is that of the client that started it: an agent can
// only choose it by killing the hub, the same class as editing its
// database while it's down; running the hub under its own account closes
// both.)
func claudeRoots() []string {
	var out []string
	if h, err := env.OSHome(); err == nil {
		out = append(out, filepath.Join(h, ".claude"))
	}
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); filepath.IsAbs(d) {
		out = append(out, d)
	}
	return out
}

// claudeFind returns the session's transcript, which Claude Code keeps at
// <config>/projects/<project>/<session id>.jsonl. Only a regular file there,
// symlinks resolved, counts: a copy anywhere else (in a repo, where an agent
// writes freely, even under a directory it named "projects") is never read.
func claudeFind(hint, session string) string {
	if !safeID(session) {
		return ""
	}
	name := session + ".jsonl"
	for _, root := range claudeRoots() {
		projects, err := filepath.EvalSymlinks(filepath.Join(root, "projects"))
		if err != nil {
			continue
		}
		if filepath.IsAbs(hint) && filepath.Base(hint) == name {
			if p := inside(projects, hint); p != "" {
				return p
			}
		}
		best, newest := "", time.Time{}
		matches, _ := filepath.Glob(filepath.Join(projects, "*", name))
		for _, m := range matches {
			if p := inside(projects, m); p != "" {
				if fi, err := os.Stat(p); err == nil && fi.ModTime().After(newest) {
					best, newest = p, fi.ModTime()
				}
			}
		}
		if best != "" {
			return best
		}
	}
	return ""
}

// inside returns path with symlinks resolved when it is a regular file in a
// project directory directly under projects, else "".
func inside(projects, path string) string {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	if !samePath(filepath.Dir(filepath.Dir(real)), projects) {
		return ""
	}
	fi, err := os.Lstat(real)
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return real
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b) // case-insensitive file systems
	}
	return a == b
}

// claudeLine: Claude Code marks the entry that starts a turn with its
// origin (claudeTurn): the user's prompt, or anything else that started
// one (another session, ainterlinked holding it open for peer mail).
func claudeLine(line []byte) (int64, kind) {
	cause, at, ok := claudeTurn(line)
	switch {
	case !ok:
		return 0, other
	case cause == CauseOwn:
		return at, user
	}
	return at, peer
}

func safeID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	return strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") == ""
}

// scanBack calls visit on each line of f, last first, until visit returns
// false or budget bytes have been read. A line longer than budget is
// skipped.
func scanBack(f io.ReaderAt, size, budget int64, visit func([]byte) bool) error {
	const chunk = 256 << 10
	var carry []byte // the start of a line whose beginning is further back
	pos, read := size, int64(0)
	for pos > 0 && read < budget {
		n := int64(chunk)
		if n > pos {
			n = pos
		}
		pos -= n
		read += n
		buf := make([]byte, n, n+int64(len(carry)))
		if _, err := f.ReadAt(buf, pos); err != nil && err != io.EOF {
			return err
		}
		buf = append(buf, carry...)
		lines := bytes.Split(buf, []byte{'\n'})
		for i := len(lines) - 1; i >= 1; i-- {
			if l := bytes.TrimSpace(lines[i]); len(l) > 0 && !visit(l) {
				return nil
			}
		}
		carry = lines[0]
		if int64(len(carry)) > budget {
			carry = nil
		}
	}
	if pos == 0 {
		if l := bytes.TrimSpace(carry); len(l) > 0 {
			visit(l)
		}
	}
	return nil
}
