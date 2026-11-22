package covenant

import (
	"path/filepath"
	"strings"
)

// A covenant can declare files shared ("## shared: NEXT.md, docs/log.md"):
// ledgers many agents keep at once, which nobody should hold (see
// core.SetShared). The declaration is a proposal: the hub never reads the
// repo, which agents can write. The owner accepts it with `ainterlinked
// shared accept`, which shows the files and records them in the hub. The
// section's rules, on how to edit those files, are shown at the first touch
// like any area's. Only plain files can be shared: a glob could free the
// whole repo.

// SharedFiles returns the files the covenant declares shared.
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

// IsShared reports whether the covenant declares a project-relative file shared.
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
