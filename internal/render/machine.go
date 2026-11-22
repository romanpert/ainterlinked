package render

import (
	"fmt"
	"strings"

	"github.com/romanpert/ainterlinked/internal/core"
)

// Machine renders this machine's load as one line, or "" when no fresh
// sample exists (no session is sampling): an old figure would mislead.
func Machine(m core.Machine) string {
	if !m.Fresh {
		return ""
	}
	parts := []string{}
	switch {
	case m.CPU < 0:
		parts = append(parts, "CPU measuring")
	case m.Approx:
		parts = append(parts, fmt.Sprintf("CPU ~%.0f%% (load average)", m.CPU))
	default:
		cpu := fmt.Sprintf("CPU %.0f%%", m.CPU)
		if m.CPUAvg >= 0 {
			cpu += fmt.Sprintf(" (1m avg %.0f%%)", m.CPUAvg)
		}
		parts = append(parts, cpu)
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
	return "This machine: " + strings.Join(parts, " · ")
}

func gb(n uint64) string {
	g := float64(n) / (1 << 30)
	if g >= 100 {
		return fmt.Sprintf("%.0f GB", g)
	}
	return fmt.Sprintf("%.1f GB", g)
}
