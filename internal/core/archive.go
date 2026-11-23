package core

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Closed tasks pile up on a board and every list_tasks puts them in some
// model's context. Archiving moves them off the board: their text goes to a
// Markdown file the user keeps, their rows to archived_tasks, where search
// still finds them and facts still count them. Only the user archives (the
// CLI), or the hub on the user's [tasks] archive_after_days, off by default.

// everyTask is every task the bus has had, on a board or archived, with the
// columns facts read.
const everyTask = `(SELECT assignee, status, labels, created_at, updated_at FROM tasks
	UNION ALL SELECT assignee, status, labels, created_at, updated_at FROM archived_tasks)`

// foundTasks is every task with taskCols and archived_at (0 on a board).
const foundTasks = `(SELECT ` + taskCols + `, 0 AS archived_at FROM tasks
	UNION ALL SELECT ` + taskCols + `, archived_at FROM archived_tasks)`

// ArchiveReq archives the closed tasks of a board (every board by default).
type ArchiveReq struct {
	By    string `json:"by"`
	Board string `json:"board,omitempty"` // "" or "*": every board
	// OlderThan, in milliseconds, keeps tasks updated more recently on the board.
	OlderThan int64 `json:"older_than_ms,omitempty"`
	// To is one Markdown file for all of them; default <Dir>/<board>.md.
	To string `json:"to,omitempty"`
	// Dir is the archive folder, set by the hub (<home>/archive), never by a caller.
	Dir string `json:"-"`
}

// ArchiveFile is what one archive run appended to one file.
type ArchiveFile struct {
	Path string  `json:"path"`
	IDs  []int64 `json:"ids"`
}

// ArchiveFailure is a file that couldn't be written: its tasks stayed on
// their board.
type ArchiveFailure struct {
	Path  string  `json:"path"`
	IDs   []int64 `json:"ids"`
	Error string  `json:"error"`
}

// ArchiveResult says what was archived and where, and which files failed.
type ArchiveResult struct {
	Count  int              `json:"count"`
	Files  []ArchiveFile    `json:"files,omitempty"`
	Failed []ArchiveFailure `json:"failed,omitempty"`
}

// ArchiveTasks archives closed tasks for the human; agents can't.
func (s *Store) ArchiveTasks(ctx context.Context, r ArchiveReq) (ArchiveResult, error) {
	by, err := s.Get(ctx, r.By)
	if err != nil {
		return ArchiveResult{}, err
	}
	if !isHuman(by) {
		return ArchiveResult{}, errors.New("only the user archives tasks (ainterlinked tasks archive)")
	}
	if r.OlderThan < 0 {
		return ArchiveResult{}, errors.New("older_than can't be negative")
	}
	return s.archive(ctx, &by, r)
}

// AutoArchive archives every board's tasks closed longer ago than after,
// into dir: what the hub does when the user set [tasks] archive_after_days.
func (s *Store) AutoArchive(ctx context.Context, after time.Duration, dir string) (ArchiveResult, error) {
	if after <= 0 {
		return ArchiveResult{}, nil
	}
	return s.archive(ctx, nil, ArchiveReq{OlderThan: after.Milliseconds(), Dir: dir})
}

