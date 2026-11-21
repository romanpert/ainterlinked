package hooks

import (
	"regexp"
	"strings"
)

// Activity lines are broadcast to every agent on the bus and to the
// dashboard. For a shell command they carry the first program it runs (and a
// well-known verb such as "git commit"), never an argument: arguments hold
// hosts, paths and secrets, and no shell parser can be trusted to find them
// all across bash, PowerShell and cmd. Prompts are shared only on opt-in.

// subcommandTools are programs whose first argument is an action worth
// showing, but only when it's one of verbs below (never a script, target
// or host name).
var subcommandTools = map[string]bool{
	"git": true, "go": true, "npm": true, "pnpm": true, "yarn": true, "bun": true, "deno": true,
	"cargo": true, "docker": true, "kubectl": true, "uv": true, "pip": true, "poetry": true,
	"gh": true, "dotnet": true, "terraform": true, "interlinked": true,
}

var knownVerbs = map[string]bool{
	"add": true, "apply": true, "build": true, "checkout": true, "clone": true, "commit": true,
	"compose": true, "deploy": true, "describe": true, "diff": true, "down": true, "exec": true,
	"fetch": true, "fmt": true, "get": true, "init": true, "install": true, "lint": true,
	"log": true, "logs": true, "merge": true, "plan": true, "pr": true, "ps": true,
	"publish": true, "pull": true, "push": true, "rebase": true, "release": true, "restart": true,
	"run": true, "status": true, "switch": true, "sync": true, "test": true, "up": true, "vet": true,
}

// dirCommands only change the shell's state; the next command is the work.
var dirCommands = map[string]bool{
	"cd": true, "pushd": true, "popd": true, "export": true, "set": true, "setx": true,
	"set-location": true, "sl": true, "source": true, ".": true,
}

var (
	envAssign   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	programName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._+-]{0,39}$`)
	exeSuffix   = regexp.MustCompile(`(?i)\.(exe|cmd|bat|ps1|sh)$`)
)

func activityFromTool(in Input, project string) string {
	if in.ToolName == "" {
		return ""
	}
	if files := EditedFiles(in); len(files) > 0 {
		return in.ToolName + " " + relDisplay(project, files[0])
	}
	if cmd, ok := in.ToolInput["command"].(string); ok {
		if prog := commandProgram(cmd); prog != "" {
			return in.ToolName + ": " + prog
		}
	}
	return in.ToolName
}

// commandProgram returns the program a command line runs first, skipping
// variable assignments and directory changes, or "" when it can't tell.
func commandProgram(cmd string) string {
	for _, seg := range shellSegments(cmd) {
		// Past a multi-line quoted string (a here-string's body) the
		// segments are text, not commands: stop rather than guess.
		multiline := false
		for _, w := range seg {
			multiline = multiline || strings.ContainsAny(w, "\r\n")
		}
		for len(seg) > 0 && envAssign.MatchString(seg[0]) {
			seg = seg[1:]
		}
		if len(seg) == 0 || strings.HasPrefix(seg[0], "$") || dirCommands[strings.ToLower(baseProgram(seg[0]))] {
			if multiline {
				return ""
			}
			continue // an assignment ($x = …, $env:X = …, X=1) or a directory change
		}
		prog := baseProgram(seg[0])
		if !programName.MatchString(prog) {
			return ""
		}
		if subcommandTools[prog] && len(seg) > 1 && knownVerbs[seg[1]] {
			prog += " " + seg[1]
		}
		return prog
	}
	return ""
}

func baseProgram(tok string) string {
	if i := strings.LastIndexAny(tok, `/\`); i >= 0 {
		tok = tok[i+1:]
	}
	return exeSuffix.ReplaceAllString(tok, "")
}

// shellSegments splits a command line into simple commands at unquoted ;, &,
// |, ( ) and line breaks, and each into words. Line continuations (\ or `
// before a newline) join lines; a backslash escapes a following space, quote
// or separator; < and > end a word; # starts a comment.
func shellSegments(cmd string) [][]string {
	var (
		segs  [][]string
		words []string
		word  strings.Builder
		quote rune
		inTok bool
	)
	endWord := func() {
		if inTok {
			words = append(words, word.String())
			word.Reset()
			inTok = false
		}
	}
	endSeg := func() {
		endWord()
		if len(words) > 0 {
			segs = append(segs, words)
			words = nil
		}
	}
	rs := []rune(cmd)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		next := rune(0)
		if i+1 < len(rs) {
			next = rs[i+1]
		}
		switch {
		case quote != 0:
			if r == '\\' && quote == '"' && (next == '"' || next == '\\') {
				word.WriteRune(next)
				i++
			} else if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
		case (r == '\\' || r == '`') && (next == '\n' || next == '\r'):
			endWord() // line continuation
			i++
			if next == '\r' && i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
		case r == '\\' && strings.ContainsRune(" \t'\";&|()<>#\\", next) && next != 0:
			word.WriteRune(next)
			inTok = true
			i++
		case r == '"' || r == '\'':
			quote, inTok = r, true
		case r == '#' && !inTok:
			for i+1 < len(rs) && rs[i+1] != '\n' {
				i++
			}
		case r == '<' || r == '>':
			endWord()
		case r == ';' || r == '&' || r == '|' || r == '\n' || r == '(' || r == ')':
			endSeg()
		case r == ' ' || r == '\t' || r == '\r':
			endWord()
		default:
			word.WriteRune(r)
			inTok = true
		}
	}
	endSeg()
	return segs
}

// promptScan bounds the work done on a prompt: only its start is shown.
const promptScan = 400

func activityFromPrompt(p string) string {
	if r := []rune(p); len(r) > promptScan {
		p = string(r[:promptScan])
	}
	p = strings.Join(strings.Fields(redactSecrets(p)), " ")
	if p == "" {
		return ""
	}
	if r := []rune(p); len(r) > 80 {
		p = string(r[:80]) + "…"
	}
	return "on: " + p
}

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

// redactSecrets hides credential-looking text. It's best effort: a secret
// written as plain prose ("the password is …") can't be told apart, which is
// why prompts are only shared on opt-in.
func redactSecrets(s string) string {
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
