package hub

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/romanpert/ainterlinked/internal/config"
	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/env"
	"github.com/romanpert/ainterlinked/internal/proc"
)

// The hub process: one per machine (per AINTERLINKED_HOME), started by the
// first client that finds none, and the only one that opens the database.
// Clients (MCP servers, hooks, the CLI, the dashboard) talk to it over a
// local socket, and it learns who each one is from the kernel (serve.go).
// It is also cheaper: a hook no longer opens SQLite on every tool call.

var errLocked = errors.New("locked by another process")

// SocketPath and LockPath live next to the database.
func SocketPath() string { return filepath.Join(config.Home(), "hub.sock") }

// LockPath is held by the running hub process.
func LockPath() string { return filepath.Join(config.Home(), "hub.lock") }

const (
	idleExit   = 30 * time.Minute // nobody connected this long: exit
	checkEvery = 20 * time.Second
	// handoversPerTick bounds the transcripts read for handovers per check (a
	// tenth of a second each): enough that closing thirty sessions at once
	// leaves them all handed over within a minute.
	handoversPerTick = 10
)

// ErrRunning: another hub process holds this hub.
var ErrRunning = errors.New("a hub process is already running for this hub")

// RunDaemon runs the hub process until ctx ends, it idles out, or its
// binary is replaced (a new install: the next client starts the new one).
func RunDaemon(ctx context.Context, cfg config.Config) error {
	if err := os.MkdirAll(config.Home(), 0o700); err != nil {
		return err
	}
	lock, err := lockFile(LockPath())
	if errors.Is(err, errLocked) {
		return ErrRunning
	}
	if err != nil {
		return err
	}
	defer lock.Close()
	st, err := core.Open(config.DBPath(), storeOptions(cfg, 0))
	if err != nil {
		return err
	}
	defer st.Close()
	st.SingleConn()
	sock := SocketPath()
	_ = os.Remove(sock) // a crashed hub's leftover: we hold the lock, it's ours
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	_ = os.Chmod(sock, 0o600)
	srv := NewServer(st)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	go srv.watch(ctx, cancel)
	go srv.wakeLoop(ctx)
	return srv.Serve(ctx, ln)
}

// watch ends the process when its binary changes or nobody uses it, and
// locks the database for the hub alone once every agent online talks
// through it (agents of an older binary still open the file directly).
func (s *Server) watch(ctx context.Context, stop func()) {
	exe, _ := os.Executable()
	stamp := fileStamp(exe)
	terms := "" // of the config file the limits come from
	idleSince := time.Now()
	exclusive := false
	pruned := time.Time{}
	staleScan := time.Time{} // last look for forgotten tasks
	t := time.NewTicker(checkEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if stamp != "" && fileStamp(exe) != stamp {
			// A new install. In-flight calls get a moment; clients reconnect.
			for i := 0; i < 50 && s.active.Load() > 0; i++ {
				time.Sleep(100 * time.Millisecond)
			}
			stop()
			return
		}
		if next := s.followLimits(terms); next != terms {
			terms, staleScan = next, time.Time{} // a new setting is applied at once
		}
		if s.Busy() {
			idleSince = time.Now()
		} else if time.Since(idleSince) > idleExit {
			stop()
			return
		}
		// Sessions that left hand their work over from their transcripts,
		// a few per tick: reading a transcript is the hub's work, not theirs.
		_, _ = s.Store.WriteHandovers(ctx, handoversPerTick, handoverIn)
		staleScan = s.noticeStale(ctx, staleScan)
		if time.Since(pruned) > time.Hour {
			_ = s.Store.Prune(ctx, core.Retention)
			s.archiveTick(ctx)
			pruned = time.Now()
		}
		if !exclusive {
			if direct, err := s.Store.DirectUsers(ctx, s.registeredIDs()); err == nil && direct == 0 {
				exclusive = s.Store.Exclusive(ctx) == nil
			}
		}
	}
}

// followLimits applies the user's [limits] and [tasks], and whether idle sessions may
// be woken, when config.toml has changed since stamp. A file that can't be read (mid-edit, a typo) leaves them as they are.
func (s *Server) followLimits(stamp string) string {
	now := fileStamp(config.Path())
	if now == stamp {
		return stamp
	}
	if cfg, err := config.LoadFile(); err == nil {
		s.Store.SetLimits(cfg.Limits)
		s.wakeIdle.Store(cfg.Delivery.WakeIdle)
		s.archiveDays.Store(int64(cfg.Tasks.ArchiveAfterDays))
		s.Store.SetStaleAfterDays(cfg.Tasks.StaleAfterDays)
	}
	return now
}

func fileStamp(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprint(fi.Size(), fi.ModTime().UnixNano())
}

// OpenAuto returns a client of the hub process, starting it if needed.
// With no hub process possible (spawning disabled or failed) it opens the
// database in-process as before, unless a hub process holds it: going
// around a running hub would go around its identity checks.
func OpenAuto(cfg config.Config, busy time.Duration) (Client, error) {
	sock := SocketPath()
	if r, err := Dial(sock); err == nil {
		return r, nil
	}
	if spawnable() {
		if err := spawn(); err == nil {
			for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
				if r, err := Dial(sock); err == nil {
					return r, nil
				}
			}
		}
	}
	f, err := lockFile(LockPath())
	if errors.Is(err, errLocked) {
		return nil, errors.New("ainterlinked: the hub process is running but not answering; try again, or run `ainterlinked doctor`")
	}
	if f != nil {
		f.Close()
	}
	return openLocal(cfg, busy)
}

// spawnable: only the real binary starts a hub process (not a test
// binary), and AINTERLINKED_HUB_PROCESS=0 turns it off.
func spawnable() bool {
	if env.Get("HUB_PROCESS") == "0" {
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(exe)), ".exe")
	return name == "ainterlinked" || name == "interlinked" // the name before the rename
}

func spawn() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.Home(), 0o700); err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(config.Home(), "hub.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(exe, "hub")
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Dir = config.Home()
	detach(cmd)
	if err := startDetached(cmd); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func storeOptions(cfg config.Config, busy time.Duration) core.Options {
	o := core.Options{
		Stale:   time.Duration(cfg.Presence.StaleSeconds) * time.Second,
		Machine: cfg.Machine,
		Alive:   proc.AliveSet,
		Busy:    busy,
		Limits:  &cfg.Limits,
	}
	o.StaleAfterDays = cfg.Tasks.StaleAfterDays
	return o
}

// openLocal is the in-process hub (no hub process).
func openLocal(cfg config.Config, busy time.Duration) (Client, error) {
	st, err := core.Open(config.DBPath(), storeOptions(cfg, busy))
	if err != nil {
		return nil, err
	}
	return NewLocal(st), nil
}

// staleEvery is how often the hub looks for forgotten tasks: their age is
// counted in days.
const staleEvery = time.Hour

// noticeStale tells about tasks untouched past [tasks] stale_after_days
// (nothing when it is off) if the last look, at last, is staleEvery old.
func (s *Server) noticeStale(ctx context.Context, last time.Time) time.Time {
	if !last.IsZero() && time.Since(last) < staleEvery {
		return last
	}
	_, _ = s.Store.NoticeStaleTasks(ctx)
	return time.Now()
}
