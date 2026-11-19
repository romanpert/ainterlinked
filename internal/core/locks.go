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
	CreatedAt   int64  `json:"created_at"`
	ExpiresAt   int64  `json:"expires_at"`
}

// LockReq reserves one or more path patterns (relative to the project root;
// globs with *, ** and ? are allowed; a trailing / means the whole folder).
type LockReq struct {
	Agent  string   `json:"agent"`
	Paths  []string `json:"paths"`
	Reason string   `json:"reason,omitempty"`
	TTL    int      `json:"ttl_minutes,omitempty"`
	Force  bool     `json:"force,omitempty"` // take it even if another agent holds an overlapping lock
}

// LockResult reports what was reserved and what conflicted.
type LockResult struct {
	Granted   []Lock `json:"granted"`
	Conflicts []Lock `json:"conflicts,omitempty"`
}

const lockCols = `id,project_path,pattern,agent_id,agent_name,reason,created_at,expires_at`

func scanLock(r scanner) (Lock, error) {
	var l Lock
	err := r.Scan(&l.ID, &l.ProjectPath, &l.Pattern, &l.AgentID, &l.Agent, &l.Reason, &l.CreatedAt, &l.ExpiresAt)
	return l, err
}

// NormPattern converts a user path into a project-relative slash pattern.
func NormPattern(projectPath, p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("empty path")
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
			return "", fmt.Errorf("%s is outside the project %s", p, projectPath)
		}
	}
	p = path.Clean(p)
	p = strings.TrimPrefix(p, "./")
	if strings.HasPrefix(p, "../") || p == ".." {
		return "", fmt.Errorf("%s is outside the project", p)
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
	rows, err := q.QueryContext(ctx, `SELECT l.id,l.project_path,l.pattern,l.agent_id,l.agent_name,l.reason,l.created_at,l.expires_at
		FROM locks l JOIN agents a ON a.id=l.agent_id
		WHERE l.project_path=? AND l.expires_at>? AND a.online=1 AND a.last_seen>?`,
		projectPath, s.ms(), s.ms()-s.stale.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Lock
	for rows.Next() {
		l, err := scanLock(rows)
		if err != nil {
			return nil, err
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
	err = s.tx(ctx, func(tx *sql.Tx) error {
		live, err := s.liveLocks(ctx, tx, me.ProjectPath)
		if err != nil {
			return err
		}
		now := s.ms()
		for _, raw := range r.Paths {
			pat, err := NormPattern(me.ProjectPath, raw)
			if err != nil {
				return err
			}
			var clash []Lock
			for _, l := range live {
				if l.AgentID != me.ID && overlap(l.Pattern, pat) {
					clash = append(clash, l)
				}
			}
			if len(clash) > 0 && !r.Force {
				res.Conflicts = append(res.Conflicts, clash...)
				continue
			}
			// Refresh our own identical reservation instead of duplicating it.
			_, _ = tx.Exec(`DELETE FROM locks WHERE agent_id=? AND project_path=? AND pattern=?`, me.ID, me.ProjectPath, pat)
			out, err := tx.Exec(`INSERT INTO locks(project_path,pattern,agent_id,agent_name,reason,created_at,expires_at) VALUES(?,?,?,?,?,?,?)`,
				me.ProjectPath, pat, me.ID, me.Name, oneLine(r.Reason, 200), now, now+ttl.Milliseconds())
			if err != nil {
				return err
			}
			id, _ := out.LastInsertId()
			res.Granted = append(res.Granted, Lock{ID: id, ProjectPath: me.ProjectPath, Pattern: pat, AgentID: me.ID, Agent: me.Name,
				Reason: r.Reason, CreatedAt: now, ExpiresAt: now + ttl.Milliseconds()})
		}
		if len(res.Granted) > 0 {
			pats := make([]string, len(res.Granted))
			for i, g := range res.Granted {
				pats[i] = g.Pattern
			}
			event(tx, now, &me, "lock", fmt.Sprintf("%s reserved %s", me.Name, strings.Join(pats, ", ")))
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
		return nil
	})
	return int(n), err
}

// Locks lists live reservations in a project ("" = every project).
func (s *Store) Locks(ctx context.Context, projectPath string) ([]Lock, error) {
	if projectPath != "" {
		return s.liveLocks(ctx, s.db, projectPath)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT l.id,l.project_path,l.pattern,l.agent_id,l.agent_name,l.reason,l.created_at,l.expires_at
		FROM locks l JOIN agents a ON a.id=l.agent_id WHERE l.expires_at>? AND a.online=1 ORDER BY l.project_path, l.pattern`, s.ms())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Lock
	for rows.Next() {
		l, err := scanLock(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
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
	if err != nil {
		return nil, nil // outside the project: not ours to police
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
