package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/romanpert/ainterlinked/internal/config"
	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/harness"
	"github.com/romanpert/ainterlinked/internal/hub"
	"github.com/romanpert/ainterlinked/internal/render"
	"github.com/romanpert/ainterlinked/internal/web"
)

func openHub() (hub.Client, config.Config, error) {
	cfg := loadConfig()
	c, err := hub.Open(cfg)
	return c, cfg, err
}

// human returns the operator's agent id, registering it on first use.
func human(ctx context.Context, c hub.Client, cfg config.Config) (string, error) {
	a, err := web.Human(ctx, c, cfg.Machine)
	return a.ID, err
}

func overview(ctx context.Context) error {
	c, _, err := openHub()
	if err != nil {
		return err
	}
	defer c.Close()
	fmt.Printf("ainterlinked %s · hub %s\n\n", Version, tilde(config.DBPath()))
	var agents []core.Agent
	if err := c.Call(ctx, "agents", core.AgentFilter{}, &agents); err != nil {
		return err
	}
	fmt.Println(render.Agents(agents, ""))
	e := env()
	var wired, found []string
	for _, h := range harness.All() {
		w := h.Wired(e)
		if w.MCP || w.Hooks {
			wired = append(wired, h.ID)
		} else if len(h.Detect(e)) > 0 {
			found = append(found, h.ID)
		}
	}
	fmt.Printf("\nWired: %s\n", orNone(wired))
	if len(found) > 0 {
		fmt.Printf("Detected, not wired: %s  →  ainterlinked install\n", strings.Join(found, ", "))
	}
	fmt.Println("\nRun `ainterlinked help` for all commands.")
	return nil
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "none yet (run: ainterlinked install)"
	}
	return strings.Join(s, ", ")
}

func cmdAgents(ctx context.Context, args []string) error {
	fs := newFlags("agents")
	all := fs.Bool("all", false, "include agents seen in the last 7 days")
	project := fs.String("project", "", "only this project")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	c, _, err := openHub()
	if err != nil {
		return err
	}
	defer c.Close()
	var list []core.Agent
	if err := c.Call(ctx, "agents", core.AgentFilter{IncludeOffline: *all, Project: *project}, &list); err != nil {
		return err
	}
	fmt.Println(render.Agents(list, ""))
	return nil
}

func cmdSend(ctx context.Context, args []string) error {
	fs := newFlags("send")
	subject := fs.String("subject", "", "short subject")
	urgent := fs.Bool("urgent", false, "ask recipients to look now")
	reply := fs.Int64("reply-to", 0, "message id to answer")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 2 && *reply == 0 {
		return errors.New(`usage: ainterlinked send <to> <message...>   (to: agent name, project:<p>, harness:<h>, "*")`)
	}
	c, cfg, err := openHub()
	if err != nil {
		return err
	}
	defer c.Close()
	me, err := human(ctx, c, cfg)
	if err != nil {
		return err
	}
	to, body := "", strings.Join(pos, " ")
	if len(pos) >= 2 {
		to, body = pos[0], strings.Join(pos[1:], " ")
	}
	if body == "-" { // read the body from stdin
		b, _ := ioReadAll(os.Stdin)
		body = string(b)
	}
	var m core.Message
	if err := c.Call(ctx, "send", core.SendReq{From: me, To: to, Body: body, Subject: *subject, Urgent: *urgent, ReplyTo: *reply}, &m); err != nil {
		return err
	}
	fmt.Printf("sent #%d to %s\n", m.ID, strings.Join(append(append([]string{}, m.Delivered...), m.Briefed...), ", "))
	if len(m.Away) > 0 {
		fmt.Printf("not active now (they read it when their session resumes): %s\n", strings.Join(m.Away, ", "))
	}
	return nil
}

func ioReadAll(f *os.File) ([]byte, error) {
	var b bytes.Buffer
	_, err := b.ReadFrom(f)
	return b.Bytes(), err
}

func cmdInbox(ctx context.Context, args []string) error {
	fs := newFlags("inbox")
	all := fs.Bool("all", false, "include read messages")
	peek := fs.Bool("peek", false, "don't mark as read")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	c, cfg, err := openHub()
	if err != nil {
		return err
	}
	defer c.Close()
	me, err := human(ctx, c, cfg)
	if err != nil {
		return err
	}
	var list []core.Message
	if err := c.Call(ctx, "inbox", core.InboxReq{Agent: me, All: *all, Peek: *peek, Limit: 50}, &list); err != nil {
		return err
	}
	fmt.Println(render.Messages(list, 0))
	return nil
}

func cmdTasks(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "archive" {
		return cmdTasksArchive(ctx, args[1:])
	}
	if len(args) > 0 && args[0] == "stale" {
		return cmdTasksStale(ctx, args[1:])
	}
	fs := newFlags("tasks")
	board := fs.String("board", "*", "board name, * for all")
	open := fs.Bool("open", false, "hide done/cancelled (the default without --status)")
	status := fs.String("status", "", "only this status (done or cancelled lists closed tasks)")
	query := fs.String("query", "", "search every task, archived ones too, by any text")
	limit := fs.Int("limit", 0, "max search hits (default 20)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		*board = pos[0]
	}
	c, _, err := openHub()
	if err != nil {
		return err
	}
	defer c.Close()
	var page core.TaskPage
	if err := c.Call(ctx, "task_list", core.TaskFilter{Board: *board, Open: *open, Status: *status, Query: *query, Limit: *limit}, &page); err != nil {
		return err
	}
	fmt.Println(render.TaskPage(page))
	return nil
}

