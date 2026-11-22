package core

import (
	"database/sql"
	"fmt"
	"strings"
)

// schemaV2 adds what the hub learned from real multi-agent use: per-agent
// delivery preferences and topic subscriptions, a wait queue for reserved
// files, how each receipt is delivered (full or one-line brief), previous
// names of an agent, and reservations linked to a task.
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
`

// addedColumns are applied with ALTER TABLE when missing, so a database made
// by an older version upgrades in place and older binaries keep working on it
// (every new column has a default).
var addedColumns = []struct{ table, column, ddl string }{
	{"receipts", "mode", `ALTER TABLE receipts ADD COLUMN mode TEXT NOT NULL DEFAULT 'full'`},
	{"agents", "aka", `ALTER TABLE agents ADD COLUMN aka TEXT NOT NULL DEFAULT ''`},
	{"locks", "task_id", `ALTER TABLE locks ADD COLUMN task_id INTEGER NOT NULL DEFAULT 0`},
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(schemaV2); err != nil {
		return fmt.Errorf("schema v2: %w", err)
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

// upgraded reports whether the v2 schema is already in place (reads only).
func upgraded(db *sql.DB) bool {
	for _, c := range addedColumns {
		if ok, err := hasColumn(db, c.table, c.column); err != nil || !ok {
			return false
		}
	}
	var n int
	err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('prefs','subscriptions','waiters')`).Scan(&n)
	return err == nil && n == 3
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
