package config

// Tasks configures the task boards.
type Tasks struct {
	// ArchiveAfterDays, above 0, has the hub archive by itself, about once
	// an hour, the tasks closed more than this many days ago (what
	// `ainterlinked tasks archive --older-than <n>d` does). 0, the default:
	// only the user archives.
	ArchiveAfterDays int `toml:"archive_after_days"`
}
