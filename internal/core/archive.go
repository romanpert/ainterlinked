package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// ArchiveResult says what was archived and where.
type ArchiveResult struct {
	Count int           `json:"count"`
	Files []ArchiveFile `json:"files,omitempty"`
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

// archive writes the tasks to their files first, synced, and only then
// moves the rows, all inside one write transaction, so nobody changes them
// in between. If the files can't be written nothing moves; if the move
// fails after the write, the tasks stay on the board and a later run writes
// them again: a task may appear twice in a file, never vanish from both.
func (s *Store) archive(ctx context.Context, by *Agent, r ArchiveReq) (ArchiveResult, error) {
	if r.To == "" && r.Dir == "" {
		return ArchiveResult{}, errors.New("no archive folder")
	}
	var res ArchiveResult
	err := s.tx(ctx, func(tx *sql.Tx) error {
		now := s.ms()
		list, err := closedTasks(tx, r, now)
		if err != nil || len(list) == 0 {
			return err
		}
		files := map[string][]Task{}
		for _, t := range list {
			p := r.To
			if p == "" {
				p = filepath.Join(r.Dir, fileSafe(t.Board)+".md")
			}
			files[p] = append(files[p], t)
		}
		paths := make([]string, 0, len(files))
		for p := range files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			if err := appendArchive(p, files[p], now); err != nil {
				return fmt.Errorf("archive %s: %w", p, err)
			}
			f := ArchiveFile{Path: p}
			for _, t := range files[p] {
				f.IDs = append(f.IDs, t.ID)
			}
			res.Files = append(res.Files, f)
		}
		ids := make([]int64, len(list))
		for i, t := range list {
			ids[i] = t.ID
		}
		if err := moveToArchive(tx, ids, now); err != nil {
			return err
		}
		res.Count = len(ids)
		who := "the hub (archive_after_days)"
		if by != nil {
			who = by.Name
		}
		event(tx, now, by, "task", fmt.Sprintf("%s archived %d closed task(s)", who, len(ids)))
		return nil
	})
	if err != nil {
		return ArchiveResult{}, err
	}
	return res, nil
}

func closedTasks(tx *sql.Tx, r ArchiveReq, now int64) ([]Task, error) {
	q := `SELECT ` + taskCols + ` FROM tasks WHERE status IN ('done','cancelled')`
	var args []any
	if b := strings.ToLower(strings.TrimSpace(r.Board)); b != "" && b != "*" {
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
	return b
}

// appendArchive appends tasks to a Markdown file and syncs it to disk.
func appendArchive(path string, list []Task, archivedAt int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
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
		b.WriteString(archiveEntry(t, archivedAt))
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
	fmt.Fprintf(&b, "\n## #%d %s (%s)\n\n", t.ID, t.Title, t.Status)
	fmt.Fprintf(&b, "- board: %s · priority: P%d", t.Board, t.Priority)
	if len(t.Labels) > 0 {
		fmt.Fprintf(&b, " · labels: %s", strings.Join(t.Labels, ", "))
	}
	fmt.Fprintf(&b, "\n- assignee: %s · creator: %s\n", orDash(t.Assignee), orDash(t.CreatedBy))
	fmt.Fprintf(&b, "- created %s · updated %s · archived %s\n", day(t.CreatedAt), day(t.UpdatedAt), day(archivedAt))
	if body := strings.TrimSpace(t.Body); body != "" {
		b.WriteString("\n" + body + "\n")
	}
	if res := strings.TrimSpace(t.Result); res != "" {
		b.WriteString("\nResult: " + res + "\n")
	}
	return b.String()
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
