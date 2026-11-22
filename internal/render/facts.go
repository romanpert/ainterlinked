package render

import (
	"fmt"
	"strings"

	"github.com/romanpert/ainterlinked/internal/core"
)

// FactsBlock lists, under an agent list, what each agent has done lately.
// Agents with nothing to show get no line, so a quiet bus costs nothing.
func FactsBlock(list []core.Agent, facts map[string]core.Facts) string {
	var lines []string
	for _, a := range list {
		f, ok := facts[strings.ToLower(a.Name)]
		if !ok || f.Empty() {
			continue
		}
		lines = append(lines, "  "+a.Name+": "+FactsLine(f))
	}
	if len(lines) == 0 {
		return ""
	}
	return "\nLately (7 days; facts, not a score; what the user ordered is the user's):\n" + strings.Join(lines, "\n")
}

// FactsLine is one agent's facts in a line.
func FactsLine(f core.Facts) string {
	var parts []string
	if f.Taken > 0 {
		s := fmt.Sprintf("%d task%s taken", f.Taken, plural(f.Taken))
		if f.Done > 0 {
			s += fmt.Sprintf(", %d done", f.Done)
		}
		if f.Open > 0 {
			s += fmt.Sprintf(", %d open", f.Open)
		}
		parts = append(parts, s)
	}
	if f.Trespass > 0 {
		s := fmt.Sprintf("%d shell write%s to files others held, on its own", f.Trespass, plural(f.Trespass))
		if f.TrespassOpen > 0 {
			s += fmt.Sprintf(" (%d not yet settled by the holder)", f.TrespassOpen)
		} else {
			s += " (settled by the holder)"
		}
		parts = append(parts, s)
	}
	if n := len(f.Holds); n > 0 {
		shown := f.Holds
		if n > 3 {
			shown = shown[:3]
		}
		s := "holds " + strings.Join(shown, ", ")
		if n > 3 {
			s += fmt.Sprintf(" +%d", n-3)
		}
		parts = append(parts, s)
	}
	if f.Broadcasts > 0 {
		parts = append(parts, fmt.Sprintf("%d broadcast%s", f.Broadcasts, plural(f.Broadcasts)))
	}
	if f.UnreadFromViewer > 0 {
		parts = append(parts, fmt.Sprintf("%d of your messages unread", f.UnreadFromViewer))
	}
	return strings.Join(parts, " · ")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
