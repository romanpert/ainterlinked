package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Forgotten tasks. abandon returns a task in progress to todo when its
// holder left; nothing else looks at a todo, review or blocked task nobody
// touches, and boards fill up with them. Lists show how long an open task
// has gone untouched (always: a few characters, and only past a day). The
// user may also ask the hub to say so, once per task and untouched stretch
// ([tasks] stale_after_days, off by default). Nothing is ever cancelled for
// being old: that is the creator's or the human's call.

const dayMs = int64(24 * time.Hour / time.Millisecond)

// DefaultStaleDays is what `ainterlinked tasks stale` looks back on when
// the user set no stale_after_days.
const DefaultStaleDays = 14

// staleStatuses are the open states nobody moves on their own. A task in
// progress is abandon's.
const staleStatuses = `('todo','review','blocked')`

// staleListed caps the tasks one notice lists; the rest are counted.
const staleListed = 20

// SetStaleAfterDays applies [tasks] stale_after_days from now on (0: off).
func (s *Store) SetStaleAfterDays(n int) {
	if n < 0 {
		n = 0
	}
	s.staleAt.Store(int64(n))
}

// StaleAfterDays is the user's setting (0: off).
func (s *Store) StaleAfterDays() int { return int(s.staleAt.Load()) }

// untouchedDays is how many whole days an open task has gone without an
// update.
func (s *Store) untouchedDays(t Task) int {
	if t.Status == "done" || t.Status == "cancelled" {
		return 0
	}
	if d := (s.ms() - t.UpdatedAt) / dayMs; d > 0 {
		return int(d)
	}
	return 0
}

// staleCut is the updated_at at or before which a task has gone days
// untouched.
func (s *Store) staleCut(days int) int64 { return s.ms() - int64(days)*dayMs }

// countStale fills each board's count of tasks untouched past the user's
// setting; nothing when it is off.
func (s *Store) countStale(ctx context.Context, boards []Board) error {
	days := s.StaleAfterDays()
	if days <= 0 || len(boards) == 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT board, count(*) FROM tasks WHERE status IN `+staleStatuses+` AND updated_at<=? GROUP BY board`, s.staleCut(days))
	if err != nil {
		return err
	}
	defer rows.Close()
	n := map[string]int{}
	for rows.Next() {
		var b string
		var c int
		if err := rows.Scan(&b, &c); err != nil {
			return err
		}
		n[b] = c
	}
	for i := range boards {
		if c := n[boards[i].Name]; c > 0 {
			boards[i].Stale, boards[i].StaleDays = c, days
		}
	}
	return rows.Err()
}

// StaleReq asks for the open tasks untouched for Days or more.
type StaleReq struct {
	Board string `json:"board,omitempty"` // "" or "*": every board
	Days  int    `json:"days,omitempty"`  // 0: the user's setting, else DefaultStaleDays
}

// StaleList answers StaleReq, oldest first.
type StaleList struct {
	Days  int    `json:"days"`
	Tasks []Task `json:"tasks"`
}

// StaleTasks lists open tasks (in progress too: the human decides) that
// nobody updated for r.Days, oldest first.
func (s *Store) StaleTasks(ctx context.Context, r StaleReq) (StaleList, error) {
	if err := s.tx(ctx, s.abandon); err != nil {
		return StaleList{}, err
	}
	out := StaleList{Days: r.Days}
	if out.Days <= 0 {
		out.Days = s.StaleAfterDays()
	}
	if out.Days <= 0 {
		out.Days = DefaultStaleDays
	}
	q := `SELECT ` + taskCols + ` FROM tasks WHERE status NOT IN ('done','cancelled') AND updated_at<=?`
	args := []any{s.staleCut(out.Days)}
	if b := strings.TrimSpace(r.Board); b != "" && b != "*" {
		q += ` AND board=?`
		args = append(args, strings.ToLower(b))
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY updated_at, id LIMIT 500`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return out, err
		}
		t.Untouched = s.untouchedDays(t)
		out.Tasks = append(out.Tasks, t)
	}
	return out, rows.Err()
}

