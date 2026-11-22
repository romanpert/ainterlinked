// Package covenant reads a repo's covenant: the few rules its owner sets for
// the agents working in it, kept in .ainterlinked/covenant.md and versioned
// with the code (amending it is a commit the owner accepts).
//
// Rules are attached to the paths they govern, like covenants that run with
// the land: an agent is shown a rule when it is about to touch those paths,
// not every rule all the time. Only the core section (rules for everyone,
// everywhere) goes into the session briefing. That keeps the cost in tokens
// near zero and nobody has to remember anything.
//
// Format (plain Markdown, readable by people and any harness):
//
//	# Covenant            ← anything before the first "##" is for humans
//	## *                  ← core: every agent, at session start
//	- Run the tests before committing.
//	## db/migrations/**, schema.sql   ← globs, relative to the repo root
//	- Never edit a merged migration; add a new one.
//	## shared: NEXT.md, docs/decisions.md   ← nobody holds these (see Shares)
//	- Edit only your own lines, with Edit; never rewrite the file.
package covenant

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/romanpert/ainterlinked/internal/core"
)

// File is where a project keeps its covenant, relative to its root.
const File = ".ainterlinked/covenant.md"

// LegacyFile is where it was kept before the rename; it is still read when
// File doesn't exist.
const LegacyFile = ".interlinked/covenant.md"

// Limits keep the covenant small: rule space is a commons too, and an
// oversized covenant is clipped where it's shown rather than flooding contexts.
const (
	MaxCore  = 800      // characters of core rules in a briefing
	MaxScope = 1200     // characters shown for the rules of one area
	maxRead  = 64 << 10 // bytes read from the file
	MaxFile  = 6 << 10  // size above which Problems flags the file
)

// Section is one heading: the areas it governs and its rules.
type Section struct {
	Patterns []string // project-relative globs; empty for the core section
	Rules    []string
	Line     int // 1-based line of the heading
	// Shared marks the files as kept by many agents at once (heading
	// "shared: <globs>"): nobody holds them. See Shares.
	Shared bool
}

// Key identifies the section for "already shown" bookkeeping.
func (s Section) Key() string {
	if len(s.Patterns) == 0 {
		return "*"
	}
	return strings.Join(s.Patterns, ",")
}

// Covenant is a parsed covenant file.
type Covenant struct {
	Sections []Section
	Hash     string // of the file, so a changed covenant is shown again
	Size     int
}

// Load reads the covenant of the project at root (nil when there is none).
func Load(root string) (*Covenant, error) {
	if root == "" {
		return nil, nil
	}
	f, err := os.Open(filepath.Join(root, filepath.FromSlash(File)))
	if os.IsNotExist(err) {
		f, err = os.Open(filepath.Join(root, filepath.FromSlash(LegacyFile)))
	}
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxRead))
	if err != nil {
		return nil, err
	}
	return Parse(string(b)), nil
}

// Parse reads covenant text. It never fails: unknown lines are kept as
// rule text or ignored, so a hand-edited file always yields something sane.
func Parse(src string) *Covenant {
	sum := sha256.Sum256([]byte(src))
	c := &Covenant{Hash: hex.EncodeToString(sum[:4]), Size: len(src)}
	var cur *Section
	comment := false
	for i, raw := range strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if comment || strings.HasPrefix(line, "<!--") {
			comment = !strings.Contains(line, "-->")
			continue
		}
		switch {
		case strings.HasPrefix(line, "## "):
			h, shared := sharedHeading(line[3:])
			sec := Section{Patterns: patterns(h), Line: i + 1}
			sec.Shared = shared && len(sec.Patterns) > 0 // "shared: *" would leave nothing to hold
			c.Sections = append(c.Sections, sec)
			cur = &c.Sections[len(c.Sections)-1]
		case cur == nil || line == "" || strings.HasPrefix(line, "#"):
			// preamble, blank lines and other headings are for humans
		case strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* "):
			cur.Rules = append(cur.Rules, strings.TrimSpace(line[2:]))
		case len(cur.Rules) > 0 && raw != line: // indented: continues the rule above
			cur.Rules[len(cur.Rules)-1] += " " + line
		default:
			cur.Rules = append(cur.Rules, line)
		}
	}
	return c
}

