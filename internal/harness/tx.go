package harness

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/romanpert/ainterlinked/internal/jsonc"
)

// Tx collects config-file changes and applies them atomically per file, with
// a timestamped backup of every file it modifies. In dry-run mode it only
// prints what would change.
type Tx struct {
	DryRun bool
	Out    io.Writer
	stamp  string
	edits  []edit
	cmds   []command
}

type edit struct {
	path   string
	fn     func(old []byte) ([]byte, error)
	remove bool
	json   bool
	toml   bool
}

// EditTOML queues an edit of a TOML file; the result must still parse.
func (t *Tx) EditTOML(path string, fn func(old []byte) ([]byte, error)) {
	t.edits = append(t.edits, edit{path: path, fn: fn, toml: true})
}

func (e edit) apply(old []byte) ([]byte, error) {
	nw, err := e.fn(old)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", e.path, err)
	}
	if e.json {
		if err := jsonc.Valid(nw); err != nil {
			return nil, fmt.Errorf("%s: edit produced invalid JSON (nothing written): %w", e.path, err)
		}
	}
	if e.toml {
		var v map[string]any
		if _, err := toml.Decode(string(nw), &v); err != nil {
			return nil, fmt.Errorf("%s: edit produced invalid TOML (nothing written): %w", e.path, err)
		}
	}
	return nw, nil
}

type command struct {
	args     []string
	desc     string
	optional bool // failure is fine (e.g. removing something that isn't there)
}

// NewTx starts a transaction.
func NewTx(dry bool, out io.Writer) *Tx {
	return &Tx{DryRun: dry, Out: out, stamp: time.Now().Format("20060102-150405")}
}

// EditJSON queues an edit of a JSON/JSONC file; the result is validated.
func (t *Tx) EditJSON(path string, fn func(old []byte) ([]byte, error)) {
	t.edits = append(t.edits, edit{path: path, fn: fn, json: true})
}

// Edit queues an edit of a text file.
func (t *Tx) Edit(path string, fn func(old []byte) ([]byte, error)) {
	t.edits = append(t.edits, edit{path: path, fn: fn})
}

// Write queues writing a whole file we own.
func (t *Tx) Write(path string, content []byte) {
	t.edits = append(t.edits, edit{path: path, fn: func([]byte) ([]byte, error) { return content, nil }})
}

// Remove queues deleting a file we own.
func (t *Tx) Remove(path string) { t.edits = append(t.edits, edit{path: path, remove: true}) }

// Run queues an external command (e.g. `claude mcp add-json`).
func (t *Tx) Run(desc string, args ...string) {
	t.cmds = append(t.cmds, command{args: args, desc: desc})
}

// RunOptional queues a command whose failure is ignored.
func (t *Tx) RunOptional(desc string, args ...string) {
	t.cmds = append(t.cmds, command{args: args, desc: desc, optional: true})
}

// Change describes one applied (or planned) change.
type Change struct {
	Path   string
	Action string // created, modified, removed, ran, unchanged
	Backup string
}

// Commit applies everything:
//  1. every edit is computed and validated first — a failing edit leaves all
//     files untouched;
//  2. external commands run next — if one fails, no file is written;
//  3. files are written; if a write fails, the files already written in this
//     transaction are restored from memory.
//
// The returned changes are what actually happened, also on error.
func (t *Tx) Commit() ([]Change, error) {
	type planned struct {
		e        edit
		path     string // symlinks resolved: we write the real file
		old, new []byte
		exists   bool
	}
	var plan []planned
	for _, e := range t.edits {
		path := realPath(e.path)
		old, err := os.ReadFile(path)
		exists := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if e.remove {
			if exists {
				plan = append(plan, planned{e: e, path: path, old: old, exists: true})
			}
			continue
		}
		nw, err := e.apply(old)
		if err != nil {
			return nil, err
		}
		nw = matchLineEndings(old, nw)
		if exists && bytes.Equal(old, nw) {
			continue
		}
		plan = append(plan, planned{e: e, path: path, old: old, new: nw, exists: exists})
	}
	if t.DryRun {
		for _, p := range plan {
			t.printDiff(p.e.path, p.old, p.new, p.e.remove)
		}
		for _, c := range t.cmds {
			fmt.Fprintf(t.Out, "  would run: %s\n", strings.Join(c.args, " "))
		}
		return nil, nil
	}
	var changes []Change
	for _, c := range t.cmds {
		out, err := exec.Command(c.args[0], c.args[1:]...).CombinedOutput()
		if err != nil && c.optional {
			continue
		}
		if err != nil {
			return changes, fmt.Errorf("%s: %v: %s (no files were changed)", c.desc, err, strings.TrimSpace(string(out)))
		}
		changes = append(changes, Change{Path: c.desc, Action: "ran"})
	}
	var written []planned
	rollback := func(cause error) ([]Change, error) {
		for i := len(written) - 1; i >= 0; i-- {
			w := written[i]
			if w.exists {
				_ = atomicWrite(w.path, w.old)
			} else {
				_ = os.Remove(w.path)
			}
		}
		return changes, fmt.Errorf("%w (file changes rolled back)", cause)
	}
	for _, p := range plan {
		// Another process (a running Claude session, an editor) may have
		// written the file since we read it: redo the edit on the fresh bytes.
		if !p.e.remove {
			if cur, err := os.ReadFile(p.path); err == nil && !bytes.Equal(cur, p.old) {
				nw, err := p.e.apply(cur)
				if err != nil {
					return rollback(err)
				}
				p.old, p.new, p.exists = cur, matchLineEndings(cur, nw), true
				if bytes.Equal(p.old, p.new) {
					continue
				}
			}
		}
		ch := Change{Path: p.e.path}
		if p.exists {
			ch.Backup = p.path + ".bak-interlinked-" + t.stamp
			if err := os.WriteFile(ch.Backup, p.old, 0o600); err != nil {
				return rollback(fmt.Errorf("backup %s: %w", p.e.path, err))
			}
		}
		if p.e.remove {
			if err := os.Remove(p.path); err != nil {
				return rollback(err)
			}
			ch.Action = "removed"
		} else {
			if err := atomicWrite(p.path, p.new); err != nil {
				return rollback(err)
			}
			ch.Action = "modified"
			if !p.exists {
				ch.Action, ch.Backup = "created", ""
			}
		}
		written = append(written, p)
		changes = append(changes, ch)
	}
	return changes, nil
}

