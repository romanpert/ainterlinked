package core

import (
	"context"
	"database/sql"
	"time"
)

// This machine's load, as one row the hub keeps current. Reading counters is
// the cheap part; what matters is that only one process does it: the
// sampler holds a short lease, renewed with every sample, and anyone may
// take it over once it lapses (the sampler's session ended).
const (
	SampleEvery  = 5 * time.Second
	samplerLease = 3 * SampleEvery
	// MachineFresh: an older sample is shown as stale (no sampler running:
	// no session with an MCP server is open).
	MachineFresh = 30 * time.Second
	// cpuAvgWindow is the span the smoothed CPU figure follows.
	cpuAvgWindow = time.Minute
)

// Machine is this machine's last sampled load.
type Machine struct {
	CPU       float64 `json:"cpu"`     // % of all cores at the last sample; -1 unknown
	CPUAvg    float64 `json:"cpu_avg"` // smoothed over about a minute; -1 unknown
	Approx    bool    `json:"approx,omitempty"`
	MemTotal  uint64  `json:"mem_total"`
	MemAvail  uint64  `json:"mem_avail"`
	DiskTotal uint64  `json:"disk_total"`
	DiskFree  uint64  `json:"disk_free"`
	Cores     int     `json:"cores"`
	SampledAt int64   `json:"sampled_at"`
	Sampler   string  `json:"sampler,omitempty"` // the agent whose session samples
	Fresh     bool    `json:"fresh"`
}

// MachineSample is one reading reported by the sampling agent.
type MachineSample struct {
	Agent     string  `json:"agent"`
	CPU       float64 `json:"cpu"` // -1: not measured yet (the sampler is priming)
	Approx    bool    `json:"approx,omitempty"`
	MemTotal  uint64  `json:"mem_total"`
	MemAvail  uint64  `json:"mem_avail"`
	DiskTotal uint64  `json:"disk_total"`
	DiskFree  uint64  `json:"disk_free"`
	Cores     int     `json:"cores"`
}

// SampleResult says whether the sample was taken: false means another
// session holds the sampler's lease, and the caller should stop sampling.
type SampleResult struct {
	Taken bool `json:"taken"`
}

// Machine returns the last sample.
func (s *Store) Machine(ctx context.Context) (Machine, error) {
	var m Machine
	var approx int
	err := s.db.QueryRowContext(ctx, `SELECT cpu,cpu_avg,approx,mem_total,mem_avail,disk_total,disk_free,cores,sampled_at,sampler_id
		FROM machine WHERE id=1`).Scan(&m.CPU, &m.CPUAvg, &approx, &m.MemTotal, &m.MemAvail, &m.DiskTotal, &m.DiskFree, &m.Cores, &m.SampledAt, &m.Sampler)
	if err == sql.ErrNoRows {
		return Machine{CPU: -1, CPUAvg: -1}, nil
	}
	m.Approx = approx == 1
	m.Fresh = m.SampledAt > 0 && s.ms()-m.SampledAt < MachineFresh.Milliseconds()
	return m, err
}

// SamplerFree reports whether agent may sample now: it holds the lease, or
// the lease has lapsed.
func (m Machine) SamplerFree(agent string, now time.Time) bool {
	return m.Sampler == agent || now.UnixMilli()-m.SampledAt >= samplerLease.Milliseconds()
}

// SampleMachine records r if its agent holds the sampler's lease or the
// lease has lapsed, and renews the lease. A priming sample (CPU < 0) keeps
// the previous CPU figures.
func (s *Store) SampleMachine(ctx context.Context, r MachineSample) (SampleResult, error) {
	if r.CPU > 100 {
		r.CPU = 100
	}
	if r.MemAvail > r.MemTotal {
		r.MemAvail = r.MemTotal
	}
	if r.DiskFree > r.DiskTotal {
		r.DiskFree = r.DiskTotal
	}
	var out SampleResult
	err := s.tx(ctx, func(tx *sql.Tx) error {
		now := s.ms()
		var holder string
		var at int64
		var avg float64
		if err := tx.QueryRow(`SELECT sampler_id, sampled_at, cpu_avg FROM machine WHERE id=1`).Scan(&holder, &at, &avg); err != nil {
			return err
		}
		if holder != r.Agent && now-at < samplerLease.Milliseconds() {
			return nil // another session samples
		}
		out.Taken = true
		cpu, approx := r.CPU, 0
		if r.Approx {
			approx = 1
		}
		if cpu < 0 {
			if now-at >= MachineFresh.Milliseconds() {
				// The old figures are from a sampler long gone: unknown, not stale-as-current.
				if _, err := tx.Exec(`UPDATE machine SET cpu=-1, cpu_avg=-1 WHERE id=1`); err != nil {
					return err
				}
			}
			_, err := tx.Exec(`UPDATE machine SET mem_total=?, mem_avail=?, disk_total=?, disk_free=?, cores=?, sampled_at=?, sampler_id=?, lease_until=? WHERE id=1`,
				r.MemTotal, r.MemAvail, r.DiskTotal, r.DiskFree, r.Cores, now, r.Agent, now+samplerLease.Milliseconds())
			return err
		}
		// Exponential moving average over about cpuAvgWindow, weighted by the
		// time since the last sample (a gap counts for more).
		if avg < 0 || at == 0 {
			avg = cpu
		} else {
			w := float64(now-at) / float64(cpuAvgWindow.Milliseconds())
			if w > 1 {
				w = 1
			}
			avg += w * (cpu - avg)
		}
		_, err := tx.Exec(`UPDATE machine SET cpu=?, cpu_avg=?, approx=?, mem_total=?, mem_avail=?, disk_total=?, disk_free=?, cores=?, sampled_at=?, sampler_id=?, lease_until=? WHERE id=1`,
			cpu, avg, approx, r.MemTotal, r.MemAvail, r.DiskTotal, r.DiskFree, r.Cores, now, r.Agent, now+samplerLease.Milliseconds())
		return err
	})
	return out, err
}
