// Package core is the interlinked hub: agents, messages, task boards, file
// reservations and an activity feed, persisted in a single SQLite database
// (WAL mode) that every harness process on the machine shares.
package core

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value TEXT);
CREATE TABLE IF NOT EXISTS agents(
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  harness TEXT NOT NULL,
  model TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL DEFAULT '',
  machine TEXT NOT NULL DEFAULT '',
  project TEXT NOT NULL DEFAULT '',
  project_path TEXT NOT NULL DEFAULT '',
  branch TEXT NOT NULL DEFAULT '',
  anchor_pid INTEGER NOT NULL DEFAULT 0,
  session_id TEXT NOT NULL DEFAULT '',
  ukey TEXT NOT NULL,
  online INTEGER NOT NULL DEFAULT 1,
  state TEXT NOT NULL DEFAULT 'idle',
  activity TEXT NOT NULL DEFAULT '',
  via TEXT NOT NULL DEFAULT '',
  delivery TEXT NOT NULL DEFAULT '',
  started_at INTEGER NOT NULL,
  last_seen INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS agents_live_name ON agents(name) WHERE online = 1;
CREATE UNIQUE INDEX IF NOT EXISTS agents_live_key ON agents(ukey) WHERE online = 1;
CREATE INDEX IF NOT EXISTS agents_session ON agents(harness, session_id);
CREATE TABLE IF NOT EXISTS messages(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  thread_id INTEGER NOT NULL DEFAULT 0,
  reply_to INTEGER NOT NULL DEFAULT 0,
  sender_id TEXT NOT NULL,
  sender_name TEXT NOT NULL,
  target TEXT NOT NULL,
  subject TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL,
  urgent INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS receipts(
  message_id INTEGER NOT NULL,
  agent_id TEXT NOT NULL,
  read_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(message_id, agent_id)
);
CREATE INDEX IF NOT EXISTS receipts_unread ON receipts(agent_id, read_at);
CREATE TABLE IF NOT EXISTS tasks(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  board TEXT NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'todo',
  priority INTEGER NOT NULL DEFAULT 2,
  assignee_id TEXT NOT NULL DEFAULT '',
  assignee TEXT NOT NULL DEFAULT '',
  created_by TEXT NOT NULL DEFAULT '',
  created_by_id TEXT NOT NULL DEFAULT '',
  depends_on TEXT NOT NULL DEFAULT '',
  labels TEXT NOT NULL DEFAULT '',
  result TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS tasks_board ON tasks(board, status);
CREATE TABLE IF NOT EXISTS locks(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_path TEXT NOT NULL,
  pattern TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  agent_name TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS locks_project ON locks(project_path);
CREATE TABLE IF NOT EXISTS events(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts INTEGER NOT NULL,
  agent_id TEXT NOT NULL DEFAULT '',
  agent_name TEXT NOT NULL DEFAULT '',
  project TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL,
  summary TEXT NOT NULL
);
`

// Store is the hub database.
type Store struct {
	db    *sql.DB
	stale time.Duration
	now   func() time.Time
}

// Options tune a Store.
type Options struct {
	// Stale is how long an agent may go without a heartbeat before it is
	// considered offline.
	Stale time.Duration
}

// Open opens (creating if needed) the hub database at path.
func Open(path string, opt Options) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	// Hooks open the hub on every call: skip the schema write when it's
	// already there, so opening is a read and never waits on a writer.
	var have string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='schema'`).Scan(&have); err != nil || have != fmt.Sprint(schemaVersion) {
		if _, err := db.Exec(schema); err != nil {
			db.Close()
			return nil, fmt.Errorf("init schema: %w", err)
		}
		if _, err := db.Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES('schema',?)`, fmt.Sprint(schemaVersion)); err != nil {
			db.Close()
			return nil, err
		}
	}
	_ = os.Chmod(path, 0o600)
	if opt.Stale <= 0 {
		opt.Stale = 45 * time.Second
	}
	return &Store{db: db, stale: opt.Stale, now: time.Now}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) ms() int64 { return s.now().UnixMilli() }

func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

var idEnc = base32.NewEncoding("abcdefghijkmnpqrstuvwxyz23456789").WithPadding(base32.NoPadding)

func newID() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return idEnc.EncodeToString(b)
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// event appends to the activity feed (best effort, inside the caller's tx).
func event(tx *sql.Tx, ts int64, a *Agent, kind, summary string) {
	var id, name, project string
	if a != nil {
		id, name, project = a.ID, a.Name, a.Project
	}
	_, _ = tx.Exec(`INSERT INTO events(ts,agent_id,agent_name,project,kind,summary) VALUES(?,?,?,?,?,?)`,
		ts, id, name, project, kind, clip(summary, 300))
}

// Prune deletes feed events, expired reservations, and read messages older
// than the retention window. Unread mail is kept.
func (s *Store) Prune(ctx context.Context, keep time.Duration) error {
	cut := s.now().Add(-keep).UnixMilli()
	for _, q := range []struct {
		sql string
		arg int64
	}{
		{`DELETE FROM events WHERE ts < ?`, cut},
		{`DELETE FROM locks WHERE expires_at < ?`, s.ms()},
		{`DELETE FROM receipts WHERE read_at > 0 AND read_at < ?`, cut},
		{`DELETE FROM messages WHERE created_at < ? AND id NOT IN (SELECT message_id FROM receipts)`, cut},
	} {
		if _, err := s.db.ExecContext(ctx, q.sql, q.arg); err != nil {
			return err
		}
	}
	return nil
}