// patterns parses a heading: "*", "all" or "core" is the core section,
// anything else a comma-separated list of globs (a trailing / is a folder).
func patterns(h string) []string {
	var out []string
	for _, p := range strings.Split(h, ",") {
		p = strings.Trim(strings.TrimSpace(p), "`")
		p = strings.TrimPrefix(filepath.ToSlash(p), "./")
		switch strings.ToLower(p) {
		case "", "*", "**", "all", "core":
			return nil
		}
		if strings.HasSuffix(p, "/") {
			p += "**"
		}
		out = append(out, p)
	}
	return out
}

// Core returns the rules every agent gets at session start.
func (c *Covenant) Core() []string {
	if c == nil {
		return nil
	}
	var out []string
	for _, s := range c.Sections {
		if len(s.Patterns) == 0 {
			out = append(out, s.Rules...)
		}
	}
	return out
}

// AreaSections returns the path-scoped sections that have rules.
func (c *Covenant) AreaSections() []Section {
	if c == nil {
		return nil
	}
	var out []Section
	for _, s := range c.Sections {
		if len(s.Patterns) > 0 && len(s.Rules) > 0 {
			out = append(out, s)
		}
	}
	return out
}

// Areas counts the path-scoped sections that have rules.
func (c *Covenant) Areas() int { return len(c.AreaSections()) }

// RulesSize is the characters of rule text, what showing it all would cost.
func (c *Covenant) RulesSize() int {
	n := 0
	if c != nil {
		for _, s := range c.Sections {
			for _, r := range s.Rules {
				n += len(r) + 3
			}
		}
	}
	return n
}

// For returns the path-scoped sections governing a project-relative file.
func (c *Covenant) For(rel string) []Section {
	if c == nil {
		return nil
	}
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "./")
	var out []Section
	for _, s := range c.Sections {
		if len(s.Rules) == 0 {
			continue
		}
		for _, p := range s.Patterns {
			if core.Match(p, rel) || core.Match(strings.ToLower(p), strings.ToLower(rel)) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// Bullets renders rules as a list within a character budget.
func Bullets(rules []string, budget int) string {
	var b strings.Builder
	for i, r := range rules {
		line := "\n- " + r
		if b.Len()+len(line) > budget {
			b.WriteString("\n- … (" + strconv.Itoa(len(rules)-i) + " more in " + File + ")")
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

// Problems lists what makes a covenant costly or ambiguous, for checks.
func (c *Covenant) Problems() []string {
	if c == nil {
		return nil
	}
	var out []string
	if c.Size > MaxFile {
		out = append(out, "the covenant is "+strconv.Itoa(c.Size)+" bytes; keep it under "+strconv.Itoa(MaxFile)+" (rules are a commons: add one, drop one)")
	}
	if n := len(strings.Join(c.Core(), "\n")); n > MaxCore {
		out = append(out, "core rules are "+strconv.Itoa(n)+" characters, briefed to every session; over "+strconv.Itoa(MaxCore)+" they are clipped. Move area-specific ones under their paths")
	}
	for _, s := range c.Sections {
		if len(s.Rules) == 0 && !s.Shared {
			out = append(out, "line "+strconv.Itoa(s.Line)+": section without rules")
		}
		if n := len(strings.Join(s.Rules, "\n")); len(s.Patterns) > 0 && n > MaxScope {
			out = append(out, "line "+strconv.Itoa(s.Line)+": "+strconv.Itoa(n)+" characters of rules for "+s.Key()+"; over "+strconv.Itoa(MaxScope)+" they are clipped")
		}
	}
	return out
}
