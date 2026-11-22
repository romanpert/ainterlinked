package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// maxAka bounds how many earlier names an agent keeps.
const maxAka = 8

// An agent can only be continued once it has been silent this long.
const handoverQuiet = 2 * time.Minute

// setAka records a's current name among its earlier names before it changes
// to next (so messages to the old name still reach it).
func setAka(tx *sql.Tx, a Agent, next string) error {
	return addAka(tx, a.ID, next, append([]string{a.Name}, a.Aka...))
}

func addAka(tx *sql.Tx, id, current string, names []string) error {
	var keep []string
	seen := map[string]bool{strings.ToLower(current): true}
	for _, n := range names {
		k := strings.ToLower(strings.TrimSpace(n))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		keep = append(keep, k)
		if len(keep) == maxAka {
			break
		}
	}
	_, err := tx.Exec(`UPDATE agents SET aka=? WHERE id=?`, strings.Join(keep, ","), id)
	return err
}

// ContinueAs makes agent me the continuation of an offline agent: after a
// restart, or a switch of IDE, provider or account, the new session takes
// the old one's name, unread mail, threads, tasks, reservations, queue
// places and delivery preferences, so nothing is lost in the hand-over.
// Only an offline agent of the same project can be continued.
func (s *Store) ContinueAs(ctx context.Context, me, ref string) (Agent, error) {
	self, err := s.Get(ctx, me)
	if err != nil {
		return Agent{}, err
	}
	old, err := s.resolveOffline(ctx, ref)
	if err != nil {
		return Agent{}, err
	}
	if old.ID == self.ID {
		return self, nil
	}
	if old.Online {
		return Agent{}, fmt.Errorf("%s is still online; only an agent that has left can be continued (message it instead)", old.Name)
	}
	if old.Harness == "human" || reservedNames[strings.ToLower(old.Name)] || self.Harness == "human" {
		return Agent{}, fmt.Errorf("%s can't be continued by an agent", old.Name)
	}
	// The project is identified by its checkout, not by the name an agent
	// declares: the same path on the same machine.
	switch {
	case self.ProjectPath == "" || old.ProjectPath == "":
		return Agent{}, errors.New("both agents need a project checkout to hand over work")
	case old.Machine != self.Machine:
		return Agent{}, fmt.Errorf("%s worked on machine %q; hand-overs work within one machine only", old.Name, old.Machine)
	case old.ProjectPath != self.ProjectPath:
		return Agent{}, fmt.Errorf("%s worked in %s, you are in %s; continue it from that checkout", old.Name, old.ProjectPath, self.ProjectPath)
	}
	// It must really be gone, not in a long think: quiet for a while, and
	// its harness process must have exited.
	quiet := handoverQuiet
	if since := time.Duration(s.ms()-old.LastSeen) * time.Millisecond; since < quiet {
		return Agent{}, fmt.Errorf("%s was seen %s ago; it can be continued once it has been gone %s", old.Name, since.Round(time.Second), quiet)
	}
	if old.Machine == s.machine && old.AnchorPID > 0 && s.alive != nil {
		if alive, err := s.alive([]int{old.AnchorPID}); err == nil && alive[old.AnchorPID] {
			return Agent{}, fmt.Errorf("%s's session is still running (pid %d); message it instead", old.Name, old.AnchorPID)
		}
	}
	var out Agent
	err = s.tx(ctx, func(tx *sql.Tx) error {
		now := s.ms()
		var online int
		if err := tx.QueryRow(`SELECT online FROM agents WHERE id=?`, old.ID).Scan(&online); err != nil || online == 1 {
			return fmt.Errorf("%s came back online; message it instead", old.Name)
		}
		// Take its name when nobody online has it; keep ours as an earlier one.
		name := self.Name
		if s.nameFree(tx, old.Name, self.ID) {
			name = old.Name
		}
		moves := []struct {
			sql  string
			args []any
		}{
			// Mail: a receipt the new agent already has wins.
			{`UPDATE OR IGNORE receipts SET agent_id=? WHERE agent_id=?`, []any{self.ID, old.ID}},
			{`DELETE FROM receipts WHERE agent_id=?`, []any{old.ID}},
			// Threads it started or answered stay its own (replies come here).
			{`UPDATE messages SET sender_id=? WHERE sender_id=?`, []any{self.ID, old.ID}},
			{`UPDATE tasks SET assignee_id=?, assignee=? WHERE assignee_id=?`, []any{self.ID, name, old.ID}},
			{`UPDATE tasks SET created_by_id=? WHERE created_by_id=?`, []any{self.ID, old.ID}},
			// Reservations only carry over inside the same checkout.
			{`UPDATE locks SET agent_id=?, agent_name=? WHERE agent_id=? AND project_path=?`, []any{self.ID, name, old.ID, self.ProjectPath}},
			{`DELETE FROM locks WHERE agent_id=?`, []any{old.ID}},
			{`UPDATE waiters SET agent_id=? WHERE agent_id=? AND project_path=?`, []any{self.ID, old.ID, self.ProjectPath}},
			{`DELETE FROM waiters WHERE agent_id=?`, []any{old.ID}},
			{`INSERT OR IGNORE INTO prefs(agent_id,broadcasts,inject,max_messages,max_chars,inline_chars)
				SELECT ?,broadcasts,inject,max_messages,max_chars,inline_chars FROM prefs WHERE agent_id=?`, []any{self.ID, old.ID}},
			{`INSERT OR IGNORE INTO subscriptions(agent_id,topic,mode) SELECT ?,topic,mode FROM subscriptions WHERE agent_id=?`, []any{self.ID, old.ID}},
			{`DELETE FROM prefs WHERE agent_id=?`, []any{old.ID}},
			{`DELETE FROM subscriptions WHERE agent_id=?`, []any{old.ID}},
		}
		for _, m := range moves {
			if _, err := tx.Exec(m.sql, m.args...); err != nil {
				return err
			}
		}
		// What it knew is now the successor's to read, no longer on offer.
		if err := takeHandovers(tx, old.ID, self.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE agents SET name=?, role=COALESCE(NULLIF(role,''),?), last_seen=? WHERE id=?`, name, old.Role, now, self.ID); err != nil {
			return err
		}
		earlier := append(append([]string{self.Name, old.Name}, old.Aka...), self.Aka...)
		if err := addAka(tx, self.ID, name, earlier); err != nil {
			return err
		}
		// The old row stays as history under a name of its own (so its old
		// name resolves to the successor), and if that session ever comes
		// back it is told where its work went.
		if _, err := tx.Exec(`UPDATE agents SET name=?, aka='', activity=? WHERE id=?`,
			clip(old.Name, 40)+"~"+old.ID, clip("continued as "+name, 200), old.ID); err != nil {
			return err
		}
		notice(tx, now, Agent{ID: old.ID, Name: old.Name}, "your work was continued",
			fmt.Sprintf("While you were away, %q (harness %q on machine %q) continued your work: your mail, threads, tasks and reservations are now theirs. If that wasn't intended, tell the operator.",
				oneLine(name, 64), oneLine(self.Harness, 40), oneLine(self.Machine, 64)))
		event(tx, now, &self, "lock", fmt.Sprintf("%s took over %s's tasks and reservations", name, old.Name))
		event(tx, now, &self, "joined", fmt.Sprintf("%s continues %s (%s → %s)", name, old.Name, old.Harness, self.Harness))
		var err error
		out, err = scanAgent(tx.QueryRow(`SELECT `+agentCols+` FROM agents WHERE id=?`, self.ID))
		return err
	})
	return out, err
}

// resolveOffline finds the agent a hand-over refers to: by id, by name or
// by an earlier name, preferring the most recently seen.
func (s *Store) resolveOffline(ctx context.Context, ref string) (Agent, error) {
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "@")
	if ref == "" {
		return Agent{}, errors.New("continue_as needs an agent name")
	}
	a, err := scanAgent(s.db.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents
		WHERE id=? OR lower(name)=lower(?) OR instr(','||lower(aka)||',', ','||lower(?)||',')>0
		ORDER BY online ASC, last_seen DESC LIMIT 1`, ref, ref, ref))
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, fmt.Errorf("no agent named %q (list_agents include_offline=true)", ref)
	}
	return a, err
}
