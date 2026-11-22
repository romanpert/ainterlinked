package core

import (
	"context"
	"database/sql"
	"strconv"
)

// Wakeable is an idle agent with unread mail addressed to it by name that it
// hasn't been woken for.
type Wakeable struct {
	Agent
	Direct int   // such messages waiting
	last   int64 // the newest of them
}

func wokenKey(agent string) string { return "woken:" + agent }

// Wakeable lists a harness's agents to wake now: idle, not choosing to read
// their mail by hand, and still allowed to be continued by peers.
func (s *Store) Wakeable(ctx context.Context, harness string) ([]Wakeable, error) {
	list, err := s.List(ctx, AgentFilter{Harness: harness})
	if err != nil {
		return nil, err
	}
	var out []Wakeable
	for _, a := range list {
		if a.SessionID == "" || a.Unread == 0 || (a.State != "idle" && a.State != "done") {
			continue
		}
		w := Wakeable{Agent: a}
		var last sql.NullInt64
		err := s.db.QueryRowContext(ctx, `SELECT count(*), max(message_id) FROM receipts WHERE agent_id=? AND read_at=0 AND direct=1`, a.ID).Scan(&w.Direct, &last)
		if err != nil {
			return out, err
		}
		if w.last = last.Int64; w.Direct == 0 || w.last <= s.wokenUpTo(ctx, a.ID) {
			continue
		}
		p, err := s.GetPrefs(ctx, a.ID)
		if err != nil || p.Inject == InjectManual {
			continue
		}
		if n, err := s.PeerHeld(ctx, a.ID); err != nil || s.lim().HeldOut(n) {
			continue
		}
		out = append(out, w)
	}
	return out, nil
}

func (s *Store) wokenUpTo(ctx context.Context, agent string) int64 {
	var v string
	if s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, wokenKey(agent)).Scan(&v) != nil {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

// Waking records, before it is tried, that w is being woken for the mail it
// has: the turn is peers' and the same mail never wakes it twice.
func (s *Store) Waking(ctx context.Context, w Wakeable) error {
	if _, err := s.MarkTurn(ctx, TurnReq{Agent: w.ID}); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)`, wokenKey(w.ID), strconv.FormatInt(w.last, 10))
	return err
}

// WakeResult records whether waking an agent works, as the delivery its
// senders are told about.
func (s *Store) WakeResult(ctx context.Context, agent string, worked bool) error {
	tier := "turn"
	if worked {
		tier = "push"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET delivery=? WHERE id=?`, tier, agent)
	return err
}
