package core

import (
	"database/sql"
	"fmt"
	"strings"
)

// schemaV2 adds what the hub learned from real multi-agent use: per-agent
// delivery preferences and topic subscriptions, a wait queue for reserved
// files, how each receipt is delivered (full or one-line brief), previous
// names of an agent, reservations linked to a task, each sender's
// broadcast budget, and this machine's load (v6, one row).
const schemaV2 = `
CREATE TABLE IF NOT EXISTS prefs(
  agent_id TEXT PRIMARY KEY,
  broadcasts TEXT NOT NULL DEFAULT '',
  inject TEXT NOT NULL DEFAULT '',
  max_messages INTEGER NOT NULL DEFAULT 0,
  max_chars INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS subscriptions(
  agent_id TEXT NOT NULL,
  topic TEXT NOT NULL,
  mode TEXT NOT NULL DEFAULT 'full',
  PRIMARY KEY(agent_id, topic)
);
CREATE INDEX IF NOT EXISTS subscriptions_topic ON subscriptions(topic);
CREATE TABLE IF NOT EXISTS waiters(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_path TEXT NOT NULL,
  pattern TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  task_id INTEGER NOT NULL DEFAULT 0,
  ttl_ms INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS waiters_project ON waiters(project_path);
CREATE TABLE IF NOT EXISTS task_wants(
  task_id INTEGER NOT NULL,
  agent_id TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY(task_id, agent_id)
);
CREATE TABLE IF NOT EXISTS broadcast_budget(
  agent_id TEXT PRIMARY KEY,
  full_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS machine(
  id INTEGER PRIMARY KEY CHECK(id=1),
  cpu REAL NOT NULL DEFAULT -1,
  cpu_avg REAL NOT NULL DEFAULT -1,
  approx INTEGER NOT NULL DEFAULT 0,
  mem_total INTEGER NOT NULL DEFAULT 0,
  mem_avail INTEGER NOT NULL DEFAULT 0,
  disk_total INTEGER NOT NULL DEFAULT 0,
  disk_free INTEGER NOT NULL DEFAULT 0,
  cores INTEGER NOT NULL DEFAULT 0,
  sampled_at INTEGER NOT NULL DEFAULT 0,
  sampler_id TEXT NOT NULL DEFAULT '',
  lease_until INTEGER NOT NULL DEFAULT 0
);
INSERT OR IGNORE INTO machine(id) VALUES(1);
-- v7: tokens a session spent, by whose turn it was (spend.go): '' the
-- user's, '~peer' another session's, '~held' mail since pruned, else the
-- agent whose mail held the turn open. Absolute figures, replaced on read.
CREATE TABLE IF NOT EXISTS spend(
  agent_id TEXT NOT NULL,
  cause TEXT NOT NULL,
  tin INTEGER NOT NULL DEFAULT 0,
  cwrite INTEGER NOT NULL DEFAULT 0,
  cread INTEGER NOT NULL DEFAULT 0,
  tout INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(agent_id, cause)
);
CREATE INDEX IF NOT EXISTS spend_cause ON spend(cause, updated_at);

-- v8: what a session that left knew, for whoever carries its work on
-- (handover.go): one per agent and harness session.
CREATE TABLE IF NOT EXISTS handovers(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  agent_id TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  harness TEXT NOT NULL,
  machine TEXT NOT NULL DEFAULT '',
  project_path TEXT NOT NULL DEFAULT '',
  branch TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL,
  gist TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  taken_by TEXT NOT NULL DEFAULT '',
  UNIQUE(agent_id, session_id)
);
CREATE INDEX IF NOT EXISTS handovers_place ON handovers(machine, project_path, created_at);
`

// addedColumns are applied with ALTER TABLE when missing, so a database made
// by an older version upgrades in place and older binaries keep working on it
// (every new column has a default).
var addedColumns = []struct{ table, column, ddl string }{
	{"receipts", "mode", `ALTER TABLE receipts ADD COLUMN mode TEXT NOT NULL DEFAULT 'full'`},
	{"agents", "aka", `ALTER TABLE agents ADD COLUMN aka TEXT NOT NULL DEFAULT ''`},
	{"locks", "task_id", `ALTER TABLE locks ADD COLUMN task_id INTEGER NOT NULL DEFAULT 0`},
	// v3: when the agent entered its current state (idle reservations lapse).
	{"agents", "state_since", `ALTER TABLE agents ADD COLUMN state_since INTEGER NOT NULL DEFAULT 0`},
	// v4: last edit inside an explicit reservation (wide leases lapse unused).
	{"locks", "used_at", `ALTER TABLE locks ADD COLUMN used_at INTEGER NOT NULL DEFAULT 0`},
	// v4: mail addressed to the recipient by name or id (only that holds a turn).
	{"receipts", "direct", `ALTER TABLE receipts ADD COLUMN direct INTEGER NOT NULL DEFAULT 0`},
	// v5: SHA-256 of the agent's key (keys.go); '' = registered by an older version.
	{"agents", "key_hash", `ALTER TABLE agents ADD COLUMN key_hash TEXT NOT NULL DEFAULT ''`},
	// v8: bodies longer than this arrive as a gist and a pointer (0: config's).
	{"prefs", "inline_chars", `ALTER TABLE prefs ADD COLUMN inline_chars INTEGER NOT NULL DEFAULT 0`},
	// v9: the updated_at of a task when it was last noticed as stale
	// (stale.go): one notice per untouched stretch, and any update starts
	// a new one.
	{"tasks", "stale_noticed", `ALTER TABLE tasks ADD COLUMN stale_noticed INTEGER NOT NULL DEFAULT 0`},
}

// migrate brings db to the current schema.
func migrate(db *sql.DB) error {
	if _, err := db.Exec(schemaV2); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	for _, c := range addedColumns {
		ok, err := hasColumn(db, c.table, c.column)
		if err != nil {
			return err
		}
		if ok {
			continue
		}
		if _, err := db.Exec(c.ddl); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add %s.%s: %w", c.table, c.column, err)
		}
	}
	return nil
}

// upgraded reports whether the current schema is already in place (reads only).
func upgraded(db *sql.DB) bool {
	for _, c := range addedColumns {
		if ok, err := hasColumn(db, c.table, c.column); err != nil || !ok {
			return false
		}
	}
	var n int
	err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('prefs','subscriptions','waiters','task_wants','broadcast_budget','machine','spend','handovers')`).Scan(&n)
	return err == nil && n == 8
}

func hasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return false, err
		}
		if n == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
