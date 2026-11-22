package hooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/romanpert/ainterlinked/internal/config"
	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/hub"
	"github.com/romanpert/ainterlinked/internal/session"
)

// Reservations are enforced before edit tools run (PreToolUse). A shell
// command can still write a reserved file (sed -i, >, mv, Set-Content…);
// wiring PreToolUse to every shell call would cost a hook per command, so
// it is caught right after instead, from the PostToolUse that already runs.
// Nothing is punished: the writer is told, the holder is told, and a task
// to make it right goes on the board, assigned to the writer. The record
// says how the turn started: a turn the user started is the user's mandate
// (the user owns the repo and may override an agent's reservation); a turn
// kept going by peer messages is not.

// shellWrites returns the files a shell command line writes, as far as its
// words show: output redirections and the usual writing commands. It can't
// see writes made inside scripts (python -c, node -e); that is what the
// reservation's PreToolUse on edit tools is for.
func shellWrites(cmd string) []string {
	segs, writes := shellParse(cmd)
	for _, seg := range segs {
		writes = append(writes, segmentWrites(seg)...)
	}
	var out []string
	seen := map[string]bool{}
	for _, w := range writes {
		w = strings.TrimSpace(w)
		switch strings.ToLower(w) {
		case "", "/dev/null", "nul", "$null", "-", "/dev/stdout", "/dev/stderr":
			continue
		}
		if strings.ContainsAny(w, "$`*?\n") || seen[w] {
			continue // variables and globs: not a file we can name
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

func segmentWrites(seg []shellWord) []string {
	if len(seg) == 0 {
		return nil
	}
	prog := strings.ToLower(strings.TrimSuffix(filepath.Base(filepath.ToSlash(seg[0].text)), ".exe"))
	args := seg[1:]
	var plain []string // arguments that aren't options
	opts := map[string]bool{}
	named := map[string]string{} // PowerShell -Name value
	for i := 0; i < len(args); i++ {
		a := args[i].text
		if strings.HasPrefix(a, "-") && !args[i].quoted && a != "-" && a != "--" {
			opts[strings.ToLower(a)] = true
			if (prog == "sed" || prog == "gsed" || prog == "perl") && (a == "-e" || a == "-f" || a == "--expression" || a == "--file") {
				i++ // the script, not a file it edits
				continue
			}
			if strings.HasPrefix(prog, "set-") || strings.Contains(prog, "-") || prog == "sc" || prog == "ac" {
				if i+1 < len(args) {
					named[strings.ToLower(strings.TrimLeft(a, "-"))] = args[i+1].text
					i++
				}
			}
			continue
		}
		if a == "--" {
			continue
		}
		plain = append(plain, a)
	}
	last := func() []string {
		if len(plain) == 0 {
			return nil
		}
		return plain[len(plain)-1:]
	}
	switch prog {
	case "tee", "rm", "unlink", "truncate", "shred":
		return plain
	case "sed", "gsed", "perl":
		if !(opts["-i"] || opts["--in-place"] || anyPrefix(opts, "-i")) {
			return nil
		}
		if len(plain) > 1 && !opts["-e"] {
			return plain[1:] // the first is the script
		}
		return plain
	case "mv":
		return plain // the source changes too: it disappears
	case "cp", "install", "ln":
		return last()
	case "dd":
		for _, a := range plain {
			if strings.HasPrefix(a, "of=") {
				return []string{a[3:]}
			}
		}
	case "git":
		if len(plain) > 1 && (plain[0] == "restore" || plain[0] == "checkout" && hasWord(args, "--")) {
			return plain[1:]
		}
	case "set-content", "add-content", "clear-content", "out-file", "remove-item", "new-item", "sc", "ac", "ri", "del", "erase":
		for _, k := range []string{"path", "literalpath", "filepath"} {
			if v, ok := named[k]; ok {
				return []string{v}
			}
		}
		if len(plain) > 0 {
			return plain[:1]
		}
	case "move-item", "copy-item", "move", "copy", "mi", "cpi":
		if v, ok := named["destination"]; ok {
			return []string{v}
		}
		return last()
	}
	return nil
}

func anyPrefix(opts map[string]bool, p string) bool {
	for o := range opts {
		if strings.HasPrefix(o, p) {
			return true
		}
	}
	return false
}

func hasWord(ws []shellWord, w string) bool {
	for _, x := range ws {
		if x.text == w {
			return true
		}
	}
	return false
}

// trespass checks a finished shell call for writes to files someone else
// holds. It returns what to tell the writer ("" when nothing happened).
func trespass(ctx context.Context, c hub.Client, me core.Agent, in Input) string {
	cmd, _ := in.ToolInput["command"].(string)
	if cmd == "" {
		if parts, ok := in.ToolInput["command"].([]any); ok { // Codex sends argv
			for _, p := range parts {
				if s, ok := p.(string); ok {
					cmd += " " + s
				}
			}
		}
	}
	if cmd == "" || len(EditedFiles(in)) > 0 {
		return ""
	}
	writes := shellWrites(cmd)
	if len(writes) == 0 {
		return ""
	}
	byUser := Holds(me.ID) == 0
	origin := "during a turn the user started (the user's mandate: the user owns the repo)"
	if !byUser {
		origin = "during a turn kept going by peer messages, not by the user"
	}
	dir := in.Cwd
	if filepath.IsAbs(in.ToolDir) {
		dir = in.ToolDir
	} else if in.ToolDir != "" && dir != "" {
		dir = filepath.Join(dir, in.ToolDir)
	}
	done := reported(me.ID)
	var told []string
	for _, w := range writes {
		f := w
		if !filepath.IsAbs(f) && dir != "" {
			f = filepath.Join(dir, f)
		}
		// Reservations hold canonical paths (session.Detect), as in touchFile:
		// on macOS the cwd a harness reports goes through /var -> /private/var.
		f = session.Canonical(f)
		var l *core.Lock
		if err := c.Call(ctx, "check_file", hub.FileParam{Agent: me.ID, File: f}, &l); err != nil || l == nil || l.Agent == core.SystemSender {
			continue
		}
		key := fmt.Sprintf("%d", l.ID)
		if done[key] {
			continue
		}
		done[key] = true
		markReported(me.ID, key)
		rel := relDisplay(me.ProjectPath, f)
		// The hub checks the reservation and keeps the record: an agent
		// can't make one up against someone else.
		var t core.Task
		_ = c.Call(ctx, "trespass", core.TrespassReq{Writer: me.ID, LockID: l.ID, File: f, Program: commandProgram(cmd), ByUser: byUser,
			Transcript: str(in.raw, "transcript_path")}, &t) // where the hub checks whose turn it was
		_ = c.Call(ctx, "send", core.SendReq{From: me.ID, To: l.Agent, Subject: "I wrote to your reserved " + rel,
			Body: fmt.Sprintf("ainterlinked noticed: my shell command (%s) wrote to %s, which you hold, %s. Task #%d is open for me to make it right with you; tell me what you need.",
				commandProgram(cmd), rel, origin, t.ID)}, nil)
		told = append(told, fmt.Sprintf("%s (reserved by %s; task #%d)", rel, l.Agent, t.ID))
	}
	if len(told) == 0 {
		return ""
	}
	return "[ainterlinked] That shell command wrote to a file another agent holds: " + strings.Join(told, ", ") +
		". They have been told. Don't keep writing it: agree with them (restore their version, or hand them your change), then put the task in review with what you did; they close it. Next time queue for it (reserve_files wait=true)" +
		map[bool]string{true: ", or, if the user wants it overridden, say so to them first.", false: "."}[byUser]
}

// Shell writes already reported for an agent (lock ids), one file per agent.
func reportedPath(agent string) string {
	return filepath.Join(config.Home(), "trespass", filepath.Base(agent))
}

func reported(agent string) map[string]bool {
	out := map[string]bool{}
	b, _ := os.ReadFile(reportedPath(agent))
	for _, l := range strings.Fields(string(b)) {
		out[l] = true
	}
	return out
}

func markReported(agent, key string) {
	_ = os.MkdirAll(filepath.Dir(reportedPath(agent)), 0o700)
	f, err := os.OpenFile(reportedPath(agent), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(key + "\n")
}
