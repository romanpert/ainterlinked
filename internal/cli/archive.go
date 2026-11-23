package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/romanpert/ainterlinked/internal/core"
)

const archiveUsage = `usage: ainterlinked tasks archive [--board b] [--older-than 7d] [--to file.md]`

// cmdTasksArchive moves closed tasks off their boards into Markdown files
// (and archived_tasks, where search still finds them).
func cmdTasksArchive(ctx context.Context, args []string) error {
	fs := newFlags("tasks archive")
	board := fs.String("board", "*", "only this board (default: every board)")
	older := fs.String("older-than", "", "only tasks closed longer ago than this, e.g. 7d or 12h")
	to := fs.String("to", "", "append them all to this Markdown file (default: <home>/archive/<board>.md)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return errors.New(archiveUsage)
	}
	age, err := parseAge(*older)
	if err != nil {
		return err
	}
	path := *to
	if path != "" {
		if path, err = filepath.Abs(path); err != nil { // the hub process has its own working directory
			return err
		}
	}
	c, cfg, err := openHub()
	if err != nil {
		return err
	}
	defer c.Close()
	me, err := human(ctx, c, cfg)
	if err != nil {
		return err
	}
	var res core.ArchiveResult
	if err := c.Call(ctx, "task_archive", core.ArchiveReq{By: me, Board: *board, OlderThan: age.Milliseconds(), To: path}, &res); err != nil {
		return err
	}
	fmt.Println(archiveReport(res))
	return nil
}

func archiveReport(res core.ArchiveResult) string {
	if res.Count == 0 {
		return "Nothing to archive: no closed task matches."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Archived %d closed task(s); search still finds them (tasks --query).", res.Count)
	for _, f := range res.Files {
		ids := make([]string, len(f.IDs))
		for i, id := range f.IDs {
			ids[i] = fmt.Sprintf("#%d", id)
		}
		fmt.Fprintf(&b, "\n  %s → %s", strings.Join(ids, " "), tilde(f.Path))
	}
	return b.String()
}

// parseAge reads 7d, 36h, 90m (or nothing: any age).
func parseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, nil
	}
	var d time.Duration
	var err error
	if days, ok := strings.CutSuffix(s, "d"); ok {
		var n float64
		n, err = strconv.ParseFloat(days, 64)
		d = time.Duration(n * float64(24*time.Hour))
	} else {
		d, err = time.ParseDuration(s)
	}
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("--older-than %q: use a positive age like 7d, 36h or 90m", s)
	}
	return d, nil
}
