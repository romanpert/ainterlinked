package core

import (
	"context"
	"fmt"
	"strings"
)

// TaskPage is a board as list_tasks and `ainterlinked tasks` show it.
type TaskPage struct {
	Tasks []Task `json:"tasks"`
	// Closed counts the done and cancelled tasks left out: unless a status
	// is asked for, a board shows its open tasks only, since every closed
	// one shown costs whoever reads the list context.
	Closed int    `json:"closed,omitempty"`
	Query  string `json:"query,omitempty"`
	More   int    `json:"more,omitempty"` // search hits past the limit
}

// TaskList is ListTasks for a reader: open tasks and a count of the closed
// ones by default, every task with that status when one is asked for, and
// a search when f.Query is set.
func (s *Store) TaskList(ctx context.Context, f TaskFilter) (TaskPage, error) {
	if f.Status != "" && !validTaskStatus(f.Status) {
		return TaskPage{}, fmt.Errorf("status must be one of %s", strings.Join(TaskStatuses, ", "))
	}
	if strings.TrimSpace(f.Query) != "" {
		return s.SearchTasks(ctx, f)
	}
	if f.Status != "" {
		list, err := s.ListTasks(ctx, f)
		return TaskPage{Tasks: list}, err
	}
	f.Open = true
	list, err := s.ListTasks(ctx, f)
	if err != nil {
		return TaskPage{}, err
	}
	all := f
	all.Open = false
	scope, args := s.taskScope(ctx, all)
	page := TaskPage{Tasks: list}
	err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM tasks WHERE status IN ('done','cancelled')`+scope, args...).Scan(&page.Closed)
	return page, err
}
