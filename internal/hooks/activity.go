package hooks

import (
	"regexp"
	"strings"

	"github.com/romanpert/ainterlinked/internal/redact"
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
	"gh": true, "dotnet": true, "terraform": true, "ainterlinked": true, "interlinked": true,
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
			multiline = multiline || strings.ContainsAny(w.text, "\r\n")
		}
		for len(seg) > 0 && envAssign.MatchString(seg[0].text) {
			seg = seg[1:]
		}
		if len(seg) == 0 || strings.HasPrefix(seg[0].text, "$") || dirCommands[strings.ToLower(baseProgram(seg[0].text))] {
			if multiline {
				return ""
			}
			continue // an assignment ($x = …, $env:X = …, X=1) or a directory change
		}
		first := seg[0]
		// A quoted program is only believable as a path to an executable
		// ("C:/Program Files/…/node.exe"); a bare quoted word is just text.
		if first.quoted && !strings.ContainsAny(first.text, `/\`) {
			return ""
		}
		prog := baseProgram(first.text)
		if !programName.MatchString(prog) {
			return ""
		}
		if subcommandTools[prog] && len(seg) > 1 && !seg[1].quoted && knownVerbs[seg[1].text] {
			prog += " " + seg[1].text
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

// shellWord is one word of a command; quoted marks that some of it was
// inside quotes.
type shellWord struct {
	text   string
	quoted bool
}

// shellSegments splits a command line into simple commands at unquoted ;, &,
// |, ( ) and line breaks, and each into words. Line continuations (\ or `
// before a newline) join lines; a backslash escapes a following space, quote
// or separator; # starts a comment. Redirections (<, >, 2>&1, <<EOF) are
// dropped together with their target, which is a file name, not a program.
func shellSegments(cmd string) [][]shellWord {
	segs, _ := shellParse(cmd)
	return segs
}

// shellParse is shellSegments that also returns the targets of output
// redirections (> and >>), the files the command line writes that way.
func shellParse(cmd string) ([][]shellWord, []string) {
	var (
		segs      [][]shellWord
		words     []shellWord
		word      strings.Builder
		quote     rune
		inTok     bool
		quoted    bool
		skipNext  bool // the next word is a redirection target
		outTarget bool // … of an output redirection
		writes    []string
	)
	endWord := func() {
		if inTok {
			if skipNext {
				skipNext = false
				if outTarget {
					writes = append(writes, word.String())
				}
			} else {
				words = append(words, shellWord{text: word.String(), quoted: quoted})
			}
			word.Reset()
			inTok, quoted = false, false
		}
	}
	endSeg := func() {
		endWord()
		skipNext = false
		if len(words) > 0 {
			segs = append(segs, words)
			words = nil
		}
	}
	isDigits := func(s string) bool {
		return s != "" && strings.Trim(s, "0123456789") == ""
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
		case r == '\\' && next != 0 && strings.ContainsRune(" \t'\";&|()<>#\\", next):
			word.WriteRune(next)
			inTok = true
			i++
		case r == '"' || r == '\'':
			quote, inTok, quoted = r, true, true
		case r == '#' && !inTok:
			for i+1 < len(rs) && rs[i+1] != '\n' {
				i++
			}
		case r == '<' || r == '>':
			// "2>" / "1>&2": a bare number glued to the operator is a file
			// descriptor, not an argument.
			if inTok && !quoted && isDigits(word.String()) {
				word.Reset()
				inTok = false
			}
			endWord()
			out, dup := r == '>', false
			for i+1 < len(rs) && (rs[i+1] == '<' || rs[i+1] == '>' || rs[i+1] == '&') {
				dup = dup || rs[i+1] == '&'
				i++ // >>, <<, >&, <&
			}
			skipNext, outTarget = true, out && !dup // ">&2" duplicates a descriptor
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
	return segs, writes
}

// promptScan bounds the work done on a prompt: only its start is shown.
const promptScan = 400

func activityFromPrompt(p string) string {
	if r := []rune(p); len(r) > promptScan {
		p = string(r[:promptScan])
	}
	p = strings.Join(strings.Fields(redact.Secrets(p)), " ")
	if p == "" {
		return ""
	}
	if r := []rune(p); len(r) > 80 {
		p = string(r[:80]) + "…"
	}
	return "on: " + p
}
