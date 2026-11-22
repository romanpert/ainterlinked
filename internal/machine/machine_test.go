package machine

import (
	"os"
	"testing"
	"time"
)

func TestParseProcStat(t *testing.T) {
	// user nice system idle iowait irq softirq steal guest guest_nice
	got, err := parseProcStat("cpu  100 10 50 800 40 0 0 0 30 0\ncpu0 1 2 3 4 5 6 7 8 9 10\n")
	if err != nil {
		t.Fatal(err)
	}
	if got.total != 1000 || got.busy != 160 {
		t.Fatalf("busy/total = %d/%d, want 160/1000 (idle and iowait are idle; guest is already in user)", got.busy, got.total)
	}
	if _, err := parseProcStat("intr 1 2 3\n"); err == nil {
		t.Fatal("no cpu line is an error")
	}
}

func TestParseMeminfo(t *testing.T) {
	total, avail, err := parseMeminfo("MemTotal:       32768000 kB\nMemFree:  1000 kB\nMemAvailable:   16384000 kB\n")
	if err != nil || total != 32768000*1024 || avail != 16384000*1024 {
		t.Fatalf("%d %d %v", total, avail, err)
	}
}

func TestParseVMStat(t *testing.T) {
	out := `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                               10000.
Pages active:                            500000.
Pages inactive:                           20000.
Pages speculative:                         3000.
Pages wired down:                        100000.
`
	got, err := parseVMStat(out)
	if err != nil || got != 33000*16384 {
		t.Fatalf("%d %v", got, err)
	}
}

// A real reading on this machine: CPU from the second read on, memory and
// the disk the given path is on.
func TestReadsThisMachine(t *testing.T) {
	s := Sampler{Path: os.TempDir()}
	first, err := s.Read()
	if err == ErrUnsupported {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	second, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !first.Approx && first.CPU != -1 {
		t.Errorf("the first measured read only primes the counters: %v", first.CPU)
	}
	if second.CPU < 0 || second.CPU > 100 {
		t.Errorf("cpu %v", second.CPU)
	}
	if second.MemTotal == 0 || second.MemAvail > second.MemTotal || second.Cores < 1 {
		t.Errorf("memory/cores: %+v", second)
	}
	if second.DiskTotal == 0 || second.DiskFree > second.DiskTotal {
		t.Errorf("disk: %+v", second)
	}
	t.Logf("%+v", second)
}
