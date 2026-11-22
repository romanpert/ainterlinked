// Package redact hides credential-looking text before it leaves the session
// that wrote it: activity lines, handovers.
package redact

import (
	"regexp"
	"strings"
)

const redacted = "[redacted]"

var secretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	// user:password@ in URLs
	{regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^/\s:@]+:)[^@\s/]+@`), "${1}" + redacted + "@"},
	// NAME=value / "name": "value" where the name says it's a secret
	{regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*(?:password|passwd|pwd|pass|secret|token|api[_-]?key|apikey|access[_-]?key|private[_-]?key|credentials?)["']?\s*[:=]\s*)("[^"]*"|'[^']*'|\S+)`), "${1}" + redacted},
	// HTTP auth schemes followed by something token-shaped
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{12,}`), "$1 " + redacted},
	// well-known token formats
	{regexp.MustCompile(`\b(sk|pk|rk)-[A-Za-z0-9_-]{8,}`), redacted},
	{regexp.MustCompile(`\b(ghp|gho|ghs|ghu|ghr|github_pat)_[A-Za-z0-9_]{10,}`), redacted},
	{regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{10,}`), redacted},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{8,}`), redacted},
	{regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{12,}`), redacted},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9._-]+`), redacted},
}

// Long random-looking runs (no "/", so file paths survive).
var longToken = regexp.MustCompile(`[A-Za-z0-9+_=-]{32,}`)

// Secrets hides credential-looking text. It's best effort: a secret
// written as plain prose ("the password is …") can't be told apart, which is
// why prompts are only shared on opt-in.
func Secrets(s string) string {
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return longToken.ReplaceAllStringFunc(s, func(t string) string {
		if strings.ContainsAny(t, "0123456789") && strings.IndexFunc(t, func(r rune) bool {
			return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'
		}) >= 0 {
			return redacted
		}
		return t
	})
}
