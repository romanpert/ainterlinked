package core

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// This machine's load, as one row. Nobody reads counters unless the user
// asks a session to: that session then publishes, at the interval the user
// gave, for as long as it is open. Everything else follows from that
// interval: a figure stands until the publisher has missed a beat, and no
// longer than its session.

// Machine is this machine's last published load.
type Machine struct {
	CPU       float64 `json:"cpu"` // % of all cores since the previous sample; -1 unknown
	Approx    bool    `json:"approx,omitempty"`
	MemTotal  uint64  `json:"mem_total"`
	MemAvail  uint64  `json:"mem_avail"`
	DiskTotal uint64  `json:"disk_total"`
	DiskFree  uint64  `json:"disk_free"`
	Cores     int     `json:"cores"`
	SampledAt int64   `json:"sampled_at"`
	Sampler   string  `json:"sampler,omitempty"`      // the publishing agent
	By        string  `json:"sampler_name,omitempty"` // its name
	Fresh     bool    `json:"fresh"`                  // the publisher's term still stands
}

// MachineSample is one reading an agent publishes.
type MachineSample struct {
	Agent     string  `json:"agent"`
	EveryMS   int64   `json:"every_ms"` // the publisher's interval
	CPU       float64 `json:"cpu"`      // -1: not measured
	Approx    bool    `json:"approx,omitempty"`
	MemTotal  uint64  `json:"mem_total"`
	MemAvail  uint64  `json:"mem_avail"`
	DiskTotal uint64  `json:"disk_total"`
	DiskFree  uint64  `json:"disk_free"`
	Cores     int     `json:"cores"`
	Stop      bool    `json:"stop,omitempty"` // stop publishing; the figures are ignored
}

// SampleResult says whether the sample was taken: false means another
// session (By) publishes, or no interval was given.
type SampleResult struct {
	Taken bool   `json:"taken"`
	By    string `json:"by,omitempty"`
}

// maxSampleEvery keeps a nonsensical interval from overflowing the term.
const maxSampleEvery = 365 * 24 * time.Hour

// Machine returns the last published load.
func (s *Store) Machine(ctx context.Context) (Machine, error) {
	m := Machine{CPU: -1}
	var approx, online int
	var until int64
	err := s.db.QueryRowContext(ctx, `SELECT m.cpu,m.approx,m.mem_total,m.mem_avail,m.disk_total,m.disk_free,m.cores,m.sampled_at,m.sampler_id,m.lease_until,
		COALESCE(a.name,''),COALESCE(a.online,0) FROM machine m LEFT JOIN agents a ON a.id=m.sampler_id WHERE m.id=1`).
		Scan(&m.CPU, &approx, &m.MemTotal, &m.MemAvail, &m.DiskTotal, &m.DiskFree, &m.Cores, &m.SampledAt, &m.Sampler, &until, &m.By, &online)
	if errors.Is(err, sql.ErrNoRows) {
		return m, nil
	}
	m.Approx = approx == 1
	m.Fresh = online == 1 && s.ms() < until
	return m, err
}

// SampleMachine publishes r, or stops publishing. One agent publishes at a
// time: another takes over once the publisher stops, leaves or lets its term
// lapse.
func (s *Store) SampleMachine(ctx context.Context, r MachineSample) (SampleResult, error) {
	var out SampleResult
	if r.EveryMS > maxSampleEvery.Milliseconds() {
		return out, errors.New("the interval is too long")
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		now := s.ms()
		var holder, name string
		var until int64
		var online int
		if err := tx.QueryRow(`SELECT m.sampler_id, m.lease_until, COALESCE(a.name,''), COALESCE(a.online,0)
			FROM machine m LEFT JOIN agents a ON a.id=m.sampler_id WHERE m.id=1`).Scan(&holder, &until, &name, &online); err != nil {
			return err
		}
		live := online == 1 && now < until
		mine := holder == r.Agent && live
		if r.Stop {
			if !mine {
				return nil
			}
			out.Taken = true
			_, err := tx.Exec(`UPDATE machine SET sampler_id='', lease_until=0 WHERE id=1`)
			return err
		}
		if live && !mine {
			out.By = name
			return nil
		}
		if r.EveryMS <= 0 {
			return nil // no term, no publication (a session older than publishing on request)
		}
		out.Taken = true
		cpu, approx := min(r.CPU, 100), 0
		if r.Approx {
			approx = 1
		}
		// Stands until the next sample is a whole interval late.
		_, err := tx.Exec(`UPDATE machine SET cpu=?, cpu_avg=-1, approx=?, mem_total=?, mem_avail=?, disk_total=?, disk_free=?, cores=?, sampled_at=?, sampler_id=?, lease_until=? WHERE id=1`,
			cpu, approx, r.MemTotal, min(r.MemAvail, r.MemTotal), r.DiskTotal, min(r.DiskFree, r.DiskTotal), r.Cores, now, r.Agent, now+2*r.EveryMS)
		return err
	})
	return out, err
}
