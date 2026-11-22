package hooks

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/romanpert/ainterlinked/internal/config"
	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/hub"
	"github.com/romanpert/ainterlinked/internal/proc"
	"github.com/romanpert/ainterlinked/internal/session"
	"github.com/romanpert/ainterlinked/internal/transcript"
)

// wakeExit tells Claude Code to wake the session with the hook's stderr.
const wakeExit = 2

// Pace of the waiter (tests shorten it).
var (
	wakeGrace = 3 * time.Second // the Stop hook runs at the same time: let it settle the state
	wakeEvery = 5 * time.Second
)

// wakeMisses is how many times in a row the hub may not answer, wakeCall how
// long it may take, and wakeLife how long a waiter lasts: its hook's timeout
// is a day.
const (
	wakeMisses = 24
	wakeLife   = 24*time.Hour - 10*time.Minute
)

var wakeCall = 10 * time.Second

// Wait is Claude Code's background Stop hook (asyncRewake, wired only with
// delivery.wake_idle): it waits for mail addressed to the idle session by
// name and then wakes it. It ends quietly when the session works again,
// leaves, or a newer waiter takes over. Other harnesses that run Claude's
// hook files would run it in the foreground, so only Claude Code gets it.
func Wait(ctx context.Context, cfg config.Config, harness string, stdin io.Reader, stderr io.Writer) (code int) {
	defer func() {
		if r := recover(); r != nil {
			logf("panic in hook %s wake: %v", harness, r)
			code = 0
		}
	}()
	if harness != "claude-code" || !cfg.Delivery.WakeIdle || !cfg.HarnessEnabled(harness) || !waitsInBackground() {
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, wakeLife)
	defer cancel()
	in := ParseInput(readInput(ctx, stdin))
	c, err := hub.OpenWithin(cfg, LockWait)
	if err != nil {
		logf("hook %s wake: open hub: %v", harness, err)
		return 0
	}
	defer c.Close()
	text, err := waitForMail(ctx, cfg, c, harness, in)
	if err != nil {
		logf("hook %s wake: %v", harness, err)
	}
	if text == "" {
		return 0
	}
	fmt.Fprintln(stderr, text)
	return wakeExit
}

// programArgs is how the program this hook runs inside was started (tests
// replace it).
var programArgs = func() ([]string, error) {
	pid, _ := proc.Anchor()
	return proc.Args(pid)
}

// waitsInBackground says whether the program running this hook is a Claude
// Code session that runs asyncRewake hooks in the background. A headless one
// (`claude -p`) and other harnesses that run Claude's hook files would wait
// for it in the foreground, for as long as no mail comes: the environment
// is inherited by whatever a session starts, so the program itself decides.
func waitsInBackground() bool {
	if anchorName() != "claude" {
		return false
	}
	args, err := programArgs()
	if err != nil || len(args) == 0 {
		return false
	}
	streams := false
	for i, a := range args[1:] {
		next := ""
		if i+2 < len(args) {
			next = args[i+2]
		}
		switch {
		case a == "--input-format=stream-json", a == "--input-format" && next == "stream-json":
			streams = true
		case a == "--print", strings.HasPrefix(a, "--print="), shortPrint.MatchString(a):
			return false
		}
	}
	// An app that keeps the session open, or a terminal. Scripts on the SDK
	// also stream, and leave when they are done.
	switch os.Getenv("CLAUDE_CODE_ENTRYPOINT") {
	case "cli":
		return !streams
	case "claude-vscode", "claude-desktop", "claude-jetbrains":
		return streams
	}
	return false
}

var shortPrint = regexp.MustCompile(`^-[a-z]*p[a-z]*$`)

func waitForMail(ctx context.Context, cfg config.Config, c hub.Client, harness string, in Input) (string, error) {
	sc := session.Detect(cfg, harness, in.Cwd)
	var me core.Agent
	first, cancel := context.WithTimeout(ctx, wakeCall)
	err := c.Call(first, "register", sc.Register("hook", "push", in.SessionID), &me)
	cancel()
	if err != nil {
		return "", err
	}
	mine := takeWatch(me.ID)
	if !pause(ctx, wakeGrace) {
		return "", nil
	}
	for misses := 0; ; {
		if !mine() || !proc.Alive(sc.AnchorPID) {
			return "", nil
		}
		round, cancel := context.WithTimeout(ctx, wakeCall)
		text, done, err := tryWake(round, cfg, c, me.ID)
		cancel()
		if text != "" || done {
			return text, nil
		}
		if misses++; err == nil {
			misses = 0
		} else if misses > wakeMisses {
			return "", err
		}
		if !pause(ctx, wakeEvery) {
			return "", nil
		}
	}
}

// tryWake takes the mail that may wake the agent. done: there is nothing
// left to wait for.
func tryWake(ctx context.Context, cfg config.Config, c hub.Client, id string) (text string, done bool, err error) {
	var a core.Agent
	if err := c.Call(ctx, "agent", hub.IDParam{ID: id}, &a); err != nil {
		return "", false, err
	}
	if !a.Online || (a.State != "idle" && a.State != "done") {
		return "", true, nil
	}
	var u hub.UnreadResult
	if err := c.Call(ctx, "unread", hub.AgentParam{Agent: id}, &u); err != nil || u.Direct == 0 {
		return "", false, err
	}
	// Mail waits. The terms are read now, not when the waiter started: the
	// user may have turned waking off since.
	if now, err := config.LoadFile(); err == nil {
		cfg.Delivery, cfg.Limits = now.Delivery, now.Limits
	}
	if !cfg.Delivery.WakeIdle || cfg.Limits.HeldOut(Holds(id)) {
		return "", true, nil
	}
	var p core.Prefs
	_ = c.Call(ctx, "prefs", hub.AgentParam{Agent: id}, &p)
	if p.Inject == core.InjectManual || overBudget(ctx, c, a) != "" {
		return "", false, nil
	}
	// Who starts this turn goes on record before it starts: were the mail
	// taken first and the record to fail, peers' work would pass as the user's.
	var allowed bool
	if err := c.Call(ctx, "wake", hub.AgentParam{Agent: id}, &allowed); err != nil || !allowed {
		return "", false, err
	}
	text = Inject(ctx, c, id, cfg.Delivery, p, transcript.WakeMark+" Peer agents wrote to you while you were idle. Handle it (reply with send_message if useful), then stop.\n\n", true)
	if text == "" {
		return "", false, nil
	}
	BumpHolds(id)
	_ = c.Call(ctx, "status", core.StatusReq{ID: id, State: "working", Quiet: true}, nil)
	return text, true, nil
}

// takeWatch makes this process the agent's one waiter and returns whether
// it still is: each turn's end starts a new one.
func takeWatch(id string) func() bool {
	p := filepath.Join(config.Home(), "wake", filepath.Base(id))
	token := fmt.Sprint(os.Getpid(), " ", time.Now().UnixNano())
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	if err := os.WriteFile(p, []byte(token), 0o600); err != nil {
		return func() bool { return false }
	}
	return func() bool {
		b, err := os.ReadFile(p)
		return err == nil && strings.TrimSpace(string(b)) == token
	}
}

func pause(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
