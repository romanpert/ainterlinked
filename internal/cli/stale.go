package cli

import (
	"context"
	"fmt"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/render"
)

// cmdTasksStale lists the open tasks nobody updated for a while, oldest
// first, for the human to keep, reopen or cancel.
func cmdTasksStale(ctx context.Context, args []string) error {
	fs := newFlags("tasks stale")
	board := fs.String("board", "*", "board name, * for all")
	days := fs.Int("days", 0, fmt.Sprintf("untouched for this many days (default: [tasks] stale_after_days, else %d)", core.DefaultStaleDays))
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		*board = pos[0]
	}
	if *days < 0 {
		return fmt.Errorf("--days %d: use 1 or more", *days)
	}
	c, _, err := openHub()
	if err != nil {
		return err
	}
	defer c.Close()
	var l core.StaleList
	if err := c.Call(ctx, "stale_tasks", core.StaleReq{Board: *board, Days: *days}, &l); err != nil {
		return err
	}
	fmt.Println(render.StaleTasks(l))
	return nil
}