func cmdTask(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: ainterlinked task add|claim|done|block|cancel|reopen ...")
	}
	sub, rest := args[0], args[1:]
	c, cfg, err := openHub()
	if err != nil {
		return err
	}
	defer c.Close()
	me, err := human(ctx, c, cfg)
	if err != nil {
		return err
	}
	if sub == "add" {
		fs := newFlags("task add")
		board := fs.String("board", "", "board (default: the current folder's project)")
		prio := fs.Int("prio", 2, "0 urgent, 1 high, 2 normal, 3 low")
		after := fs.String("after", "", "comma-separated task ids this depends on")
		label := fs.String("label", "", "comma-separated labels")
		assign := fs.String("assign", "", "assign to an agent")
		body := fs.String("body", "", "details")
		pos, err := parse(fs, rest)
		if err != nil {
			return err
		}
		if len(pos) == 0 {
			return errors.New(`usage: ainterlinked task add "title" [--board b] [--prio n]`)
		}
		if *board == "" {
			wd, _ := os.Getwd()
			if root := gitRoot(wd); root != "" {
				wd = root
			}
			*board = filepath.Base(wd)
		}
		var deps []int64
		for _, d := range strings.Split(*after, ",") {
			if n, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(d), "#"), 10, 64); err == nil {
				deps = append(deps, n)
			}
		}
		var labels []string
		for _, l := range strings.Split(*label, ",") {
			if l = strings.TrimSpace(l); l != "" {
				labels = append(labels, l)
			}
		}
		var t core.Task
		if err := c.Call(ctx, "task_create", core.TaskReq{By: me, Board: *board, Title: strings.Join(pos, " "), Body: *body,
			Priority: prio, DependsOn: deps, Labels: labels, Assignee: *assign}, &t); err != nil {
			return err
		}
		fmt.Println("created", render.Task(t))
		return nil
	}
	fs := newFlags("task " + sub)
	result := fs.String("result", "", "outcome / notes")
	pos, err := parse(fs, rest)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: ainterlinked task %s <id>", sub)
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(pos[0], "#"), 10, 64)
	if err != nil {
		return fmt.Errorf("bad task id %q", pos[0])
	}
	var t core.Task
	if sub == "claim" {
		err = c.Call(ctx, "task_claim", hub.ClaimParam{Agent: me, ID: id}, &t)
	} else {
		status := map[string]string{"done": "done", "block": "blocked", "cancel": "cancelled", "reopen": "todo", "review": "review", "start": "doing"}[sub]
		if status == "" {
			return fmt.Errorf("unknown task command %q", sub)
		}
		err = c.Call(ctx, "task_update", core.TaskUpdate{ID: id, By: me, Status: status, Result: *result}, &t)
	}
	if err != nil {
		return err
	}
	fmt.Println(render.Task(t))
	return nil
}

func gitRoot(dir string) string {
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		p := filepath.Dir(d)
		if p == d {
			return ""
		}
		d = p
	}
}

func cmdLocks(ctx context.Context, args []string) error {
	c, _, err := openHub()
	if err != nil {
		return err
	}
	defer c.Close()
	var list []core.Lock
	if err := c.Call(ctx, "locks", hub.ProjectParam{}, &list); err != nil {
		return err
	}
	fmt.Println(render.Locks(list))
	return nil
}

func cmdFeed(ctx context.Context, args []string) error {
	fs := newFlags("feed")
	follow := fs.Bool("f", false, "follow")
	n := fs.Int("n", 40, "events to show")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	c, _, err := openHub()
	if err != nil {
		return err
	}
	defer c.Close()
	var list []core.Event
	if err := c.Call(ctx, "feed", core.FeedReq{Limit: *n}, &list); err != nil {
		return err
	}
	if len(list) > 0 {
		fmt.Println(render.Feed(list))
	}
	if !*follow {
		if len(list) == 0 {
			fmt.Println(render.Feed(nil))
		}
		return nil
	}
	var last int64
	if len(list) > 0 {
		last = list[len(list)-1].ID
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(700 * time.Millisecond):
		}
		var more []core.Event
		if err := c.Call(ctx, "feed", core.FeedReq{SinceID: last, Limit: 200}, &more); err != nil {
			continue
		}
		if len(more) > 0 {
			fmt.Println(render.Feed(more))
			last = more[len(more)-1].ID
		}
	}
}

