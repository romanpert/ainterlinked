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
// ago, when the user set it (0, the default: only the user archives).
func (s *Server) archiveTick(ctx context.Context) {
	if days := s.archiveDays.Load(); days > 0 {
		_, _ = s.Store.AutoArchive(ctx, time.Duration(days)*24*time.Hour, ArchiveDir())
	}
}
