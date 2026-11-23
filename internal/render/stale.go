package render

import (
	"fmt"
	"strings"

	"github.com/romanpert/ainterlinked/internal/core"
)

// untouched is the age of an open task nobody updated for a day or more.
func untouched(t core.Task) string {
	if t.Untouched < 1 {
		return ""
	}
	return fmt.Sprintf(" · %dd untouched", t.Untouched)
}

// staleCount is a board's count of forgotten tasks, when the user asked
// for one ([tasks] stale_after_days).
func staleCount(b core.Board) string {
	if b.Stale == 0 {
		return ""
	}
	return fmt.Sprintf(" · %d untouched %dd+", b.Stale, b.StaleDays)
}

// StaleTasks renders `ainterlinked tasks stale`: oldest first, with what to
// do about them.
func StaleTasks(l core.StaleList) string {
	if len(l.Tasks) == 0 {
		return fmt.Sprintf("No open task untouched for %dd+.", l.Days)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d open task(s) untouched for %dd+, oldest first:\n", len(l.Tasks), l.Days)
	for _, t := range l.Tasks {
		fmt.Fprintf(&b, "#%d [%s] P%d %s · %dd untouched · %s", t.ID, t.Status, t.Priority, t.Title, t.Untouched, t.Board)
		if t.CreatedBy != "" {
			fmt.Fprintf(&b, " · by %s", t.CreatedBy)
		}
		if t.Assignee != "" {
			fmt.Fprintf(&b, " · @%s", t.Assignee)
		}
		b.WriteString("\n")
	}
	b.WriteString("Drop one: ainterlinked task cancel <id> · back to todo: ainterlinked task reopen <id> · any update restarts its count.")
	return b.String()
}
