package config

import "fmt"

// Tasks configures the task boards.
type Tasks struct {
	// ArchiveAfterDays, above 0, has the hub archive by itself, about once
	// an hour, the tasks closed more than this many days ago (what
	// `ainterlinked tasks archive --older-than <n>d` does). 0, the default:
	// only the user archives.
	ArchiveAfterDays int `toml:"archive_after_days"`
	// StaleAfterDays: a todo, review or blocked task nobody updated for this
	// many days gets one notice, to its creator or the human. 0: none.
	StaleAfterDays int `toml:"stale_after_days"`
}

// Check refuses values that mean nothing.
func (t Tasks) Check() error {
	if t.ArchiveAfterDays < 0 || t.ArchiveAfterDays > 100_000 {
		return fmt.Errorf("tasks.archive_after_days = %d: use 0 (off) to 100000", t.ArchiveAfterDays)
	}
	if t.StaleAfterDays < 0 || t.StaleAfterDays > 100_000 {
		return fmt.Errorf("tasks.stale_after_days = %d: use 0 (off) to 100000", t.StaleAfterDays)
	}
	return nil
}
