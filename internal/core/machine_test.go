package core

import (
	"context"
	"math"
	"testing"
	"time"
)

// One session samples at a time; another takes over only once the lease
// lapses. The first sample of a new sampler primes (CPU unknown) without
// passing off a long-gone sampler's CPU as current.
func TestOneSamplerAtATime(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()
	s.now = func() time.Time { return now }
	if m, _ := s.Machine(ctx); m.Fresh || m.CPU != -1 {
		t.Fatalf("no sample yet: %+v", m)
	}
	sample := func(agent string, cpu float64) bool {
		t.Helper()
		r, err := s.SampleMachine(ctx, MachineSample{Agent: agent, CPU: cpu, MemTotal: 64, MemAvail: 20, DiskTotal: 100, DiskFree: 40, Cores: 12})
		if err != nil {
			t.Fatal(err)
		}
		return r.Taken
	}
	if !sample("a", -1) {
		t.Fatal("a free lease is taken")
	}
	if m, _ := s.Machine(ctx); !m.Fresh || m.CPU != -1 || m.MemAvail != 20 || m.Sampler != "a" {
		t.Fatalf("priming records memory and disk, CPU still unknown: %+v", m)
	}
	now = now.Add(SampleEvery)
	if sample("b", 50) {
		t.Fatal("b must not sample while a holds the lease")
	}
	if !sample("a", 80) {
		t.Fatal("the holder renews")
	}
	if m, _ := s.Machine(ctx); m.CPU != 80 || m.CPUAvg != 80 {
		t.Fatalf("first measured sample: %+v", m)
	}
	now = now.Add(30 * time.Second) // half the averaging window
	sample("a", 20)
	if m, _ := s.Machine(ctx); m.CPU != 20 || math.Abs(m.CPUAvg-50) > 0.01 {
		t.Fatalf("the average moves by the time elapsed: %+v", m)
	}

	now = now.Add(samplerLease) // a's session ended
	if m, _ := s.Machine(ctx); !m.SamplerFree("b", now) || m.SamplerFree("b", now.Add(-time.Second)) {
		t.Fatalf("the lease lapses %s after the last sample: %+v", samplerLease, m)
	}
	now = now.Add(MachineFresh - samplerLease)
	if m, _ := s.Machine(ctx); m.Fresh {
		t.Fatalf("a sample older than %s is stale: %+v", MachineFresh, m)
	}
	if !sample("b", -1) {
		t.Fatal("b takes over a lapsed lease")
	}
	if m, _ := s.Machine(ctx); m.CPU != -1 || m.CPUAvg != -1 || m.Sampler != "b" {
		t.Fatalf("a long-gone sampler's CPU is not shown as current: %+v", m)
	}
}
