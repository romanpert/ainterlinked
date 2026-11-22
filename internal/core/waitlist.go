package core

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Waiter is a place in the queue for a reserved path: when the holder
// releases it (or its reservation expires, or it leaves), the hub reserves
// it for the first waiter and tells them, so nobody has to poll or write
// "tell me when you let go of NEXT.md".
type Waiter struct {
	ID          int64    `json:"id"`
	ProjectPath string   `json:"project_path"`
	Pattern     string   `json:"pattern"`
	AgentID     string   `json:"agent_id"`
	Agent       string   `json:"agent,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	TaskID      int64    `json:"task_id,omitempty"`
	Position    int      `json:"position,omitempty"` // 1 = next
	Holders     []string `json:"holders,omitempty"`
	CreatedAt   int64    `json:"created_at"`
	ExpiresAt   int64    `json:"expires_at"`
}

// WaitTTL is how long a queue place lasts, unless the user set Limits.QueueMinutes.
const WaitTTL = 2 * time.Hour

// idleGrantTTL caps a reservation granted from the queue to an agent that
// is idle at that moment: it may not come back soon, and others may wait.
const idleGrantTTL = 10 * time.Minute

// enqueue adds (or refreshes) a's place for pattern and tells the holders
// once. It runs inside the Reserve transaction.
func (s *Store) enqueue(tx *sql.Tx, now int64, a Agent, pattern, reason string, taskID int64, ttl time.Duration, holders []Lock) (Waiter, error) {
	last := minutes(s.lim().QueueMinutes)
	if last == 0 {
		last = maxLimit * time.Minute // while the agent is online: the queue drops the rest
	}
	w := Waiter{ProjectPath: a.ProjectPath, Pattern: pattern, AgentID: a.ID, Agent: a.Name, Reason: reason, TaskID: taskID,
		CreatedAt: now, ExpiresAt: now + last.Milliseconds()}
	err := tx.QueryRow(`SELECT id, created_at FROM waiters WHERE project_path=? AND pattern=? AND agent_id=?`, a.ProjectPath, pattern, a.ID).Scan(&w.ID, &w.CreatedAt)
	fresh := err == sql.ErrNoRows
	switch {
	case fresh:
		res, err := tx.Exec(`INSERT INTO waiters(project_path,pattern,agent_id,reason,task_id,ttl_ms,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?)`,
			a.ProjectPath, pattern, a.ID, oneLine(reason, 200), taskID, ttl.Milliseconds(), now, w.ExpiresAt)
		if err != nil {
			return w, err
		}
		w.ID, _ = res.LastInsertId()
	case err != nil:
		return w, err
	default:
		if _, err := tx.Exec(`UPDATE waiters SET reason=?, task_id=?, ttl_ms=?, expires_at=? WHERE id=?`,
			oneLine(reason, 200), taskID, ttl.Milliseconds(), w.ExpiresAt, w.ID); err != nil {
			return w, err
		}
	}
	ahead, err := s.queued(tx, a.ProjectPath, "")
	if err != nil {
		return w, err
	}
	for _, q := range ahead { // in queue order: count up to our own place
		if overlap(q.Pattern, pattern) {
			w.Position++
		}
		if q.ID == w.ID {
			break
		}
	}
	told := map[string]bool{}
	for _, h := range holders {
		w.Holders = append(w.Holders, h.Agent)
		if !fresh || told[h.AgentID] || recentlyTold(tx, now, h.AgentID, a.Name, h.Pattern) {
			continue
		}
		told[h.AgentID] = true
		holder := Agent{ID: h.AgentID, Name: h.Agent}
		why := ""
		if reason != "" {
			// The waiter's words, quoted and labelled: a notice from the hub
			// must not let a peer speak with the hub's voice.
			why = fmt.Sprintf(" Their stated reason (peer text): %q.", oneLine(reason, 120))
		}
		notice(tx, now, holder, "waiting for "+h.Pattern,
			fmt.Sprintf("%s is queued for %s.%s When you're done with it, release_files [%q]: it passes to them automatically.",
				a.Name, h.Pattern, why, h.Pattern))
	}
	event(tx, now, &a, "lock", fmt.Sprintf("%s queued for %s", a.Name, pattern))
	return w, nil
}

// ownerRank and queueOrder order a queue by the owner's valuation, then by
// arrival. The owner is the only one whose priorities count: a waiter goes
// by the priority of a task the owner (a human) created and that is assigned
// to that very waiter; anyone else by the default priority. So an agent
// can't jump the queue by linking an urgent task that isn't its own work.
const ownerRank = ` LEFT JOIN tasks t ON t.id=w.task_id AND w.task_id>0 AND t.assignee_id=w.agent_id
		AND t.created_by_id IN (SELECT id FROM agents WHERE harness='human')`
const queueOrder = ` ORDER BY COALESCE(t.priority, 2), w.id`

// queued lists live queue places in a project in queue order, except those
// of agent exceptID.
func (s *Store) queued(tx *sql.Tx, projectPath, exceptID string) ([]Waiter, error) {
	rows, err := tx.Query(`SELECT w.id,w.pattern,w.agent_id,a.name,w.reason,w.expires_at FROM waiters w JOIN agents a ON a.id=w.agent_id`+ownerRank+`
		WHERE w.project_path=? AND w.expires_at>? AND a.online=1 AND w.agent_id<>?`+queueOrder, projectPath, s.ms(), exceptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Waiter
	for rows.Next() {
		w := Waiter{ProjectPath: projectPath}
		if err := rows.Scan(&w.ID, &w.Pattern, &w.AgentID, &w.Agent, &w.Reason, &w.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// noticeEvery limits "X is waiting for your file" notices per waiter, file
// and holder, so queueing and leaving in a loop can't spam a holder.
const noticeEvery = 10 * time.Minute

func recentlyTold(tx *sql.Tx, now int64, holderID, waiter, pattern string) bool {
	var n int
	err := tx.QueryRow(`SELECT count(*) FROM messages m JOIN receipts r ON r.message_id=m.id
		WHERE m.sender_id=? AND r.agent_id=? AND m.subject=? AND m.created_at>? AND instr(m.body, ?)=1`,
		SystemSender, holderID, oneLine("waiting for "+pattern, 200), now-noticeEvery.Milliseconds(), waiter+" ").Scan(&n)
	return err != nil || n > 0
}

// settle brings presence and the queue up to date: reap silent agents, end
// the reservations of agents idle too long and those gone unused (two terms,
// each the user's to turn off), then hand released or expired reservations
// to whoever waits for them.
func (s *Store) settle(tx *sql.Tx) error {
	if err := s.reap(tx); err != nil {
		return err
	}
	if err := s.lapse(tx); err != nil {
		return err
	}
	if err := s.lapseUnused(tx); err != nil {
		return err
	}
	return s.promote(tx)
}

// promote grants queued reservations that no longer collide, in queue order
// (the owner's priority, then first come): a waiter never jumps one ahead
// of it on the same files.
func (s *Store) promote(tx *sql.Tx) error {
	now := s.ms()
	if _, err := tx.Exec(`DELETE FROM waiters WHERE expires_at<=? OR agent_id NOT IN (SELECT id FROM agents WHERE online=1)`, now); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT w.id,w.project_path,w.pattern,w.agent_id,a.name,a.state,w.reason,w.task_id,w.ttl_ms
		FROM waiters w JOIN agents a ON a.id=w.agent_id` + ownerRank + queueOrder)
	if err != nil {
		return err
	}
	type queued struct {
		Waiter
		state string
		ttl   int64
	}
	var list []queued
	for rows.Next() {
		var q queued
		if err := rows.Scan(&q.ID, &q.ProjectPath, &q.Pattern, &q.AgentID, &q.Agent, &q.state, &q.Reason, &q.TaskID, &q.ttl); err != nil {
			rows.Close()
			return err
		}
		list = append(list, q)
	}
	rows.Close()
	if len(list) == 0 {
		return nil
	}
	live := map[string][]Lock{}
	var still []queued
	for _, q := range list {
		locks, ok := live[q.ProjectPath]
		if !ok {
			if locks, err = s.liveLocks(context.Background(), tx, q.ProjectPath); err != nil {
				return err
			}
			live[q.ProjectPath] = locks
		}
		blocked := false
		for _, l := range locks {
			if l.AgentID != q.AgentID && overlap(l.Pattern, q.Pattern) {
				blocked = true
				break
			}
		}
		for _, e := range still {
			if e.ProjectPath == q.ProjectPath && e.AgentID != q.AgentID && overlap(e.Pattern, q.Pattern) {
				blocked = true // fairness: an earlier waiter is first
				break
			}
		}
		if blocked {
			still = append(still, q)
			continue
		}
		ttl := q.ttl
		if most := minutes(s.lim().IdleGrantMinutes).Milliseconds(); most > 0 && (q.state == "idle" || q.state == "done") && ttl > most {
			ttl = most
		}
		_, _ = tx.Exec(`DELETE FROM locks WHERE agent_id=? AND project_path=? AND pattern=?`, q.AgentID, q.ProjectPath, q.Pattern)
		res, err := tx.Exec(`INSERT INTO locks(project_path,pattern,agent_id,agent_name,reason,task_id,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?)`,
			q.ProjectPath, q.Pattern, q.AgentID, q.Agent, q.Reason, q.TaskID, now, now+ttl)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		if _, err := tx.Exec(`DELETE FROM waiters WHERE id=?`, q.ID); err != nil {
			return err
		}
		live[q.ProjectPath] = append(locks, Lock{ID: id, ProjectPath: q.ProjectPath, Pattern: q.Pattern, AgentID: q.AgentID, Agent: q.Agent,
			Reason: q.Reason, TaskID: q.TaskID, CreatedAt: now, ExpiresAt: now + ttl})
		who := Agent{ID: q.AgentID, Name: q.Agent}
		notice(tx, now, who, "reserved for you: "+q.Pattern,
			fmt.Sprintf("Your queued reservation is granted: %s is now reserved for you for %s. Edit it now, and release_files when done.",
				q.Pattern, time.Duration(ttl*int64(time.Millisecond)).Round(time.Minute)))
		event(tx, now, &who, "lock", fmt.Sprintf("%s got %s from the queue", q.Agent, q.Pattern))
	}
	return nil
}

// Waiters lists the queue of a project ("" = every project).
func (s *Store) Waiters(ctx context.Context, projectPath string) ([]Waiter, error) {
	q := `SELECT w.id,w.project_path,w.pattern,w.agent_id,a.name,w.reason,w.task_id,w.created_at,w.expires_at
		FROM waiters w JOIN agents a ON a.id=w.agent_id` + ownerRank + ` WHERE w.expires_at>? AND a.online=1`
	args := []any{s.ms()}
	if projectPath != "" {
		q += ` AND w.project_path=?`
		args = append(args, projectPath)
	}
	rows, err := s.db.QueryContext(ctx, q+queueOrder, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Waiter
	for rows.Next() {
		var w Waiter
		if err := rows.Scan(&w.ID, &w.ProjectPath, &w.Pattern, &w.AgentID, &w.Agent, &w.Reason, &w.TaskID, &w.CreatedAt, &w.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
