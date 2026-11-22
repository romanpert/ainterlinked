package render

import (
	"fmt"
	"strings"

	"github.com/romanpert/ainterlinked/internal/core"
)

// Machine renders this machine's load as one line, or "" when no session
// publishes it.
func Machine(m core.Machine) string {
	if !m.Fresh {
		return ""
	}
	parts := []string{}
	switch {
	case m.CPU < 0:
		parts = append(parts, "CPU unknown")
	case m.Approx:
		parts = append(parts, fmt.Sprintf("CPU ~%.0f%% (load average)", m.CPU))
	default:
		parts = append(parts, fmt.Sprintf("CPU %.0f%%", m.CPU))
	}
	if m.Cores > 0 {
		parts = append(parts, fmt.Sprintf("%d cores", m.Cores))
	}
	if m.MemTotal > 0 {
		parts = append(parts, fmt.Sprintf("memory %s free of %s", gb(m.MemAvail), gb(m.MemTotal)))
	}
	if m.DiskTotal > 0 {
		parts = append(parts, fmt.Sprintf("disk %s free of %s", gb(m.DiskFree), gb(m.DiskTotal)))
	}
	return fmt.Sprintf("This machine (by %s, %s ago): %s", m.By, Ago(m.SampledAt), strings.Join(parts, " · "))
}

func gb(n uint64) string {
	g := float64(n) / (1 << 30)
	if g >= 100 {
		return fmt.Sprintf("%.0f GB", g)
	}
	return fmt.Sprintf("%.1f GB", g)
}
