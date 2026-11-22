// Package transcript reads, from a harness's own record of a session, who
// started the latest turn: the user or a peer. Hooks can report a turn, but
// an agent can run a hook command from its own shell; the transcript is
// written by the harness, and forging it means tampering with the harness's
// files, not running a command.
package transcript

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
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
// is false when interlinked can't read this harness's transcripts; a path
// that doesn't belong to the session is known with no user prompt in it.
func Read(harness, path, session string) (o Origin, known bool) {
	r, ok := readers[harness]
	if !ok {
		return Origin{}, false
	}
	if !r.owns(path, session) {
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
	owns     func(path, session string) bool
	classify func(line []byte) (int64, kind)
}

var readers = map[string]reader{
	"claude-code": {owns: claudeOwns, classify: claudeLine},
}

// claudeOwns: Claude Code keeps a session at
// <config>/projects/<project>/<session id>.jsonl.
func claudeOwns(path, session string) bool {
	if !safeID(session) || !filepath.IsAbs(path) {
		return false
	}
	path = filepath.Clean(path)
	return filepath.Base(path) == session+".jsonl" &&
		filepath.Base(filepath.Dir(filepath.Dir(path))) == "projects"
}

// claudeLine: Claude Code marks the entry that starts a turn with its
// origin: "human" (typed by the user; "sdk" for a prompt passed on the
// command line, also the user's) or "peer" (another session). Background
// task notifications continue the turn that started the task: skipped.
func claudeLine(line []byte) (int64, kind) {
	if !bytes.Contains(line, []byte(`"origin"`)) && !bytes.Contains(line, []byte(`"turnOrigin"`)) {
		return 0, other
	}
	var e struct {
		Timestamp  string `json:"timestamp"`
		TurnOrigin string `json:"turnOrigin"`
		Origin     *struct {
			Kind string `json:"kind"`
		} `json:"origin"`
	}
	if json.Unmarshal(line, &e) != nil {
		return 0, other
	}
	how := e.TurnOrigin
	if e.Origin != nil {
		how = e.Origin.Kind
	}
	t, err := time.Parse(time.RFC3339Nano, e.Timestamp)
	if err != nil {
		return 0, other
	}
	switch how {
	case "human", "sdk":
		return t.UnixMilli(), user
	case "peer":
		return t.UnixMilli(), peer
	}
	return 0, other
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
