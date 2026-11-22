package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// A trespass is a write to a file another agent holds (internal/hooks finds
// shell writes after the fact). The record is a task, owed by the writer to
// the holder: the holder is its creator, the writer its assignee. Only this
// path makes those tasks; create_task refuses their labels, so nobody can
// pin a trespass on someone else, and the facts (AgentFacts) can trust them.

// TrespassReq reports a write by Writer to File, under the reservation
// LockID that someone else holds.
type TrespassReq struct {
	Writer  string `json:"writer"`
	LockID  int64  `json:"lock_id"`
	File    string `json:"file"`
	Program string `json:"program,omitempty"` // the program only, never its arguments
	// ByUser: the turn was started by the user (the user's order, the
	// user's act); otherwise peer messages kept it going.
	ByUser bool `json:"by_user,omitempty"`
	// Transcript is where the hook says the harness's record of the session
	// is (its transcript_path): a hint only. The hub checks a ByUser claim
	// in the session's file under the harness's own directory, wherever
	// this points.
	Transcript string `json:"transcript,omitempty"`
}

// reservedLabels are set by the hub only.
var reservedLabels = map[string]bool{LabelRestitution: true, LabelReconcile: true}

func checkLabels(labels []string) error {
	for _, l := range cleanLabels(labels) {
		if reservedLabels[strings.ToLower(l)] {
			return fmt.Errorf("the label %q is the hub's own: it marks a write to someone else's reserved file, which only the hub records", l)
		}
	}
	return nil
}

// RecordTrespass checks the reservation is real and someone else's, and
// opens the task that makes it right.
func (s *Store) RecordTrespass(ctx context.Context, r TrespassReq) (Task, error) {
	me, err := s.Get(ctx, r.Writer)
	if err != nil {
		return Task{}, err
	}
	file, err := NormPattern(me.ProjectPath, r.File)
	if err != nil {
		return Task{}, err
	}
	// Whose act it was, decided before the write lock: it may read a file.
	byUser := r.ByUser && s.userTurn(ctx, me, r.Transcript)
	var out Task
	err = s.tx(ctx, func(tx *sql.Tx) error {
		now := s.ms()
		var holderID, holder, reason, project, pattern string
		err := tx.QueryRow(`SELECT agent_id, agent_name, reason, project_path, pattern FROM locks WHERE id=? AND expires_at>?`, r.LockID, now).
			Scan(&holderID, &holder, &reason, &project, &pattern)
		if err == sql.ErrNoRows {
			return errors.New("no such reservation in force")
		}
		if err != nil {
			return err
		}
		if holderID == me.ID || project != me.ProjectPath || !overlap(pattern, file) {
			return errors.New("that reservation doesn't cover a file of someone else's in your project")
		}
		label, title := LabelRestitution, fmt.Sprintf("Make %s right with %s after a shell write", file, holder)
		origin := "during a turn kept going by peer messages, not by the user"
		if byUser {
			label, title = LabelReconcile, fmt.Sprintf("Reconcile %s with %s: written from the shell on the user's turn", file, holder)
			origin = "during a turn the user started (the user's mandate: the user owns the repo)"
		}
		prog := r.Program
		if prog == "" {
			prog = "a command"
		}
		body := fmt.Sprintf("%s wrote to %s (reserved by %s, reason %q) from the shell (%s), %s. Agree with %s on what stays: restore their version, or hand them the change.",
			me.Name, file, holder, orNone(reason), oneLine(prog, 40), origin, holder)
		if !byUser {
			body += fmt.Sprintf(" When done, set it to review with what you did: %s (or the human) closes it.", holder)
		}
		res, err := tx.Exec(`INSERT INTO tasks(board,title,body,status,priority,assignee_id,assignee,created_by,created_by_id,depends_on,labels,created_at,updated_at)
			VALUES(?,?,?,'todo',1,?,?,?,?,'',?,?,?)`, s.boardFor(me, ""), oneLine(title, 200), body, me.ID, me.Name, holder, holderID, label, now, now)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		event(tx, now, &me, "task", fmt.Sprintf("%s wrote to %s, held by %s: #%d %s", me.Name, file, holder, id, label))
		out, err = scanTask(tx.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id=?`, id))
		return err
	})
	return out, err
}

// settleRule: a trespass record names the writer the hub saw, so nobody but
// the human moves it to someone else (not the writer, not the holder: that
// would pin it on a third agent). A restitution is owed to the holder, so
// the holder (its creator) or the human settles it; the writer says what
// it did by moving it to review.
func settleRule(t Task, by Agent, status string, reassign bool) error {
	trespass := contains(t.Labels, LabelRestitution) || contains(t.Labels, LabelReconcile)
	if !trespass || isHuman(by) {
		return nil
	}
	if reassign {
		return fmt.Errorf("task #%d records a write by %s that the hub saw: only the human can reassign it", t.ID, t.Assignee)
	}
	if !contains(t.Labels, LabelRestitution) || by.ID == t.CreatedByID {
		return nil
	}
	if status == "done" || status == "cancelled" {
		return fmt.Errorf("task #%d is a restitution owed to %s: they (or the human) settle it. Set it to review with what you did (result), and they close it",
			t.ID, t.CreatedBy)
	}
	return nil
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "none given"
	}
	return s
}
