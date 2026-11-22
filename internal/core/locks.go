package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Lock is a file reservation: "I'm editing these paths, keep off".
type Lock struct {
	ID          int64  `json:"id"`
	ProjectPath string `json:"project_path"`
	Pattern     string `json:"pattern"`
	AgentID     string `json:"agent_id"`
	Agent       string `json:"agent"`
	Reason      string `json:"reason,omitempty"`
	TaskID      int64  `json:"task_id,omitempty"` // released when that task is done or cancelled
	CreatedAt   int64  `json:"created_at"`
	ExpiresAt   int64  `json:"expires_at"`
	// LapsesAt is when it stops applying because its holder's turn ended
	// (see IdleLapse) or it went unused (see UseWindow); 0 when neither applies.
	LapsesAt int64 `json:"lapses_at,omitempty"`
	// UsedAt is the last edit inside it (or when it was made); explicit
	// reservations lapse UseWindow after it.
	UsedAt int64 `json:"used_at,omitempty"`
	// Terms are the conditions the holder accepted by reserving it.
	Terms string `json:"terms,omitempty"`
}

// An explicit reservation is a lease on terms stated when it is made. One
// file is individual use: it keeps its TTL (and the idle lapse). A folder, a
// glob or the whole project fences off what others may need, so it lasts
// while it is used: it lapses after a window without edits inside it, shorter
// the more it covers. Each edit inside it, or reserving it again, renews it;
// its TTL still caps it. Automatic reservations (made by editing) keep AutoLease.
// Starting values: see Limits.
const (
	FolderUse  = 30 * time.Minute // a folder or a glob
	ProjectUse = 10 * time.Minute // the whole project (**)
)

// UseWindow is how long pattern stays reserved without an edit inside it
// (0: a single file, which only its TTL and the idle lapse end).
func (l Limits) UseWindow(pattern string) time.Duration {
	switch {
	case pattern == "**":
		return minutes(l.ProjectUseMinutes)
	case strings.ContainsAny(pattern, "*?["):
		return minutes(l.FolderUseMinutes)
	}
	return 0
}

func (l Limits) leaseTerms(pattern string, expiresAt int64) string {
	until := time.UnixMilli(expiresAt).Format("15:04")
	if w := l.UseWindow(pattern); w > 0 {
		return fmt.Sprintf("covers more than one file, so it lapses after %s without edits in it (editing inside it or reserving again renews it); ends at %s at the latest", w, until)
	}
	if l.IdleLapseMinutes == 0 {
		return "held until " + until
	}
	return fmt.Sprintf("held until %s; it lapses earlier if your turn ends and you stay idle for %s", until, minutes(l.IdleLapseMinutes))
}

// unusedAt is when an explicit reservation lapses for lack of use (0 for
// automatic ones, which renew by editing anyway).
func (t Limits) unusedAt(l Lock) int64 {
	window := t.UseWindow(l.Pattern)
	if l.Reason == autoReason || window == 0 {
		return 0
	}
	used := l.UsedAt
	if used < l.CreatedAt {
		used = l.CreatedAt
	}
	return used + window.Milliseconds()
}

// LockReq reserves one or more path patterns (relative to the project root;
// globs with *, ** and ? are allowed; a trailing / means the whole folder).
type LockReq struct {
	Agent  string   `json:"agent"`
	Paths  []string `json:"paths"`
	Reason string   `json:"reason,omitempty"`
	TTL    int      `json:"ttl_minutes,omitempty"`
	Force  bool     `json:"force,omitempty"` // take it even if another agent holds an overlapping lock
	// Wait queues for paths held by others: they're reserved for this agent
	// (and it is told) as soon as the holder lets go.
	Wait bool `json:"wait,omitempty"`
	// Task links the reservation to a board task: the task is claimed if it
	// is free, and the reservation ends when the task is done or cancelled.
	Task int64 `json:"task,omitempty"`
}

// LockResult reports what was reserved, what conflicted and what was queued.
type LockResult struct {
	Granted   []Lock   `json:"granted"`
	Conflicts []Lock   `json:"conflicts,omitempty"`
	Queued    []Waiter `json:"queued,omitempty"`
	Task      *Task    `json:"task,omitempty"`
}

