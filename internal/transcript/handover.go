package transcript

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// A handover is what a session that left knew, for whoever carries its work
// on: read from the harness's own transcript, so it costs the leaving
// session nothing, and handed over by reference (one line at the next
// session's start, the body only when that session asks).
//
// For Claude Code it is the last compaction summary (Claude Code's own
// account of the work: request, decisions, files, what's pending, the next
// step), the user's requests since, and the session's last report.
type Handover struct {
	Body string
	Gist string // one line: where the work stands
}

// Caps on what a handover keeps from each part.
const (
	maxSummary  = 40000
	maxReport   = 6000
	maxRequest  = 800
	maxRequests = 3
	gistChars   = 160
)

// ReadHandover builds the handover of a harness session from its
// transcript. The transcript must be of a session that ran in project (the
// checkout the agent registered from): the first working directory Claude
// Code recorded in it is that folder or inside it, so a session id claimed
// at registration can't bring another checkout's transcript in. ok is false
// when the transcript can't be found, ran elsewhere, or the session never
// reported anything.
func ReadHandover(harness, session, project string) (Handover, bool) {
	if harness != "claude-code" || project == "" {
		return Handover{}, false
	}
	path := claudeFind("", session)
	if path == "" || !regular(path) {
		return Handover{}, false
	}
	var summary, report, first, cwd string
	var asks []string
	var c cursor // reads whole lines, however long
	c.read(path, func(line []byte) {
		var e struct {
			Type             string `json:"type"`
			Cwd              string `json:"cwd"`
			IsSidechain      bool   `json:"isSidechain"`
			IsCompactSummary bool   `json:"isCompactSummary"`
			IsMeta           bool   `json:"isMeta"`
			Origin           *struct {
				Kind string `json:"kind"`
			} `json:"origin"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &e) != nil || e.IsSidechain {
			return
		}
		if cwd == "" {
			cwd = e.Cwd
		}
		switch {
		case e.IsCompactSummary:
			summary, asks, report = textOf(e.Message.Content), nil, ""
		case e.Type == "user" && !e.IsMeta && e.Origin != nil && e.Origin.Kind == "human":
			t := strings.TrimSpace(textOf(e.Message.Content))
			if t == "" {
				return
			}
			if first == "" {
				first = t
			}
			asks = append(asks, t)
			if len(asks) > maxRequests {
				asks = asks[1:]
			}
		case e.Type == "assistant":
			if t := strings.TrimSpace(textOf(e.Message.Content)); t != "" {
				report = t
			}
		}
	})
	if !within(cwd, project) || (report == "" && summary == "") {
		return Handover{}, false
	}
	var b strings.Builder
	summary = cleanSummary(summary)
	if summary != "" {
		b.WriteString("## Where the work stood (Claude Code's last compaction summary)\n\n")
		b.WriteString(cut(summary, maxSummary))
		b.WriteString("\n\n")
	} else if first != "" {
		b.WriteString("## The first request\n\n")
		b.WriteString(cut(first, maxRequest))
		b.WriteString("\n\n")
	}
	if len(asks) > 0 {
		b.WriteString("## The user's latest requests\n\n")
		for _, a := range asks {
			b.WriteString("- " + strings.ReplaceAll(cut(a, maxRequest), "\n", "\n  ") + "\n")
		}
		b.WriteString("\n")
	}
	if report != "" {
		b.WriteString("## The session's last report\n\n")
		b.WriteString(cut(report, maxReport))
		b.WriteString("\n")
	}
	return Handover{Body: strings.TrimSpace(b.String()), Gist: gistOf(summary, report, first)}, true
}

// within reports whether dir is project or a folder inside it.
func within(dir, project string) bool {
	if dir == "" {
		return false
	}
	d, p := filepath.Clean(filepath.FromSlash(dir)), filepath.Clean(filepath.FromSlash(project))
	if samePath(d, p) {
		return true
	}
	sep := string(filepath.Separator)
	return len(d) > len(p) && samePath(d[:len(p)+1], strings.TrimSuffix(p, sep)+sep)
}

// textOf returns a message content's text: a plain string, or the text
// parts of a list (tool calls and results are left out).
func textOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var out []string
	for _, p := range parts {
		if p.Type == "text" && strings.TrimSpace(p.Text) != "" {
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, "\n")
}

var reminder = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

// cleanSummary keeps the account and drops what Claude Code wrapped around
// it for the session itself: the preamble, system reminders, and the
// closing order to carry on without asking (an order to that session, not
// to whoever reads the handover).
func cleanSummary(s string) string {
	s = reminder.ReplaceAllString(s, "")
	s = strings.TrimPrefix(strings.TrimSpace(s), "This session is being continued from a previous conversation that ran out of context. The summary below covers the earlier portion of the conversation.")
	if i := strings.Index(s, "Continue the conversation from where it left off"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

var heading = regexp.MustCompile(`(?im)^[#*\s\d.]*(current work|optional next step|next step)\b[*:]*`)

// gistOf picks one line on where the work stands: the summary's "Current
// Work" (or next step) section, else the last report, else the first
// request.
func gistOf(summary, report, first string) string {
	src := report
	if loc := heading.FindStringIndex(summary); loc != nil {
		src = summary[loc[1]:]
	}
	if strings.TrimSpace(src) == "" {
		src = first
	}
	return cut(strings.Join(strings.Fields(src), " "), gistChars)
}

// cut shortens s to at most n bytes on a character boundary, marking it.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.TrimSpace(s[:n]) + "…"
}