func cmdServe(ctx context.Context, args []string) error {
	cfg := loadConfig()
	fs := newFlags("serve")
	listen := fs.String("listen", cfg.Server.Listen, "loopback address to serve the dashboard on")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	client, err := hub.Open(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	srv := &web.Server{Hub: client, Machine: cfg.Machine, Version: Version, Scan: func() []web.HarnessStatus {
		e, cur := env(), loadConfig()
		var out []web.HarnessStatus
		for _, h := range harness.All() {
			w := h.Wired(e)
			out = append(out, web.HarnessStatus{ID: h.ID, Name: h.Name, Detected: len(h.Detect(e)) > 0, MCP: w.MCP, Hooks: w.Hooks,
				HasHooks: h.Hooks, Tier: h.Tier, Enabled: cur.HarnessEnabled(h.ID)})
		}
		return out
	}}
	if !web.IsLoopback(*listen) {
		return fmt.Errorf("serve listens on this machine only (e.g. 127.0.0.1:7717), not %s", *listen)
	}
	fmt.Printf("ainterlinked dashboard: http://%s\n", *listen)
	if local, ok := client.(*hub.Local); ok { // with a hub process, it prunes
		go func() {
			t := time.NewTicker(time.Hour)
			defer t.Stop()
			for {
				_ = local.Store.Prune(ctx, core.Retention)
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
	return srv.Listen(ctx, *listen)
}

func cmdDoctor(ctx context.Context, args []string) error {
	ok, bad := 0, 0
	check := func(name string, err error, detail string) {
		if err != nil {
			bad++
			fmt.Printf("  ✗ %s: %v\n", name, err)
			return
		}
		ok++
		fmt.Printf("  ✓ %s%s\n", name, prefix(" — ", detail))
	}
	fmt.Printf("ainterlinked %s doctor\n", Version)
	cfg, err := config.Load()
	check("config", err, tilde(config.Path()))
	bin, warn := stableBin()
	var binErr error
	if warn != "" {
		binErr = errors.New(warn)
	}
	check("binary", binErr, filepath.ToSlash(bin))

	start := time.Now()
	c, err := hub.Open(cfg)
	if err != nil {
		check("hub", err, "")
		return fmt.Errorf("%d problem(s)", bad+0)
	}
	defer c.Close()
	check("hub mode", nil, hubMode(ctx, c))
	me, err := human(ctx, c, cfg)
	var n []core.Agent
	if err == nil {
		err = c.Call(ctx, "agents", core.AgentFilter{}, &n)
	}
	check("hub", err, fmt.Sprintf("%s, %d agent(s) online, round trip %s", tilde(config.DBPath()), len(n), time.Since(start).Round(time.Millisecond)))
	if cfg.OldHub != "" {
		check("config", fmt.Errorf("hub = %q in %s is ignored: this machine uses its own hub (the next save drops it)", cfg.OldHub, tilde(config.Path())), "")
	}
	if config.LegacyHome() {
		check("data directory", fmt.Errorf("%s is the name from before the rename; with every agent session closed, rename it to %s (nothing else changes)",
			tilde(config.Home()), config.DirName), "")
	}
	_ = me

	// Hook latency: a hook must be fast because it runs on every tool call.
	t0 := time.Now()
	hc := exec.Command(bin, "hook", "doctor", "PostToolUse")
	hc.Stdin = strings.NewReader(`{"session_id":"doctor","cwd":"."}`)
	_, herr := hc.CombinedOutput()
	check("hook process", herr, fmt.Sprintf("%s per call", time.Since(t0).Round(time.Millisecond)))
	end := exec.Command(bin, "hook", "doctor", "SessionEnd") // don't leave a "doctor" agent behind
	end.Stdin = strings.NewReader(`{"session_id":"doctor","cwd":"."}`)
	_ = end.Run()

	e := harness.NewEnv(config.UserHome(), bin)
	fmt.Println("harnesses")
	for _, h := range harness.All() {
		w := h.Wired(e)
		if !w.MCP && !w.Hooks && !w.Legacy {
			continue
		}
		var herr error
		if h.Hooks && !w.Hooks {
			herr = errors.New("MCP wired but hooks missing — run: ainterlinked install " + h.ID)
		}
		if herr == nil && w.Legacy {
			herr = errors.New("still wired under the old name interlinked — run: ainterlinked install " + h.ID)
		}
		stale := false
		for _, f := range w.Files {
			if b, err := os.ReadFile(f); err == nil && bytes.Contains(b, []byte("ainterlinked")) &&
				!bytes.Contains(bytes.ReplaceAll(b, []byte(`\\`), []byte(`/`)), []byte(filepath.ToSlash(bin))) &&
				!bytes.Contains(b, []byte(strings.ReplaceAll(bin, `\`, `\\`))) && !bytes.Contains(b, []byte(bin)) {
				stale = true
			}
		}
		if herr == nil && stale {
			herr = errors.New("config points at a different ainterlinked binary — run: ainterlinked install " + h.ID)
		}
		state := "on"
		if !cfg.HarnessEnabled(h.ID) {
			state = "muted"
		}
		check(h.Name, herr, fmt.Sprintf("delivery %s, %s", h.Tier, state))
	}
	if bad > 0 {
		return fmt.Errorf("%d problem(s) found", bad)
	}
	fmt.Printf("all %d checks passed\n", ok)
	return nil
}

func prefix(p, s string) string {
	if s == "" {
		return ""
	}
	return p + s
}