// archive archives file by file, each in its own write transaction: the
// file's tasks are appended to it and synced, then their rows moved, so
// nobody changes them in between. A file that can't be written keeps only
// its own tasks on their boards (res.Failed), tried again next run; the
// other files go ahead. If a move fails after its write, the tasks stay on
// the board and the next run finds them in the file already (appendArchive
// skips them): a task is never written twice, nor lost from both. It is an
// error only when files failed and nothing was archived.
func (s *Store) archive(ctx context.Context, by *Agent, r ArchiveReq) (ArchiveResult, error) {
	if r.To == "" && r.Dir == "" {
		return ArchiveResult{}, errors.New("no archive folder")
	}
	files := map[string][]string{} // path → its boards (none with To: r's scope)
	err := s.tx(ctx, func(tx *sql.Tx) error {
		list, err := closedTasks(tx, r, nil, s.ms())
		for _, t := range list {
			p := r.To
			if p == "" {
				p = filepath.Join(r.Dir, fileSafe(t.Board)+".md")
				if boards := files[p]; len(boards) > 0 && boards[len(boards)-1] == t.Board {
					continue // sorted by board
				}
			}
			files[p] = append(files[p], t.Board)
		}
		return err
	})
	if err != nil {
		return ArchiveResult{}, err
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var res ArchiveResult
	var failed []error
	for _, p := range paths {
		var boards []string
		if r.To == "" {
			boards = files[p]
		}
		f, err := s.archiveFile(ctx, by, r, p, boards)
		if err != nil {
			res.Failed = append(res.Failed, ArchiveFailure{Path: p, IDs: f.IDs, Error: err.Error()})
			failed = append(failed, fmt.Errorf("archive %s: %w", p, err))
		} else if len(f.IDs) > 0 {
			res.Files = append(res.Files, f)
			res.Count += len(f.IDs)
		}
	}
	if res.Count == 0 && len(failed) > 0 {
		return res, errors.Join(failed...)
	}
	return res, nil
}

// archiveFile archives into path the closed tasks of boards (r's scope when
// none) in one transaction. Its IDs are the tasks it tried, on error too.
func (s *Store) archiveFile(ctx context.Context, by *Agent, r ArchiveReq, path string, boards []string) (ArchiveFile, error) {
	f := ArchiveFile{Path: path}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		now := s.ms()
		list, err := closedTasks(tx, r, boards, now)
		if err != nil || len(list) == 0 {
			return err
		}
		for _, t := range list {
			f.IDs = append(f.IDs, t.ID)
		}
		if err := appendArchive(path, list, now); err != nil {
			return err
		}
		if err := moveToArchive(tx, f.IDs, now); err != nil {
			return err
		}
		who := "the hub (archive_after_days)"
		if by != nil {
			who = by.Name
		}
		event(tx, now, by, "task", fmt.Sprintf("%s archived %d closed task(s) to %s", who, len(f.IDs), filepath.Base(path)))
		return nil
	})
	return f, err
}

// closedTasks lists the closed tasks r archives, on boards when given.
func closedTasks(tx *sql.Tx, r ArchiveReq, boards []string, now int64) ([]Task, error) {
	q := `SELECT ` + taskCols + ` FROM tasks WHERE status IN ('done','cancelled')`
	var args []any
	if len(boards) > 0 {
		q += ` AND board IN (` + strings.TrimSuffix(strings.Repeat("?,", len(boards)), ",") + `)`
		for _, b := range boards {
			args = append(args, b)
		}
	} else if b := strings.ToLower(strings.TrimSpace(r.Board)); b != "" && b != "*" {
		q += ` AND board=?`
		args = append(args, b)
	}
	if r.OlderThan > 0 {
		q += ` AND updated_at<?`
		args = append(args, now-r.OlderThan)
	}
	rows, err := tx.Query(q+` ORDER BY board, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// moveToArchive moves tasks to archived_tasks. What only matters while a
// task is on a board goes with it: who wants it, queue places for it.
// Reservations linked to it were let go when it was closed (UpdateTask);
// others' reservations are theirs and keep their own term.
func moveToArchive(tx *sql.Tx, ids []int64, now int64) error {
	for len(ids) > 0 {
		n := min(len(ids), 400)
		chunk := ids[:n]
		ids = ids[n:]
		in := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO archived_tasks(` + taskCols + `,archived_at) SELECT ` + taskCols + `,? FROM tasks WHERE id IN (` + in + `)`, append([]any{now}, args...)},
			{`DELETE FROM tasks WHERE id IN (` + in + `)`, args},
			{`DELETE FROM task_wants WHERE task_id IN (` + in + `)`, args},
			{`DELETE FROM waiters WHERE task_id IN (` + in + `)`, args},
		} {
			if _, err := tx.Exec(q.sql, q.args...); err != nil {
				return err
			}
		}
	}
	return nil
}

