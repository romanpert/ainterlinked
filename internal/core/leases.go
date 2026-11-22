package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Reservations follow the work, not the intent. Seen in real use: agents
// reserved files for 2-3 hours up front (one held 18 files), a quarter were
// never released, and an agent waiting for its user held files for hours.
//
//   - Editing a file reserves it for AutoLease, renewed by each edit
//     (TouchFile, from the PreToolUse hook): no reserve_files needed for the
//     common case, and a clash shows up exactly when two agents edit the same file.
//   - A hook-driven agent's reservations lapse IdleLapse after its turn ended
//     (idle/done): forgotten reservations clear themselves in minutes.
//     Agents without hooks don't report turns, so their TTL alone applies.
//
// Starting values: see Limits.
const (
	AutoLease = 15 * time.Minute
	IdleLapse = 15 * time.Minute
)

// autoReason marks reservations made by editing.
const autoReason = "editing (auto-reserved)"

// lapses reports when a's reservations made before its current idle spell
// stop applying (0: they don't lapse).
func (l Limits) lapses(via, state string, since int64) int64 {
	if l.IdleLapseMinutes == 0 || !strings.Contains(via, "hook") || since <= 0 || (state != "idle" && state != "done") {
		return 0
	}
	return since + minutes(l.IdleLapseMinutes).Milliseconds()
}

// lapse ends the reservations of agents idle for longer than IdleLapse and
// tells them once. Reservations granted while idle (from the queue, which
// caps them at idleGrantTTL) keep their own term.
func (s *Store) lapse(tx *sql.Tx) error {
	after := minutes(s.lim().IdleLapseMinutes)
	if after == 0 {
		return nil
	}
	now := s.ms()
	rows, err := tx.Query(`SELECT l.id, l.pattern, a.id, a.name FROM locks l JOIN agents a ON a.id=l.agent_id
		WHERE l.expires_at>? AND a.online=1 AND a.state IN ('idle','done') AND instr(a.via,'hook')>0
		AND a.state_since>0 AND a.state_since<? AND l.created_at<=a.state_since`, now, now-after.Milliseconds())
	if err != nil {
		return err
	}
	type gone struct {
		id      int64
		pattern string
	}
	byAgent := map[string][]gone{}
	names := map[string]string{}
	for rows.Next() {
		var g gone
		var aid, aname string
		if err := rows.Scan(&g.id, &g.pattern, &aid, &aname); err != nil {
			rows.Close()
			return err
		}
		byAgent[aid] = append(byAgent[aid], g)
		names[aid] = aname
	}
	rows.Close()
	for aid, list := range byAgent {
		pats := make([]string, 0, len(list))
		for _, g := range list {
			if _, err := tx.Exec(`UPDATE locks SET expires_at=? WHERE id=?`, now, g.id); err != nil {
				return err
			}
			pats = append(pats, g.pattern)
		}
		who := Agent{ID: aid, Name: names[aid]}
		notice(tx, now, who, "reservations lapsed",
			fmt.Sprintf("Your reservation(s) of %s lapsed: your turn ended over %s ago, so others may edit them now. Editing a file reserves it again; reserve_files does too.",
				clip(strings.Join(pats, ", "), 300), after))
		event(tx, now, &who, "lock", fmt.Sprintf("%s's reservations lapsed (idle): %s", who.Name, clip(strings.Join(pats, ", "), 120)))
	}
	return nil
}

// lapseUnused ends explicit reservations that went unused past their
// UseWindow, the term stated when they were made, and tells the holder once.
func (s *Store) lapseUnused(tx *sql.Tx) error {
	now, terms := s.ms(), s.lim()
	rows, err := tx.Query(`SELECT `+lockColsL+` FROM locks l WHERE l.expires_at>? AND l.reason<>?`, now, autoReason)
	if err != nil {
		return err
	}
	var unused []Lock
	for rows.Next() {
		l, err := scanLock(rows)
		if err != nil {
			rows.Close()
			return err
		}
		if at := terms.unusedAt(l); at > 0 && at <= now {
			unused = append(unused, l)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, l := range unused {
		if _, err := tx.Exec(`UPDATE locks SET expires_at=? WHERE id=?`, now, l.ID); err != nil {
			return err
		}
		who := Agent{ID: l.AgentID, Name: l.Agent}
		notice(tx, now, who, "reservation lapsed (unused)",
			fmt.Sprintf("Your reservation of %s lapsed on its terms: no edits in it for %s, so others may edit it now. Editing a file or reserving it again takes it back if it is still free.",
				l.Pattern, terms.UseWindow(l.Pattern)))
		event(tx, now, &who, "lock", fmt.Sprintf("%s's reservation of %s lapsed (unused for %s)", who.Name, l.Pattern, terms.UseWindow(l.Pattern)))
	}
	return nil
}

// TouchFile is called just before an agent edits file. When another agent
// holds it, that reservation is returned (as CheckFile does). Otherwise the
// file is reserved for the editor for AutoLease, or its earlier automatic
// reservation is renewed. It never takes a file someone is queued for.
func (s *Store) TouchFile(ctx context.Context, agent, file string) (*Lock, error) {
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
		return nil, nil
	case err != nil:
		return &Lock{Pattern: file, Agent: SystemSender, Reason: "refused: " + err.Error()}, nil
	}
	shared := s.isShared(me.ProjectPath, rel)
	var held *Lock
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if err := s.settle(tx); err != nil {
			return err
		}
		live, err := s.liveLocks(ctx, tx, me.ProjectPath)
		if err != nil {
			return err
		}
		mine := false
		var used []int64
		for _, l := range live {
			if !Match(l.Pattern, rel) {
				continue
			}
			if shared { // nobody holds it; an edit still renews our own wider lease
				if l.AgentID == me.ID && l.Reason != autoReason {
					used = append(used, l.ID)
				}
				continue
			}
			if l.AgentID != me.ID {
				l := l
				held = &l
				return nil
			}
			mine = true
			if l.Reason != autoReason {
				used = append(used, l.ID)
			}
		}
		now := s.ms()
		lease := minutes(s.lim().AutoReserveMinutes).Milliseconds()
		until := now + lease
		for _, id := range used { // an edit inside an explicit reservation is use: it renews it
			if _, err := tx.Exec(`UPDATE locks SET used_at=? WHERE id=?`, now, id); err != nil {
				return err
			}
		}
		if lease == 0 || shared {
			return nil // the user has editing reserve nothing, or nothing here
		}
		if mine {
			// Renew automatic leases on this file; explicit ones keep their term.
			_, err := tx.Exec(`UPDATE locks SET expires_at=max(expires_at, ?) WHERE agent_id=? AND project_path=? AND pattern=? AND reason=?`,
				until, me.ID, me.ProjectPath, rel, autoReason)
			return err
		}
		line, err := s.queued(tx, me.ProjectPath, me.ID)
		if err != nil {
			return err
		}
		for _, w := range line {
			if Match(w.Pattern, rel) {
				return nil // someone waits for it: don't jump the queue
			}
		}
		_, err = tx.Exec(`INSERT INTO locks(project_path,pattern,agent_id,agent_name,reason,task_id,created_at,expires_at) VALUES(?,?,?,?,?,0,?,?)`,
			me.ProjectPath, rel, me.ID, me.Name, autoReason, now, until)
		return err
	})
	return held, err
}
