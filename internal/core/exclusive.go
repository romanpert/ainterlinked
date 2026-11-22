package core

import (
	"context"
	"strings"
	"time"
)

// SingleConn makes this Store use one database connection, kept open: the
// hub process is the database's only user, and SQLite has one writer
// anyway.
func (s *Store) SingleConn() {
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)
	s.db.SetConnMaxLifetime(0)
	s.db.SetConnMaxIdleTime(0)
}

// Exclusive puts the database in SQLite's exclusive locking mode (after
// SingleConn): from the next write on, no other process can read or write
// the file while the hub runs; a script opening it gets "database is
// locked". State the agents could otherwise edit directly is out of reach.
func (s *Store) Exclusive(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA locking_mode=EXCLUSIVE`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO meta(key,value) VALUES('hub_exclusive_since', ?)`, time.Now().UTC().Format(time.RFC3339))
	return err
}

// DirectUsers counts agents online that didn't come through the hub
// process (ids): sessions of an older binary that open the file directly.
func (s *Store) DirectUsers(ctx context.Context, ids []string) (int, error) {
	q := `SELECT count(*) FROM agents WHERE online=1 AND harness<>'human'`
	args := []any{}
	if len(ids) > 0 {
		q += ` AND id NOT IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	}
	var n int
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

// AnchorTaken reports whether a harness process anchors a registered agent
// other than the human: such a process and its children act as that agent.
func (s *Store) AnchorTaken(ctx context.Context, anchor int) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM agents WHERE anchor_pid=? AND harness<>'human' AND online=1`, anchor).Scan(&n)
	return n > 0, err
}