// deviceNames are Windows' reserved file names: "con.md" opens the console,
// not a file. Kept from board names on every OS, so a board has one file
// name everywhere.
var deviceNames = func() map[string]bool {
	m := map[string]bool{"con": true, "prn": true, "aux": true, "nul": true}
	for i := '0'; i <= '9'; i++ {
		m["com"+string(i)], m["lpt"+string(i)] = true, true
	}
	return m
}()

// fileSafe turns a board name into a file name.
func fileSafe(board string) string {
	b := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '-'
	}, board)
	if b = strings.Trim(b, "-."); b == "" {
		return "default"
	}
	if stem, _, _ := strings.Cut(b, "."); deviceNames[strings.ToLower(stem)] {
		return "_" + b
	}
	return b
}

// entryHeading is the line an archived task's entry starts with.
func entryHeading(t Task) string {
	return fmt.Sprintf("## #%d %s (%s)", t.ID, oneLine(t.Title, len(t.Title)), t.Status)
}

// headingsIn lists the entry headings already in an archive file.
func headingsIn(path string) (map[string]bool, error) {
	have := map[string]bool{}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return have, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rd := bufio.NewReader(f)
	for {
		line, err := rd.ReadString('\n')
		if strings.HasPrefix(line, "## #") {
			have[strings.TrimRight(line, "\r\n")] = true
		}
		if errors.Is(err, io.EOF) {
			return have, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// appendArchive appends tasks to a Markdown file and syncs it to disk,
// skipping those already in it (written by a run whose move then failed).
func appendArchive(path string, list []Task, archivedAt int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	have, err := headingsIn(path)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	var b strings.Builder
	if fi, err := f.Stat(); err == nil && fi.Size() == 0 {
		b.WriteString("# Archived tasks\n")
	}
	for _, t := range list {
		if !have[entryHeading(t)] {
			b.WriteString(archiveEntry(t, archivedAt))
		}
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func archiveEntry(t Task, archivedAt int64) string {
	day := func(ms int64) string { return time.UnixMilli(ms).Format("2006-01-02 15:04") }
	var b strings.Builder
	b.WriteString("\n" + entryHeading(t) + "\n\n")
	fmt.Fprintf(&b, "- board: %s · priority: P%d", t.Board, t.Priority)
	if len(t.Labels) > 0 {
		fmt.Fprintf(&b, " · labels: %s", strings.Join(t.Labels, ", "))
	}
	fmt.Fprintf(&b, "\n- assignee: %s · creator: %s\n", orDash(t.Assignee), orDash(t.CreatedBy))
	fmt.Fprintf(&b, "- created %s · updated %s · archived %s\n", day(t.CreatedAt), day(t.UpdatedAt), day(archivedAt))
	if body := strings.TrimSpace(t.Body); body != "" {
		b.WriteString("\n" + quoted(body) + "\n")
	}
	if res := strings.TrimSpace(t.Result); res != "" {
		b.WriteString("\nResult:\n\n" + quoted(res) + "\n")
	}
	return b.String()
}

// quoted writes a task's own text as a Markdown quote: no line of it can
// start like an entry's heading, which is what tells a written task.
func quoted(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight("> "+l, " ")
	}
	return strings.Join(lines, "\n")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// rowQuerier is a *sql.DB or a *sql.Tx.
type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// noTask says why task id isn't on a board: archived (search still finds
// it, read-only) or never there.
func noTask(q rowQuerier, id int64) error {
	var status string
	var at int64
	if q.QueryRow(`SELECT status, archived_at FROM archived_tasks WHERE id=?`, id).Scan(&status, &at) == nil {
		return fmt.Errorf("task #%d is archived (%s, archived %s): it is read-only; list_tasks query=\"#%d\" shows it",
			id, status, time.UnixMilli(at).Format("2006-01-02"), id)
	}
	return fmt.Errorf("no task #%d", id)
}
