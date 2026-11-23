package render

import (
	"fmt"

	"github.com/romanpert/ainterlinked/internal/core"
)

// TaskPage renders a board or a search: one line per task, then one line
// for what was left out.
func TaskPage(p core.TaskPage) string {
	var s string
	switch {
	case len(p.Tasks) > 0:
		s = Tasks(p.Tasks)
	case p.Query != "":
		s = fmt.Sprintf("No task matches %q.", p.Query)
	case p.Closed > 0:
		s = "No open tasks."
	default:
		s = "No tasks."
	}
	if p.More > 0 {
		s += fmt.Sprintf("\n%d more match (raise limit to see them)", p.More)
	}
	if p.Closed > 0 {
		s += fmt.Sprintf("\n%d closed (status=done or status=cancelled to see them)", p.Closed)
	}
	return s
}

// foundMarks is what a search adds to a task's line: whether it is
// archived, and the text around the match.
func foundMarks(t core.Task) string {
	var s string
	if t.ArchivedAt > 0 {
		s += " · archived"
	}
	if t.Snippet != "" {
		s += "\n    match: " + oneLine(t.Snippet)
	}
	return s
}
