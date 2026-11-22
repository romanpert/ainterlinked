package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/romanpert/ainterlinked/internal/config"
	"github.com/romanpert/ainterlinked/internal/covenant"
	"github.com/romanpert/ainterlinked/internal/session"
)

// How a repo's covenant reaches an agent, cheapest first:
//
//  1. Session start: the core rules. A small covenant goes in whole, and
//     then nothing else is ever needed.
//  2. After a tool call that touched a file (usually reading it before an
//     edit): the rules of that file's area, as context. No extra turn.
//  3. Before an edit in an area whose rules the agent hasn't seen yet: the
//     edit is held back once with them. That costs a retry, so it is only
//     the safety net for edits made without looking first.
//
// Each area is shown once per session; /clear and compaction end the
// session (and start a new briefing), so a model that lost its context is
// shown the rules again. An amended covenant is shown again too.

// smallCovenant is the size (characters of rules) under which the whole
// covenant goes into the briefing: cheaper than any notice and retry.
const smallCovenant = 700

// covenantBrief is the briefing's covenant part.
func covenantBrief(cfg config.Config, agent, project string) string {
	cov := loadCovenant(cfg, project)
	if cov == nil {
		return ""
	}
	var b strings.Builder
	small := cov.RulesSize() <= smallCovenant
	if core := cov.Core(); len(core) > 0 {
		b.WriteString("\nThis repo's covenant (" + covenant.File + ", set by its owner) asks every agent:")
		b.WriteString(covenant.Bullets(core, covenant.MaxCore))
	}
	if cov.Areas() == 0 {
		return b.String()
	}
	if b.Len() == 0 {
		b.WriteString("\nThis repo has a covenant (" + covenant.File + ", set by its owner).")
	}
	if !small {
		b.WriteString("\nIt has rules for " + plural(cov.Areas(), "area") + " too; you're shown them when you work there.")
		return b.String()
	}
	var keys, areas []string
	for _, s := range cov.AreaSections() {
		b.WriteString("\nFor " + strings.Join(s.Patterns, ", ") + ":")
		b.WriteString(covenant.Bullets(s.Rules, covenant.MaxScope))
		keys = append(keys, cov.Hash+" "+strconv.Itoa(s.Line))
		areas = append(areas, s.Key())
	}
	markShown(agent, keys)
	logShown(agent, project, cov.Hash, "briefing", areas)
	return b.String()
}

// covenantNotice returns the rules of the areas files fall in that the
// agent hasn't been shown (and marks them shown), or "". held words it for
// an edit that was held back.
func covenantNotice(cfg config.Config, agent, project string, files []string, held bool) string {
	cov := loadCovenant(cfg, project)
	if cov == nil || cov.Areas() == 0 {
		return ""
	}
	seen := shownAreas(agent)
	var b strings.Builder
	var keys, areas []string
	for _, f := range files {
		rel, ok := relToProject(project, f)
		if !ok {
			continue
		}
		for _, s := range cov.For(rel) {
			key := cov.Hash + " " + strconv.Itoa(s.Line)
			if seen[key] {
				continue
			}
			seen[key] = true
			keys = append(keys, key)
			areas = append(areas, s.Key())
			b.WriteString("\nFor " + strings.Join(s.Patterns, ", ") + ":")
			b.WriteString(covenant.Bullets(s.Rules, covenant.MaxScope))
		}
	}
	if len(keys) == 0 {
		return ""
	}
	markShown(agent, keys)
	if held {
		logShown(agent, project, cov.Hash, "held", areas)
		// Say plainly that nothing was applied: a model told only "held back"
		// may wait for a result that never comes.
		return "interlinked: edit NOT applied. This repo's covenant (" + covenant.File + ") has rules for this file; read them first (shown once):" +
			b.String() + "\nIf your edit complies, run the same edit again now."
	}
	logShown(agent, project, cov.Hash, "context", areas)
	return "[interlinked] Covenant rules (" + covenant.File + ") for what you're working on:" + b.String()
}

// logShown appends one line per delivery to <home>/covenant.log: which
// areas reached which agent and how. It is what tells an owner which rules
// fire (and which never do, so they can go), and what tests measure.
func logShown(agent, project, hash, route string, areas []string) {
	p := filepath.Join(config.Home(), "covenant.log")
	if st, err := os.Stat(p); err == nil && st.Size() > 1<<20 {
		_ = os.Rename(p, p+".1")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	line, _ := json.Marshal(map[string]any{"ts": time.Now().UnixMilli(), "agent": agent, "project": project, "covenant": hash, "route": route, "areas": areas})
	_, _ = f.Write(append(line, '\n'))
}

func loadCovenant(cfg config.Config, project string) *covenant.Covenant {
	if !cfg.Covenant.Show || project == "" {
		return nil
	}
	cov, err := covenant.Load(project)
	if err != nil {
		logf("covenant %s: %v", project, err)
		return nil
	}
	return cov
}

// relToProject makes a tool's file path project-relative (false if it's
// outside the project).
func relToProject(project, f string) (string, bool) {
	f = session.Canonical(f)
	if !filepath.IsAbs(f) {
		f = filepath.ToSlash(filepath.Clean(f))
		return strings.TrimPrefix(f, "./"), !strings.HasPrefix(f, "../")
	}
	rel := relDisplay(project, f)
	return rel, rel != filepath.ToSlash(f)
}

// Areas already shown to an agent, in a tiny file per agent (like holds),
// dropped when the session ends.
func shownPath(agent string) string {
	return filepath.Join(config.Home(), "covenant", filepath.Base(agent))
}

func shownAreas(agent string) map[string]bool {
	out := map[string]bool{}
	b, _ := os.ReadFile(shownPath(agent))
	for _, l := range strings.Split(string(b), "\n") {
		if l != "" {
			out[l] = true
		}
	}
	return out
}

func markShown(agent string, keys []string) {
	if len(keys) == 0 {
		return
	}
	_ = os.MkdirAll(filepath.Dir(shownPath(agent)), 0o700)
	f, err := os.OpenFile(shownPath(agent), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(strings.Join(keys, "\n") + "\n")
}

func forgetShown(agent string) { _ = os.Remove(shownPath(agent)) }

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}