const lockColsL = `l.id,l.project_path,l.pattern,l.agent_id,l.agent_name,l.reason,l.task_id,l.created_at,l.expires_at,l.used_at`

func scanLock(r scanner) (Lock, error) {
	var l Lock
	err := r.Scan(&l.ID, &l.ProjectPath, &l.Pattern, &l.AgentID, &l.Agent, &l.Reason, &l.TaskID, &l.CreatedAt, &l.ExpiresAt, &l.UsedAt)
	return l, err
}

var (
	errOutside = errors.New("is outside the project")
	errBadPath = errors.New("path contains line breaks or control characters")
)

// NormPattern converts a user path into a project-relative slash pattern.
func NormPattern(projectPath, p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("empty path")
	}
	if hasControl(p) {
		return "", errBadPath
	}
	if len(p) > 4096 {
		return "", errors.New("path too long")
	}
	dir := strings.HasSuffix(p, "/") || strings.HasSuffix(p, `\`)
	p = filepath.ToSlash(p)
	if strings.HasPrefix(p, "/") || (len(p) > 1 && p[1] == ':') {
		root := strings.TrimRight(filepath.ToSlash(projectPath), "/")
		a, b := p, root
		if isWindowsLike(projectPath) {
			a, b = strings.ToLower(a), strings.ToLower(b)
		}
		switch {
		case a == b:
			p = "."
		case strings.HasPrefix(a, b+"/"):
			p = p[len(root)+1:]
		default:
			return "", fmt.Errorf("%s: %w %s", p, errOutside, projectPath)
		}
	}
	p = path.Clean(p)
	p = strings.TrimPrefix(p, "./")
	if strings.HasPrefix(p, "../") || p == ".." {
		return "", fmt.Errorf("%s: %w", p, errOutside)
	}
	if p == "." {
		p = "**"
	} else if dir {
		p += "/**"
	}
	if isWindowsLike(projectPath) {
		p = strings.ToLower(p)
	}
	return p, nil
}

func isWindowsLike(p string) bool { return len(p) > 1 && p[1] == ':' || strings.Contains(p, `\`) }

var globCache sync.Map // pattern -> *regexp.Regexp

func globRE(pat string) *regexp.Regexp {
	if re, ok := globCache.Load(pat); ok {
		return re.(*regexp.Regexp)
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pat); i++ {
		c := pat[i]
		switch {
		case c == '*' && i+1 < len(pat) && pat[i+1] == '*':
			i++
			if i+1 < len(pat) && pat[i+1] == '/' {
				i++
				b.WriteString("(.*/)?")
			} else {
				b.WriteString(".*")
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re := regexp.MustCompile(b.String())
	globCache.Store(pat, re)
	return re
}

// Match reports whether a project-relative file path matches a pattern.
func Match(pattern, file string) bool { return globRE(pattern).MatchString(file) }

func staticPrefix(p string) string {
	if i := strings.IndexAny(p, "*?"); i >= 0 {
		return p[:i]
	}
	return p
}

// overlap is conservative: two patterns conflict if either matches the other
// or one's literal prefix contains the other's.
func overlap(a, b string) bool {
	if a == b || Match(a, b) || Match(b, a) {
		return true
	}
	// Two globs: compare their literal prefixes (may over-report, never under).
	pa, pb := staticPrefix(a), staticPrefix(b)
	if pa != a && pb != b {
		return strings.HasPrefix(pa, pb) || strings.HasPrefix(pb, pa)
	}
	return false
}

func (s *Store) liveLocks(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, projectPath string) ([]Lock, error) {
	// "online" is decided by reap (heartbeat or live harness process); callers
	// reap first so a dead agent's reservations don't count.
	rows, err := q.QueryContext(ctx, `SELECT `+lockColsL+`, a.via, a.state, a.state_since
		FROM locks l JOIN agents a ON a.id=l.agent_id
		WHERE (?='' OR l.project_path=?) AND l.expires_at>? AND a.online=1 ORDER BY l.project_path, l.pattern`,
		projectPath, projectPath, s.ms())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	terms := s.lim()
	var out []Lock
	for rows.Next() {
		var l Lock
		var via, state string
		var since int64
		if err := rows.Scan(&l.ID, &l.ProjectPath, &l.Pattern, &l.AgentID, &l.Agent, &l.Reason, &l.TaskID, &l.CreatedAt, &l.ExpiresAt, &l.UsedAt,
			&via, &state, &since); err != nil {
			return nil, err
		}
		if at := terms.lapses(via, state, since); at > 0 && l.CreatedAt <= since && at < l.ExpiresAt {
			l.LapsesAt = at
		}
		if at := terms.unusedAt(l); at > 0 && at < l.ExpiresAt && (l.LapsesAt == 0 || at < l.LapsesAt) {
			l.LapsesAt = at
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Reserve grants file reservations that don't collide with other agents'.
func (s *Store) Reserve(ctx context.Context, r LockReq) (LockResult, error) {
	me, err := s.Get(ctx, r.Agent)
	if err != nil {
		return LockResult{}, err
	}
	if me.ProjectPath == "" {
		return LockResult{}, errors.New("this agent has no project path; reservations are per project")
	}
	if len(r.Paths) == 0 {
		return LockResult{}, errors.New("paths is required")
	}
	ttl := time.Duration(r.TTL) * time.Minute
	if ttl <= 0 || ttl > 24*time.Hour {
		ttl = 30 * time.Minute
	}
	var res LockResult
	claimed := false
	if r.Task > 0 {
		t, now, err := s.taskForReservation(ctx, me, r.Task)
		if err != nil {
			return LockResult{}, err
		}
		claimed = now
		res.Task = &t
		if strings.TrimSpace(r.Reason) == "" {
			r.Reason = fmt.Sprintf("task #%d: %s", t.ID, t.Title)
		}
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if err := s.settle(tx); err != nil {
			return err
		}
		live, err := s.liveLocks(ctx, tx, me.ProjectPath)
		if err != nil {
			return err
		}
		line, err := s.queued(tx, me.ProjectPath, me.ID)
		if err != nil {
			return err
		}
		now := s.ms()
		for _, raw := range r.Paths {
			pat, err := NormPattern(me.ProjectPath, raw)
			if err != nil {
				return err
			}
			var clash, ahead []Lock
			for _, l := range live {
				if l.AgentID != me.ID && overlap(l.Pattern, pat) {
					clash = append(clash, l)
				}
			}
			// Agents already queued for these files go first: a plain
			// reservation must not jump the line.
			for _, w := range line {
				if overlap(w.Pattern, pat) {
					ahead = append(ahead, Lock{ProjectPath: me.ProjectPath, Pattern: w.Pattern, AgentID: w.AgentID, Agent: w.Agent,
						Reason: "queued before you", ExpiresAt: w.ExpiresAt})
				}
			}
			if len(clash)+len(ahead) > 0 && !r.Force {
				res.Conflicts = append(append(res.Conflicts, clash...), ahead...)
				if r.Wait {
					w, err := s.enqueue(tx, now, me, pat, r.Reason, r.Task, ttl, clash)
					if err != nil {
						return err
					}
					res.Queued = append(res.Queued, w)
				}
				continue
			}
			// Refresh our own identical reservation instead of duplicating it.
			_, _ = tx.Exec(`DELETE FROM locks WHERE agent_id=? AND project_path=? AND pattern=?`, me.ID, me.ProjectPath, pat)
			_, _ = tx.Exec(`DELETE FROM waiters WHERE agent_id=? AND project_path=? AND pattern=?`, me.ID, me.ProjectPath, pat)
			reason := oneLine(r.Reason, 200)
			if reason == autoReason {
				reason = "reserved" // the automatic marker is not for explicit leases
			}
			out, err := tx.Exec(`INSERT INTO locks(project_path,pattern,agent_id,agent_name,reason,task_id,created_at,expires_at,used_at) VALUES(?,?,?,?,?,?,?,?,?)`,
				me.ProjectPath, pat, me.ID, me.Name, reason, r.Task, now, now+ttl.Milliseconds(), now)
			if err != nil {
				return err
			}
			id, _ := out.LastInsertId()
			res.Granted = append(res.Granted, Lock{ID: id, ProjectPath: me.ProjectPath, Pattern: pat, AgentID: me.ID, Agent: me.Name,
				Reason: reason, TaskID: r.Task, CreatedAt: now, ExpiresAt: now + ttl.Milliseconds(), UsedAt: now,
				Terms: s.lim().leaseTerms(pat, now+ttl.Milliseconds())})
		}
		if len(res.Granted) > 0 {
			pats := make([]string, len(res.Granted))
			for i, g := range res.Granted {
				pats[i] = g.Pattern
			}
			event(tx, now, &me, "lock", fmt.Sprintf("%s reserved %s", me.Name, strings.Join(pats, ", ")))
		}
		// A task claimed for files we got none of (and don't wait for) goes
		// back to the board.
		if claimed && len(res.Granted) == 0 && len(res.Queued) == 0 {
			if _, err := tx.Exec(`UPDATE tasks SET status='todo', assignee_id='', assignee='', updated_at=? WHERE id=? AND assignee_id=? AND status='doing'`,
				now, r.Task, me.ID); err != nil {
				return err
			}
			res.Task.Status, res.Task.AssigneeID, res.Task.Assignee = "todo", "", ""
		}
		return nil
	})
	return res, err
}

// Release drops reservations: the given patterns, or all of the agent's when empty.
func (s *Store) Release(ctx context.Context, agent string, paths []string) (int, error) {
	me, err := s.Get(ctx, agent)
	if err != nil {
		return 0, err
	}
	var n int64
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if len(paths) == 0 {
			res, err := tx.Exec(`DELETE FROM locks WHERE agent_id=?`, me.ID)
			if err != nil {
				return err
			}
			n, _ = res.RowsAffected()
		}
		for _, p := range paths {
			pat, err := NormPattern(me.ProjectPath, p)
			if err != nil {
				return err
			}
			res, err := tx.Exec(`DELETE FROM locks WHERE agent_id=? AND pattern=?`, me.ID, pat)
			if err != nil {
				return err
			}
			k, _ := res.RowsAffected()
			n += k
		}
		if n > 0 {
			event(tx, s.ms(), &me, "lock", fmt.Sprintf("%s released %d reservation(s)", me.Name, n))
		}
		// Releasing also leaves the queue for those paths.
		if len(paths) == 0 {
			_, _ = tx.Exec(`DELETE FROM waiters WHERE agent_id=?`, me.ID)
		}
		for _, p := range paths {
			if pat, err := NormPattern(me.ProjectPath, p); err == nil {
				_, _ = tx.Exec(`DELETE FROM waiters WHERE agent_id=? AND pattern=?`, me.ID, pat)
			}
		}
		return s.promote(tx)
	})
	return int(n), err
}

// Locks lists live reservations in a project ("" = every project).
func (s *Store) Locks(ctx context.Context, projectPath string) ([]Lock, error) {
	if err := s.tx(ctx, s.settle); err != nil {
		return nil, err
	}
	return s.liveLocks(ctx, s.db, projectPath) // "" = every project
}

// CheckFile returns the reservation held by another agent that covers file,
// if any. file may be absolute or project-relative.
func (s *Store) CheckFile(ctx context.Context, agent, file string) (*Lock, error) {
	me, err := s.Get(ctx, agent)
	if err != nil {
		return nil, err
	}
	if me.ProjectPath == "" {
		return nil, nil
	}
	rel, err := NormPattern(me.ProjectPath, file)
	switch {
	case errors.Is(err, errOutside):
		return nil, nil // outside the project: not ours to police
	case err != nil:
		// A path we can't evaluate must not slip past a reservation: fail closed.
		return &Lock{Pattern: file, Agent: SystemSender, Reason: "refused: " + err.Error()}, nil
	}
	if err := s.tx(ctx, s.settle); err != nil {
		return nil, err
	}
	live, err := s.liveLocks(ctx, s.db, me.ProjectPath)
	if err != nil {
		return nil, err
	}
	for _, l := range live {
		if l.AgentID != me.ID && Match(l.Pattern, rel) {
			l := l
			return &l, nil
		}
	}
	return nil, nil
}
