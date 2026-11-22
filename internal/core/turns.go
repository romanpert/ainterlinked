package core

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
)

// Who started an agent's current turn: the user (a prompt) or peers (the
// turn was held open by peer mail at its end). It decides whether what the
// agent does is the user's act (the user's order) or its own, so the hub
// keeps it, not a file the agent could edit. Hooks report the two events;
// the count is in meta, which every schema has.

// TurnReq reports a turn event for Agent: User when the user prompted,
// otherwise the turn was held open by peer mail.
type TurnReq struct {
	Agent string `json:"agent"`
	User  bool   `json:"user,omitempty"`
}

func turnKey(agent string) string { return "turn_held:" + agent }

// MarkTurn records a turn event and returns how many times in a row the
// turn has been held open by peers since the user last prompted.
func (s *Store) MarkTurn(ctx context.Context, r TurnReq) (int, error) {
	if r.Agent == "" {
		return 0, errors.New("agent is required")
	}
	if r.User {
		_, err := s.db.ExecContext(ctx, `DELETE FROM meta WHERE key=?`, turnKey(r.Agent))
		return 0, err
	}
	var n int
	err := s.tx(ctx, func(tx *sql.Tx) error {
		n = heldIn(tx, r.Agent) + 1
		_, err := tx.Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)`, turnKey(r.Agent), strconv.Itoa(n))
		return err
	})
	return n, err
}

// PeerHeld returns how many times in a row the agent's turn has been held
// open by peers since the user last prompted (0: the user's turn).
func (s *Store) PeerHeld(ctx context.Context, agent string) (int, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, turnKey(agent)).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	n, _ := strconv.Atoi(v)
	return n, err
}

func heldIn(tx *sql.Tx, agent string) int {
	var v string
	if tx.QueryRow(`SELECT value FROM meta WHERE key=?`, turnKey(agent)).Scan(&v) != nil {
		return 0
	}
	n, _ := strconv.Atoi(v)
	return n
}
