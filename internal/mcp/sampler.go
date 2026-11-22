package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/romanpert/ainterlinked/internal/config"
	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/hub"
	"github.com/romanpert/ainterlinked/internal/machine"
)

// firstSpan is how long the first reading watches the CPU, which is a
// difference between two counters.
const firstSpan = 300 * time.Millisecond

// maxShareSeconds keeps an interval inside what a duration can hold.
const maxShareSeconds = 365 * 24 * 3600

// shareLoad publishes this machine's load every interval until the session
// ends or the user has it stop (every 0). It runs only in the session the
// user asked; no other session reads counters.
func (s *Server) shareLoad(ctx context.Context, every time.Duration) (string, error) {
	s.mu.Lock()
	stop := s.stopLoad
	s.stopLoad = nil
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	if every <= 0 {
		var res core.SampleResult
		if err := s.client.Call(ctx, "machine_sample", core.MachineSample{Agent: s.agentID(), Stop: true}, &res); err != nil {
			return "", err
		}
		if !res.Taken {
			return "this session wasn't publishing the load", nil
		}
		s.noteLoad(ctx, "stopped publishing this machine's load")
		return "no longer publishing this machine's load", nil
	}
	smp := &machine.Sampler{Path: config.Home()}
	if _, err := smp.Read(); err != nil { // the CPU counters' starting point
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(firstSpan):
	}
	if err := s.publishLoad(ctx, smp, every); err != nil {
		return "", err
	}
	life, cancel := context.WithCancel(s.life)
	s.mu.Lock()
	if s.stopLoad != nil { // a concurrent call started one
		s.stopLoad()
	}
	s.stopLoad = cancel
	s.mu.Unlock()
	go s.loadLoop(life, smp, every)
	s.noteLoad(ctx, fmt.Sprintf("publishes this machine's load every %s", every))
	return fmt.Sprintf("publishing this machine's load every %s while this session is open (list_agents, activity_feed and the dashboard show it)", every), nil
}

var errLoadTaken = errors.New("already publishing this machine's load")

func (s *Server) publishLoad(ctx context.Context, smp *machine.Sampler, every time.Duration) error {
	r, err := smp.Read()
	if err != nil {
		return err
	}
	var res core.SampleResult
	err = s.client.Call(ctx, "machine_sample", core.MachineSample{
		Agent: s.agentID(), EveryMS: every.Milliseconds(), CPU: r.CPU, Approx: r.Approx, Cores: r.Cores,
		MemTotal: r.MemTotal, MemAvail: r.MemAvail, DiskTotal: r.DiskTotal, DiskFree: r.DiskFree,
	}, &res)
	if err == nil && !res.Taken {
		err = fmt.Errorf("%s is %w", res.By, errLoadTaken)
	}
	return err
}

func (s *Server) loadLoop(ctx context.Context, smp *machine.Sampler, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		// Anything else (a busy hub, a reconnection) passes: the next beat retries.
		if err := s.publishLoad(ctx, smp, every); errors.Is(err, errLoadTaken) {
			return // another session took over after this one slept past its term
		}
	}
}

func (s *Server) noteLoad(ctx context.Context, what string) {
	_ = s.client.Call(ctx, "note", hub.NoteParam{Agent: s.agentID(), Kind: "load", Summary: what}, nil)
}
