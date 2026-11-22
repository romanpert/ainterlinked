package core

import (
	"context"
	"testing"
	"time"
)

// Nothing is published until an agent does, on the interval it declares; one
// publishes at a time, and the figure stands no longer than its term or its
// session.
func TestLoadIsPublishedOnTheTermsThePublisherDeclares(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()
	s.now = func() time.Time { return now }
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "shop", AnchorPID: 11})
	b := reg(t, s, RegisterReq{Harness: "codex", Project: "shop", AnchorPID: 22})
	if m, err := s.Machine(ctx); err != nil || m.Fresh || m.CPU != -1 {
		t.Fatalf("nothing published: %+v %v", m, err)
	}
	const every = 7 * time.Second // whatever the user said
	publish := func(agent string, every time.Duration) SampleResult {
		t.Helper()
		r, err := s.SampleMachine(ctx, MachineSample{Agent: agent, EveryMS: every.Milliseconds(), CPU: 140, MemTotal: 64, MemAvail: 70, DiskTotal: 100, DiskFree: 40, Cores: 12})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if publish(a.ID, 0).Taken {
		t.Fatal("no interval, no publication: we don't pick one")
	}
	if !publish(a.ID, every).Taken {
		t.Fatal("a publishes")
	}
	if m, _ := s.Machine(ctx); !m.Fresh || m.CPU != 100 || m.MemAvail != 64 || m.Sampler != a.ID || m.By != a.Name {
		t.Fatalf("published, clamped: %+v", m)
	}
	if r := publish(b.ID, every); r.Taken || r.By != a.Name {
		t.Fatalf("a publishes already: %+v", r)
	}
	now = now.Add(2*every - time.Millisecond)
	if m, _ := s.Machine(ctx); !m.Fresh {
		t.Fatalf("the term stands: %+v", m)
	}
	now = now.Add(time.Millisecond)
	if m, _ := s.Machine(ctx); m.Fresh {
		t.Fatalf("a whole interval late: %+v", m)
	}
	if !publish(b.ID, every).Taken {
		t.Fatal("b takes over a lapsed term")
	}
	if r, _ := s.SampleMachine(ctx, MachineSample{Agent: a.ID, Stop: true}); r.Taken {
		t.Fatal("a can't stop b")
	}
	if r, _ := s.SampleMachine(ctx, MachineSample{Agent: b.ID, Stop: true}); !r.Taken {
		t.Fatal("b stops")
	}
	if m, _ := s.Machine(ctx); m.Fresh {
		t.Fatalf("stopped: %+v", m)
	}
	publish(a.ID, time.Hour)
	if err := s.Leave(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.Machine(ctx); m.Fresh {
		t.Fatalf("the figure doesn't outlive its publisher's session: %+v", m)
	}
	if !publish(b.ID, every).Taken {
		t.Fatal("b needn't wait out the term of an agent that left")
	}
}
