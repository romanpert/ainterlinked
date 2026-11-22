package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Cede hands the holder's own reservations over to another agent: the
// holder's title, passed on by the holder's choice. Two consents meet here:
// the holder gives, and the receiver has asked for it by queueing for
// those files (reserve_files wait=true). The holder may pick any of the
// agents waiting, not only the first: the title is theirs to give. The
// receiver gets what is left of the term, never more: nobody gives what
// they don't have.
//
// paths are the holder's reservation patterns to hand over; none means
// every reservation of the holder that the receiver waits for.
func (s *Store) Cede(ctx context.Context, agent, to string, paths []string) (int, error) {
	me, err := s.Get(ctx, agent)
	if err != nil {
		return 0, err
	}
	them, err := s.Resolve(ctx, to)
	if err != nil {
		return 0, err
	}
	if them.ID == me.ID {
		return 0, errors.New("those reservations are already yours")
	}
	if !them.Online || them.ProjectPath != me.ProjectPath {
		return 0, fmt.Errorf("%s isn't working in this project now: release_files without to, and the queue decides", them.Name)
	}
	want := map[string]bool{}
	for _, p := range paths {
		pat, err := NormPattern(me.ProjectPath, p)
		if err != nil {
			return 0, err
		}
		want[pat] = true
	}
	var n int
	err = s.tx(ctx, func(tx *sql.Tx) error {
		now := s.ms()
		mine, err := cedable(tx, now, me.ID)
		if err != nil {
			return err
		}
		waits, err := waitedFor(tx, now, them.ID, me.ProjectPath)
		if err != nil {
			return err
		}
		var given []string
		var asked []string
		for _, l := range mine {
			if len(want) > 0 && !want[l.pattern] {
				continue
			}
			wanted := false
			for _, w := range waits {
				wanted = wanted || overlap(w, l.pattern)
			}
			if !wanted {
				asked = append(asked, l.pattern)
				continue
			}
			// The title moves as it is, minus a link to the giver's task.
			if _, err := tx.Exec(`UPDATE locks SET agent_id=?, agent_name=?, task_id=0 WHERE id=?`, them.ID, them.Name, l.id); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM locks WHERE agent_id=? AND pattern=? AND id<>?`, them.ID, l.pattern, l.id); err != nil {
				return err
			}
			given = append(given, fmt.Sprintf("%s (for %s)", l.pattern, time.Duration((l.expires-now)*int64(time.Millisecond)).Round(time.Minute)))
			for _, w := range waits {
				if overlap(w, l.pattern) {
					_, _ = tx.Exec(`DELETE FROM waiters WHERE agent_id=? AND project_path=? AND pattern=?`, them.ID, me.ProjectPath, w)
				}
			}
		}
		if len(given) == 0 {
			switch {
			case len(asked) > 0:
				return fmt.Errorf("%s isn't queued for %s: they can ask with reserve_files wait=true; or release_files without to, and the queue decides",
					them.Name, strings.Join(asked, ", "))
			case len(want) > 0:
				return errors.New("you hold none of those reservations")
			default:
				return errors.New("you hold no reservations")
			}
		}
		n = len(given)
		notice(tx, now, them, "ceded to you: "+strings.Join(given, ", "),
			fmt.Sprintf("%s passed you their reservation of %s. It's yours now: edit, and release_files when done (or to=<name> to pass it on to someone waiting).",
				me.Name, strings.Join(given, ", ")))
		event(tx, now, &me, "lock", fmt.Sprintf("%s ceded %s to %s", me.Name, strings.Join(given, ", "), them.Name))
		return s.promote(tx)
	})
	return n, err
}

type heldLock struct {
	id      int64
	pattern string
	expires int64
}

func cedable(tx *sql.Tx, now int64, agent string) ([]heldLock, error) {
	rows, err := tx.Query(`SELECT id, pattern, expires_at FROM locks WHERE agent_id=? AND expires_at>? ORDER BY id`, agent, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []heldLock
	for rows.Next() {
		var l heldLock
		if err := rows.Scan(&l.id, &l.pattern, &l.expires); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func waitedFor(tx *sql.Tx, now int64, agent, projectPath string) ([]string, error) {
	rows, err := tx.Query(`SELECT pattern FROM waiters WHERE agent_id=? AND project_path=? AND expires_at>?`, agent, projectPath, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
