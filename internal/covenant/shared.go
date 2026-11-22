package covenant

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Some files are ledgers that many agents keep at once: an open-items list
// each session prunes, a decision log each one appends to. Held whole they
// become a queue (2026-11-22, one repo: 230 of 255 queue waits in a day were
// for four such files, each held for minutes over a one-line edit). The
// owner can declare them shared in the covenant; then nobody holds them.
//
// That is safe only with tools that refuse to write over a newer version of
// the file (Claude Code's Edit and Write, Codex's apply_patch); the owner
// declaring a file shared accepts that, and says how to edit it in the
// section's rules, which are shown at the first touch like any area's.
//
// A declaration takes files away from reservations, so it is read from the
// covenant as committed (HEAD), never from the working tree: an agent's
// uncommitted edit to the covenant can't free a file another agent holds.
// And it names files one by one: a glob could free the whole repo.

// SharedFiles returns the files declared shared.
func (c *Covenant) SharedFiles() []string {
	if c == nil {
		return nil
	}
	var out []string
	for _, s := range c.Sections {
		if s.Shared {
			out = append(out, s.Files...)
		}
	}
	return out
}

// IsShared reports whether a project-relative file is declared shared.
func (c *Covenant) IsShared(rel string) bool {
	rel = strings.ToLower(strings.TrimPrefix(filepath.ToSlash(rel), "./"))
	for _, f := range c.SharedFiles() {
		if strings.ToLower(f) == rel {
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

// literalFiles splits shared patterns into plain file paths and the rest,
// which a shared section ignores (see Problems).
func literalFiles(pats []string) (files, rejected []string) {
	for _, p := range pats {
		if strings.ContainsAny(p, "*?[") || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "/") {
			rejected = append(rejected, p)
			continue
		}
		files = append(files, p)
	}
	return files, rejected
}

// Shares reports whether the committed covenant of the project at root
// declares rel shared. It is asked on edits, so the covenant is looked up
// again at most every Recheck per project.
func Shares(root, rel string) bool {
	return sharedCache.get(root).IsShared(rel)
}

// Recheck is how often a project's committed covenant is looked up again.
var Recheck = 10 * time.Second

var sharedCache = &cache{m: map[string]*entry{}}

type cache struct {
	mu sync.Mutex
	m  map[string]*entry
}

type entry struct {
	mu      sync.Mutex // one lookup at a time per project, none across them
	checked time.Time
	blob    string
	c       *Covenant
}

const maxProjects = 256 // a bound on the cache; past it the oldest is dropped

func (k *cache) get(root string) *Covenant {
	if !localRoot(root) {
		return nil
	}
	k.mu.Lock()
	e := k.m[root]
	if e == nil {
		if len(k.m) >= maxProjects {
			k.dropOldest()
		}
		e = &entry{}
		k.m[root] = e
	}
	k.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.checked.IsZero() && time.Since(e.checked) < Recheck {
		return e.c
	}
	e.checked = time.Now()
	blob, text, ok := committed(root, e.blob)
	switch {
	case !ok:
		e.blob, e.c = "", nil // not a repo, no git, no covenant: nothing is shared
	case blob != e.blob:
		e.blob, e.c = blob, Parse(text)
	}
	return e.c
}

func (k *cache) dropOldest() {
	var old string
	var at time.Time
	for r, e := range k.m {
		if old == "" || e.checked.Before(at) {
			old, at = r, e.checked
		}
	}
	delete(k.m, old)
}

// localRoot refuses network and device paths: a lookup there could make
// the hub reach out to another machine.
func localRoot(root string) bool {
	r := filepath.ToSlash(root)
	return r != "" && !strings.HasPrefix(r, "//") && filepath.IsAbs(root)
}

// committed returns the blob id and text of the covenant at HEAD (File,
// else LegacyFile). The text is read only when the blob is not known.
func committed(root, known string) (blob, text string, ok bool) {
	for _, f := range []string{File, LegacyFile} {
		id, err := git(root, "rev-parse", "--verify", "--quiet", "HEAD:"+f)
		if err != nil || id == "" {
			continue
		}
		if id == known {
			return id, "", true
		}
		kind, err := git(root, "cat-file", "-t", id)
		if err != nil || kind != "blob" {
			return "", "", false
		}
		body, err := git(root, "cat-file", "blob", id)
		if err != nil || len(body) > maxRead {
			return "", "", false
		}
		return id, body, true
	}
	return "", "", false
}

func git(root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	hide(cmd)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
