package mcp

import (
	"errors"
	"sync/atomic"
	"time"

	"github.com/romanpert/ainterlinked/internal/config"
	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/hub"
	"github.com/romanpert/ainterlinked/internal/machine"
)

// sampleOverride, when set, replaces core.SampleEvery (tests shorten it).
// Atomic: servers started by other tests may still be running their loop.
var sampleOverride atomic.Int64

func sampleEvery() time.Duration {
	if d := sampleOverride.Load(); d > 0 {
		return time.Duration(d)
	}
	return core.SampleEvery
}

// machineLoop keeps the hub's machine row current. Every session runs it,
// but only the one holding the sampler's lease reads counters; the others
// read one row every SampleEvery and take over when the lease lapses (the
// sampling session ended).
func (s *Server) machineLoop() {
	t := time.NewTicker(sampleEvery())
	defer t.Stop()
	var smp *machine.Sampler // nil while another session samples
	for {
		select {
		case <-s.life.Done():
			return
		case <-t.C:
		}
		id := s.agentID()
		if id == "" {
			continue
		}
		var m core.Machine
		if err := s.client.Call(s.life, "machine", hub.Empty{}, &m); err != nil || !m.SamplerFree(id, time.Now()) {
			smp = nil
			continue
		}
		if smp == nil {
			smp = &machine.Sampler{Path: config.Home()}
		}
		r, err := smp.Read()
		if errors.Is(err, machine.ErrUnsupported) {
			return
		}
		if err != nil {
			continue
		}
		var res core.SampleResult
		err = s.client.Call(s.life, "machine_sample", core.MachineSample{
			Agent: id, CPU: r.CPU, Approx: r.Approx, Cores: r.Cores,
			MemTotal: r.MemTotal, MemAvail: r.MemAvail, DiskTotal: r.DiskTotal, DiskFree: r.DiskFree,
		}, &res)
		if err != nil || !res.Taken {
			smp = nil // lost the race: the next holder primes its own counters
		}
	}
}
