package hub

import (
	"context"
	"path/filepath"
	"time"

	"github.com/romanpert/ainterlinked/internal/config"
)

// ArchiveDir is where archived tasks are written, one Markdown file per board.
func ArchiveDir() string { return filepath.Join(config.Home(), "archive") }

// archiveTick archives the tasks closed more than [tasks] archive_after_days
// ago, when the user set it (0, the default: only the user archives). Each
// file is tried once per tick; one that can't be written keeps its tasks on
// their board and is logged, and the others go ahead.
func (s *Server) archiveTick(ctx context.Context) {
	days := s.archiveDays.Load()
	if days <= 0 {
		return
	}
	res, err := s.Store.AutoArchive(ctx, time.Duration(days)*24*time.Hour, ArchiveDir())
	for _, f := range res.Failed {
		logf("archive: %d closed task(s) stay on their board, %s can't be written: %s", len(f.IDs), f.Path, f.Error)
	}
	if err != nil && len(res.Failed) == 0 {
		logf("archive: %v", err)
	}
}
