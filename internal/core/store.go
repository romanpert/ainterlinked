// Package core is the ainterlinked hub: agents, messages, task boards, file
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
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// schemaVersion is raised with every addition to migrate.go: a database
// marked with the current version is not migrated again on open.
const schemaVersion = 6

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

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
	db      *sql.DB
	stale   time.Duration
	machine string
	alive   func(pids []int) (map[int]bool, error)
	now     func() time.Time
	limits  atomic.Pointer[Limits]

	keyPath string     // the hub secret agent keys derive from (keys.go)
	keyMu   sync.Mutex // guards secret
	secret  []byte
}

// Options tune a Store.
type Options struct {
	// Stale is how long an agent may go without a heartbeat before it is
	// considered offline.
	Stale time.Duration
	// Machine and Alive let a quiet agent stay online while its harness
	// process (its anchor PID) is still running on this machine. Alive
	// reports which pids exist; nil means heartbeats alone decide.
	Machine string
	Alive   func(pids []int) (map[int]bool, error)
	// Busy is how long to wait for another writer (0 = 10 s).
	Busy time.Duration
	// Limits are the user's terms (nil = DefaultLimits).
	Limits *Limits
}

// Open opens (creating if needed) the hub database at path.
func Open(path string, opt Options) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	busy := opt.Busy
	if busy <= 0 {
		busy = 10 * time.Second
	}
	dsn := "file:" + filepath.ToSlash(path) +
		fmt.Sprintf("?_pragma=busy_timeout(%d)", busy.Milliseconds()) +
		"&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	// Hooks open the hub on every call: skip the schema write when it's
	// already there, so opening is a read and never waits on a writer.
	var have string
	// ">=": a database already upgraded by a newer binary is left alone.
	// An older binary still running (a long-lived MCP server) resets the
	// marker to its own version; if the upgrade is already there, leave it,
	// or the two would rewrite the marker on every open.
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='schema'`).Scan(&have); err != nil || (atoi(have) < schemaVersion && !upgraded(db)) {
		if _, err := db.Exec(schema); err != nil {
			db.Close()
			return nil, fmt.Errorf("init schema: %w", err)
		}
		if err := migrate(db); err != nil {
			db.Close()
			return nil, err
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
	s := &Store{db: db, stale: opt.Stale, machine: opt.Machine, alive: opt.Alive, now: time.Now,
		keyPath: filepath.Join(filepath.Dir(path), "hub.key")}
	limits := DefaultLimits()
	if opt.Limits != nil {
		limits = *opt.Limits
	}
	s.SetLimits(limits)
	return s, nil
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

// oneLine makes a peer-supplied short field (activity, role, reason, task
// title, subject, name) a single line without control characters, so it
// can't impersonate a new section when shown to another model.
func oneLine(s string, n int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0x2028 || r == 0x2029:
		default:
			b.WriteRune(r)
		}
	}
	return clip(strings.Join(strings.Fields(b.String()), " "), n)
}

func isControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0x2028 || r == 0x2029
}

// hasControl reports line breaks or other control characters (spaces, even
// repeated, are fine: they're legal in file names).
func hasControl(s string) bool { return strings.IndexFunc(s, isControl) >= 0 }

// stripControl removes control characters without touching spacing.
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if isControl(r) {
			return -1
		}
		return r
	}, s)
}

// sanitizeReg applies oneLine to every text field of a registration: all of
// them end up in other models' context (agent lists, briefings, feed).
func sanitizeReg(r RegisterReq) RegisterReq {
	r.Harness = oneLine(r.Harness, 40)
	r.Name = oneLine(r.Name, 64)
	r.Role = oneLine(r.Role, 80)
	r.Model = oneLine(r.Model, 80)
	r.Machine = oneLine(r.Machine, 64)
	r.Project = oneLine(r.Project, 120)
	r.ProjectPath = stripControl(r.ProjectPath) // paths keep their exact spacing
	r.Branch = oneLine(r.Branch, 120)
	r.SessionID = clip(stripControl(r.SessionID), 200)
	r.Via = oneLine(r.Via, 20)
	r.Delivery = oneLine(r.Delivery, 20)
	return r
}

func cleanLabels(in []string) []string {
	var out []string
	for _, l := range in {
		if l = strings.ReplaceAll(oneLine(l, 40), ",", " "); l != "" {
			out = append(out, l)
		}
	}
	return out
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
		ts, id, name, oneLine(project, 120), oneLine(kind, 20), oneLine(summary, 300))
}

// Retention is how long feed events, read mail and offline agents are kept.
const Retention = 7 * 24 * time.Hour

// Prune deletes feed events, expired reservations, read messages and agents
// offline for longer than keep. Unread mail, and the agents it waits for
// (a resumed session revives them), are kept.
func (s *Store) Prune(ctx context.Context, keep time.Duration) error {
	cut := s.now().Add(-keep).UnixMilli()
	for _, q := range []struct {
		sql string
		arg int64
	}{
		{`DELETE FROM events WHERE ts < ?`, cut},
		// Briefs nobody read in time are stale news, not mail to keep.
		// (read_at = now, so they stay in history for the retention window.)
		{`UPDATE receipts SET read_at=strftime('%s','now')*1000 WHERE read_at=0 AND mode='brief' AND message_id IN (SELECT id FROM messages WHERE created_at < ?)`, s.briefCut()},
		{`DELETE FROM locks WHERE expires_at < ?`, s.ms()},
		// A refilled broadcast budget is the same as none.
		{`DELETE FROM broadcast_budget WHERE full_at < ?`, s.ms()},
		{`DELETE FROM receipts WHERE read_at > 0 AND read_at < ?`, cut},
		{`DELETE FROM messages WHERE created_at < ? AND id NOT IN (SELECT message_id FROM receipts)`, cut},
		{`DELETE FROM agents WHERE online=0 AND last_seen < ? AND id NOT IN (SELECT agent_id FROM receipts WHERE read_at=0)`, cut},
		// A session's spend is rewritten on every read: an old row is a
		// session long over, past what facts look back on (the same week).
		{`DELETE FROM spend WHERE updated_at < ?`, cut},
	} {
		if _, err := s.db.ExecContext(ctx, q.sql, q.arg); err != nil {
			return err
		}
	}
	return nil
}
