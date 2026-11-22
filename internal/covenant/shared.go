package covenant

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/romanpert/ainterlinked/internal/core"
)

// Some files are ledgers that many agents keep at once: an open-items list
// each session prunes, a decision log each one appends to. Held whole they
// become a queue (2026-11-22, one repo: 221 of 249 queue waits in a day were
// for three such files, each held for minutes over a one-line edit). The
// owner can declare them shared in the covenant; then nobody holds them.
//
// That is safe only with tools that refuse to write over a newer version of
// the file (Claude Code's Edit and Write, Codex's apply_patch); the owner
// declaring a file shared accepts that, and says how to edit it in the
// section's rules, which are shown at the first touch like any area's.

// SharedPatterns returns the globs declared shared.
func (c *Covenant) SharedPatterns() []string {
	if c == nil {
		return nil
	}
	var out []string
	for _, s := range c.Sections {
		if s.Shared {
			out = append(out, s.Patterns...)
		}
	}
	return out
}

// IsShared reports whether a project-relative file is declared shared.
func (c *Covenant) IsShared(rel string) bool {
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "./")
	for _, p := range c.SharedPatterns() {
		if core.Match(p, rel) || core.Match(strings.ToLower(p), strings.ToLower(rel)) {
			return true
		}
	}
	return false
}

// sharedHeading strips a "shared:" prefix from a heading.
func sharedHeading(h string) (string, bool) {
	t := strings.TrimSpace(h)
	if len(t) >= 7 && strings.EqualFold(t[:7], "shared:") {
		return t[7:], true
	}
	return h, false
}

// Shares reports whether the covenant of the project at root declares rel
// shared. It is asked on every edit, so the parsed covenant is kept until
// the file changes (checked by size and time, at most once a second).
func Shares(root, rel string) bool {
	return sharedCache.get(root).IsShared(rel)
}

var sharedCache = &cache{m: map[string]*entry{}}

type cache struct {
	mu sync.Mutex
	m  map[string]*entry
}

type entry struct {
	checked time.Time
	stamp   string
	c       *Covenant
}

func (k *cache) get(root string) *Covenant {
	if root == "" {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	e := k.m[root]
	if e != nil && time.Since(e.checked) < time.Second {
		return e.c
	}
	st := stamp(root)
	if e != nil && e.stamp == st {
		e.checked = time.Now()
		return e.c
	}
	c, err := Load(root)
	if err != nil {
		c = nil // unreadable: nothing is shared, reservations work as usual
	}
	k.m[root] = &entry{checked: time.Now(), stamp: st, c: c}
	return c
}

// stamp identifies the version of the covenant on disk ("" when none).
func stamp(root string) string {
	for _, f := range []string{File, LegacyFile} {
		if fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err == nil {
			return f + "|" + strconv.FormatInt(fi.ModTime().UnixNano(), 10) + "|" + strconv.FormatInt(fi.Size(), 10)
		}
	}
	return ""
}
