package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"
)

// The pace of waking one session, whatever max_holds says: two agents that
// write to each other must not wake each other without end.
const (
	WakeSpacing = 15 * time.Second
	WakesHourly = 30
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

// Wake says whether an agent may be woken now and, if so, records it before
// it is tried: the turn it starts is peers'. Every way of waking asks here.
func (s *Store) Wake(ctx context.Context, agent string) (bool, error) {
	ok := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		held := heldIn(tx, agent)
		if s.lim().HeldOut(held) {
			return nil
		}
		now := s.ms()
		var v string
		var all, recent []int64
		if tx.QueryRow(`SELECT value FROM meta WHERE key=?`, wakesKey(agent)).Scan(&v) == nil {
			_ = json.Unmarshal([]byte(v), &all)
		}
		for _, at := range all {
			if now-at < time.Hour.Milliseconds() {
				recent = append(recent, at)
			}
		}
		if n := len(recent); n >= WakesHourly || (n > 0 && now-recent[n-1] < WakeSpacing.Milliseconds()) {
			return nil
		}
		b, err := json.Marshal(append(recent, now))
		if err != nil {
			return err
		}
		for k, v := range map[string]string{
			wakesKey(agent):  string(b),
			turnKey(agent):   strconv.Itoa(held + 1),
			heldAtKey(agent): strconv.FormatInt(now, 10),
		} {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)`, k, v); err != nil {
				return err
			}
		}
		ok = true
		return nil
	})
	return ok && err == nil, err
}

func wakesKey(agent string) string { return "wakes:" + agent }

// Waking is Wake for the mail w has, which never wakes it twice.
func (s *Store) Waking(ctx context.Context, w Wakeable) (bool, error) {
	ok, err := s.Wake(ctx, w.ID)
	if !ok || err != nil {
		return false, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)`, wokenKey(w.ID), strconv.FormatInt(w.last, 10))
	return err == nil, err
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