// realPath follows symlinks so a dotfiles-managed config stays a link.
func realPath(p string) string {
	if st, err := os.Lstat(p); err == nil && st.Mode()&os.ModeSymlink != 0 {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
	}
	return p
}

// matchLineEndings makes new content use CRLF throughout when the original did.
func matchLineEndings(old, nw []byte) []byte {
	if !bytes.Contains(old, []byte("\r\n")) {
		return nw
	}
	lf := bytes.ReplaceAll(nw, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(lf, []byte("\n"), []byte("\r\n"))
}

func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".interlinked-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	_ = os.Chmod(tmp.Name(), mode)
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// printDiff prints a line diff of the change in hunks with context.
func (t *Tx) printDiff(path string, old, nw []byte, remove bool) {
	if remove {
		fmt.Fprintf(t.Out, "  --- would remove %s\n", path)
		return
	}
	split := func(b []byte) []string {
		if len(b) == 0 {
			return nil
		}
		s := strings.ReplaceAll(string(b), "\r\n", "\n")
		return strings.Split(strings.TrimRight(s, "\n"), "\n")
	}
	a, b := split(old), split(nw)
	verb := "modify"
	if len(old) == 0 {
		verb = "create"
	}
	fmt.Fprintf(t.Out, "  --- would %s %s\n", verb, path)
	ops := lineDiff(a, b)
	const ctx, max = 2, 80
	printed := 0
	for i := 0; i < len(ops); i++ {
		if ops[i].kind == ' ' {
			continue
		}
		// Start of a hunk: show context before it.
		start := i
		for start > 0 && i-start < ctx && ops[start-1].kind == ' ' {
			start--
		}
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run-end > 2*ctx || run == len(ops) {
				end = min(end+ctx, len(ops))
				break
			}
			end = run
		}
		fmt.Fprintf(t.Out, "    @@ line %d\n", ops[start].line)
		for _, op := range ops[start:end] {
			if printed >= max {
				fmt.Fprintln(t.Out, "      … (diff truncated)")
				return
			}
			fmt.Fprintf(t.Out, "    %c %s\n", op.kind, op.text)
			printed++
		}
		i = end - 1
	}
}

type diffOp struct {
	kind byte // ' ', '-', '+'
	text string
	line int // line number in the new file
}

// lineDiff is an LCS line diff; huge inputs fall back to one replace hunk
// around the common prefix/suffix.
func lineDiff(a, b []string) []diffOp {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	var ops []diffOp
	for i := 0; i < pre; i++ {
		ops = append(ops, diffOp{' ', a[i], i + 1})
	}
	if len(ma)*len(mb) > 4_000_000 {
		for _, l := range ma {
			ops = append(ops, diffOp{'-', l, pre + 1})
		}
		for k, l := range mb {
			ops = append(ops, diffOp{'+', l, pre + k + 1})
		}
	} else {
		n, m := len(ma), len(mb)
		dp := make([][]int32, n+1)
		for i := range dp {
			dp[i] = make([]int32, m+1)
		}
		for i := n - 1; i >= 0; i-- {
			for j := m - 1; j >= 0; j-- {
				if ma[i] == mb[j] {
					dp[i][j] = dp[i+1][j+1] + 1
				} else {
					dp[i][j] = max(dp[i+1][j], dp[i][j+1])
				}
			}
		}
		i, j := 0, 0
		for i < n || j < m {
			switch {
			case i < n && j < m && ma[i] == mb[j]:
				ops = append(ops, diffOp{' ', ma[i], pre + j + 1})
				i, j = i+1, j+1
			case j < m && (i == n || dp[i][j+1] >= dp[i+1][j]):
				ops = append(ops, diffOp{'+', mb[j], pre + j + 1})
				j++
			default:
				ops = append(ops, diffOp{'-', ma[i], pre + j + 1})
				i++
			}
		}
	}
	for k := len(a) - suf; k < len(a); k++ {
		ops = append(ops, diffOp{' ', a[k], len(b) - (len(a) - k) + 1})
	}
	return ops
}