// NoticeStaleTasks tells, once per untouched stretch, about each todo,
// review or blocked task nobody updated for the user's stale_after_days:
// its creator while that agent is online, else the human; a review always
// the human (it waits for a person). One message per recipient per call.
// A task stays noticed until it is updated (stale_noticed = its updated_at).
// It returns how many tasks were noticed; nothing at all when the setting
// is off.
func (s *Store) NoticeStaleTasks(ctx context.Context) (int, error) {
	days := s.StaleAfterDays()
	if days <= 0 {
		return 0, nil
	}
	total := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		now := s.ms()
		list, err := staleUnnoticed(tx, s.staleCut(days))
		if err != nil || len(list) == 0 {
			return err
		}
		var human Agent
		err = tx.QueryRow(`SELECT id, name, harness FROM agents WHERE harness='human' ORDER BY last_seen DESC LIMIT 1`).Scan(&human.ID, &human.Name, &human.Harness)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var order []string
		groups := map[string][]Task{}
		who := map[string]Agent{}
		for _, t := range list {
			to, err := staleRecipient(tx, t, human)
			if err != nil {
				return err
			}
			if to.ID == "" {
				continue // no one to tell yet: it waits for the human's first CLI or dashboard use
			}
			if _, ok := groups[to.ID]; !ok {
				order = append(order, to.ID)
				who[to.ID] = to
			}
			t.Untouched = s.untouchedDays(t)
			groups[to.ID] = append(groups[to.ID], t)
		}
		var told []string
		for _, id := range order {
			to, ts := who[id], groups[id]
			// Not direct: news for the next turn, never a reason to hold
			// or wake a session.
			noticeAs(tx, now, to, fmt.Sprintf("%d task(s) untouched for %dd+", len(ts), days), staleBody(ts, days, isHuman(to)), 0)
			for _, t := range ts {
				if _, err := tx.Exec(`UPDATE tasks SET stale_noticed=updated_at WHERE id=?`, t.ID); err != nil {
					return err
				}
			}
			total += len(ts)
			told = append(told, to.Name)
		}
		if total > 0 {
			event(tx, now, nil, "task", fmt.Sprintf("%d task(s) untouched for %dd+: told %s", total, days, strings.Join(told, ", ")))
		}
		return nil
	})
	return total, err
}

// staleUnnoticed lists the todo, review and blocked tasks updated at or
// before cut that have not been noticed since that update, oldest first.
func staleUnnoticed(tx *sql.Tx, cut int64) ([]Task, error) {
	rows, err := tx.Query(`SELECT `+taskCols+` FROM tasks WHERE status IN `+staleStatuses+`
		AND updated_at<=? AND stale_noticed<>updated_at ORDER BY updated_at, id`, cut)
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

// staleRecipient is who hears about t: the creator while it is online,
// else the human; a review always the human. An offline creator would
// never read the notice, the task would count as told, and the unread
// notice would keep Prune from ever dropping the dead agent's row.
func staleRecipient(tx *sql.Tx, t Task, human Agent) (Agent, error) {
	if t.Status == "review" || t.CreatedByID == "" {
		return human, nil
	}
	var a Agent
	err := tx.QueryRow(`SELECT id, name, harness FROM agents WHERE id=? AND online=1`, t.CreatedByID).Scan(&a.ID, &a.Name, &a.Harness)
	if errors.Is(err, sql.ErrNoRows) {
		return human, nil
	}
	return a, err
}

func staleBody(ts []Task, days int, toHuman bool) string {
	var b strings.Builder
	if toHuman {
		fmt.Fprintf(&b, "These open tasks have had no update for %d days or more:\n", days)
	} else {
		fmt.Fprintf(&b, "These tasks you created have had no update for %d days or more:\n", days)
	}
	for i, t := range ts {
		if i == staleListed {
			fmt.Fprintf(&b, "… and %d more\n", len(ts)-i)
			break
		}
		fmt.Fprintf(&b, "#%d [%s] P%d %s · %s · %dd untouched\n", t.ID, t.Status, t.Priority, oneLine(t.Title, 80), t.Board, t.Untouched)
	}
	if toHuman {
		b.WriteString("`ainterlinked tasks stale` lists them; `ainterlinked task cancel <id>` drops one, `ainterlinked task reopen <id>` puts it back in todo.")
	} else {
		b.WriteString("If one is still wanted, update it (update_task); if not, cancel it.")
	}
	b.WriteString(" Any update resets this notice; nothing is cancelled automatically.")
	return b.String()
}
