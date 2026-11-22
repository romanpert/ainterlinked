package core

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/romanpert/ainterlinked/internal/transcript"
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

// heldAtKey keeps when peers last held the agent's turn open (hub clock).
func heldAtKey(agent string) string { return "turn_held_at:" + agent }

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
		if _, err := tx.Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)`, turnKey(r.Agent), strconv.Itoa(n)); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)`, heldAtKey(r.Agent), strconv.FormatInt(s.ms(), 10))
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

// userTurn decides whether the agent's current turn is the user's. A
// harness whose transcript interlinked reads (Claude Code) answers it: the
// user's latest prompt must be newer than any peer-started turn and than
// the last time the hub saw peers hold the turn open. A hook reporting
// "the user prompted" can't change that, since an agent can run a hook
// command itself. Other harnesses: the hub's count of held turns, reset
// by the hook on a user prompt (that reset is the hooks' word).
func (s *Store) userTurn(ctx context.Context, a Agent, path string) bool {
	if o, known := transcript.Read(a.Harness, path, a.SessionID); known {
		var heldAt int64
		var v string
		if s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, heldAtKey(a.ID)).Scan(&v) == nil {
			heldAt, _ = strconv.ParseInt(v, 10, 64)
		}
		return o.User > 0 && o.User > o.Peer && o.User > heldAt
	}
	n, err := s.PeerHeld(ctx, a.ID)
	return err == nil && n == 0
}
