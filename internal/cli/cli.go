// Package cli implements the ainterlinked command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/romanpert/ainterlinked/internal/config"
	"github.com/romanpert/ainterlinked/internal/core"
	envvar "github.com/romanpert/ainterlinked/internal/env"
	"github.com/romanpert/ainterlinked/internal/harness"
	"github.com/romanpert/ainterlinked/internal/hooks"
	"github.com/romanpert/ainterlinked/internal/hub"
	"github.com/romanpert/ainterlinked/internal/mcp"
)

// Version is stamped at build time (-ldflags "-X .../cli.Version=x.y.z").
var Version = "0.1.0-dev"

const usage = `ainterlinked — a real-time bus for AI coding agents across harnesses

Setup
  install [harness...] [--dry-run]   wire every detected harness (or the ones named)
  uninstall [harness...] [--dry-run] remove ainterlinked from harnesses (all if none named)
  scan                               show detected harnesses and their wiring
  doctor                             check the installation end to end
  enable|disable <harness>           allow / mute a harness on the bus

Use (as the human operator)
  agents [--all]                     who is online, on what, in which project
  send <to> <message...>             message agents (name, project:<p>, harness:<h>, *)
  inbox [--all]                      messages agents sent to "human"
  tasks [--board b|*] [--open]       show a task board
  task add <title> [--board b] [--prio 0-3] [--after 1,2] [--label l] [--assign name]
  task claim|done|block|cancel|reopen <id> [--result text]
  locks                              active file reservations
  budget [<tokens>|off]              cap what one session may spend (billed tokens, e.g. 2M)
  shared [accept [files...]|off]     files of this repo nobody holds (declared in its covenant)
  context [<session>] [--all] [--hours N]   what fills sessions' context windows
  feed [-f] [-n 50]                  activity feed (-f follows)
  serve [--listen 127.0.0.1:port]    web dashboard for this machine
  launch <harness> [--name n] [--role r] [-- args]   start a harness with a fixed identity
                                     (launch claude also enables push into idle sessions)

Plumbing (called by harnesses)
  hub [status]                       the hub process (clients start it themselves)
  mcp [--harness h]                  stdio MCP server
  hook <harness> <event>             lifecycle hook handler

  version, config [--path]

Docs: https://github.com/romanpert/ainterlinked
`

// Main runs the CLI and returns the exit code.
func Main(args []string) int {
	mcp.Version = Version
	hub.Version = Version
	if len(args) == 0 {
		return run(overview)
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0
	case "version", "--version", "-v":
		fmt.Println("ainterlinked", Version)
		return 0
	case "mcp":
		return cmdMCP(rest)
	case "hook":
		return cmdHook(rest)
	case "launch":
		return cmdLaunch(rest)
	}
	fns := map[string]func(context.Context, []string) error{
		"install": cmdInstall, "uninstall": cmdUninstall, "scan": cmdScan, "doctor": cmdDoctor,
		"enable": cmdEnable(true), "disable": cmdEnable(false), "config": cmdConfig,
		"agents": cmdAgents, "ls": cmdAgents, "send": cmdSend, "inbox": cmdInbox,
		"tasks": cmdTasks, "task": cmdTask, "locks": cmdLocks, "feed": cmdFeed, "watch": cmdFeed,
		"serve": cmdServe, "hub": cmdHub, "budget": cmdBudget, "context": cmdContext, "shared": cmdShared,
	}
	fn, ok := fns[cmd]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	return run(func(ctx context.Context) error { return fn(ctx, rest) })
}

func run(fn func(context.Context) error) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := fn(ctx); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

// parse lets flags appear before or after positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

func loadConfig() config.Config {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err, "(using defaults)")
		cfg = config.Default()
	}
	return cfg
}

// ---------------------------------------------------------------- plumbing

func cmdMCP(args []string) int {
	fs := newFlags("mcp")
	h := fs.String("harness", "", "harness id (default: inferred from the MCP client)")
	if _, err := parse(fs, args); err != nil {
		return 2
	}
	cfg := loadConfig()
	client, err := hub.Open(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ainterlinked:", err)
		return 1
	}
	defer client.Close()
	if local, ok := client.(*hub.Local); ok {
		// Housekeeping without a daemon: each new session tidies the hub once.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = local.Store.Prune(ctx, core.Retention)
		}()
	}
	srv := mcp.New(cfg, client, *h)
	if err := srv.Serve(context.Background(), os.Stdin, os.Stdout); err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintln(os.Stderr, "ainterlinked:", err)
		return 1
	}
	return 0
}

func cmdHook(args []string) int {
	// Never fail the harness: wrong usage is silently ignored.
	if len(args) < 2 {
		return 0
	}
	hooks.Run(context.Background(), loadConfig(), args[0], args[1], os.Stdin, os.Stdout)
	return 0
}

func cmdLaunch(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ainterlinked launch <claude|codex|gemini|...> [--name n] [--role r] [-- harness args]")
		return 2
	}
	target := args[0]
	var name, role string
	var pass []string
	for i := 1; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--":
			pass = append(pass, args[i+1:]...)
			i = len(args)
		case a == "--name" && i+1 < len(args):
			name, i = args[i+1], i+1
		case a == "--role" && i+1 < len(args):
			role, i = args[i+1], i+1
		default:
			pass = append(pass, a)
		}
	}
	env := os.Environ()
	if name != "" {
		env = append(env, envvar.Var("NAME")+"="+name)
	}
	if role != "" {
		env = append(env, envvar.Var("ROLE")+"="+role)
	}
	bin := target
	switch target {
	case "claude", "claude-code":
		bin = "claude"
		if loadConfig().Delivery.ClaudeChannel {
			env = append(env, envvar.Var("CHANNEL")+"=1")
			pass = append([]string{"--dangerously-load-development-channels", "server:ainterlinked"}, pass...)
		}
	case "copilot-cli":
		bin = "copilot"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ainterlinked: %s not found on PATH\n", bin)
		return 1
	}
	c := exec.Command(path, pass...)
	c.Env, c.Stdin, c.Stdout, c.Stderr = env, os.Stdin, os.Stdout, os.Stderr
	signal.Ignore(os.Interrupt) // the child handles Ctrl-C
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "ainterlinked:", err)
		return 1
	}
	return 0
}

// ---------------------------------------------------------------- setup

// stableBin picks the binary path harness configs should point at. A copy
// running from a uvx/npx/go-run cache would vanish, so prefer the one on PATH.
func stableBin() (string, string) {
	exe, err := os.Executable()
	if err == nil {
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			exe = r
		}
	}
	if b := envvar.Get("BIN"); b != "" {
		return b, ""
	}
	low := strings.ToLower(filepath.ToSlash(exe))
	ephemeral := strings.Contains(low, "/uv/cache") || strings.Contains(low, "/archive-v") || strings.Contains(low, "go-build") ||
		strings.Contains(low, "/_npx/") || strings.Contains(low, "/appdata/local/temp/") || strings.HasPrefix(low, "/tmp/")
	if ephemeral {
		if lp, err := exec.LookPath("ainterlinked"); err == nil {
			if abs, err := filepath.Abs(lp); err == nil {
				return abs, ""
			}
		}
		return exe, "ainterlinked is running from a temporary location (" + exe + "). Install it permanently first: pipx install ainterlinked  (or: uv tool install ainterlinked)"
	}
	return exe, ""
}

func env() *harness.Env {
	bin, _ := stableBin()
	return harness.NewEnv(config.UserHome(), bin)
}

func pickHarnesses(e *harness.Env, names []string, detectedOnly bool) ([]*harness.Harness, error) {
	if len(names) == 0 {
		var out []*harness.Harness
		for _, h := range harness.All() {
			if !detectedOnly || len(h.Detect(e)) > 0 {
				out = append(out, h)
			}
		}
		return out, nil
	}
	var out []*harness.Harness
	for _, n := range names {
		h := harness.ByID(n)
		if h == nil {
			return nil, fmt.Errorf("unknown harness %q (known: %s)", n, knownIDs())
		}
		out = append(out, h)
	}
	return out, nil
}

func knownIDs() string {
	var ids []string
	for _, h := range harness.All() {
		ids = append(ids, h.ID)
	}
	return strings.Join(ids, ", ")
}

func tilde(p string) string {
	home := config.UserHome()
	if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(filepath.Join("~", rel))
	}
	return p
}

func cmdInstall(ctx context.Context, args []string) error {
	fs := newFlags("install")
	dry := fs.Bool("dry-run", false, "show what would change without writing")
	names, err := parse(fs, args)
	if err != nil {
		return err
	}
	bin, warn := stableBin()
	if warn != "" {
		fmt.Fprintln(os.Stderr, "warning:", warn)
	}
	e := harness.NewEnv(config.UserHome(), bin)
	list, err := pickHarnesses(e, names, true)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("No supported harnesses found on this machine. Supported:", knownIDs())
		return nil
	}
	cfg, err := config.LoadFile() // never overwrite a config we couldn't read
	if err != nil {
		return fmt.Errorf("%w — fix it (or move it away) and run install again", err)
	}
	fmt.Printf("ainterlinked %s → %s\n\n", Version, filepath.ToSlash(bin))
	var notes []string
	failed := 0
	for _, h := range list {
		ev := h.Detect(e)
		if len(ev) == 0 && len(names) > 0 {
			fmt.Printf("• %s: not detected, wiring anyway because you asked for it\n", h.Name)
		}
		what := "MCP"
		if h.Hooks {
			what = "MCP + hooks"
		}
		fmt.Printf("• %-26s %s, delivery %s\n", h.Name, what, h.Tier)
		tx := harness.NewTx(*dry, os.Stdout)
		h.Install(e, tx)
		changes, err := tx.Commit()
		if err != nil {
			failed++
			fmt.Printf("    FAILED: %v\n", err)
			for _, c := range changes {
				fmt.Printf("    (already done before the failure: %s %s)\n", c.Action, tilde(c.Path))
			}
			continue
		}
		for _, c := range changes {
			line := fmt.Sprintf("    %s %s", c.Action, tilde(c.Path))
			if c.Backup != "" {
				line += "  (backup: " + filepath.Base(c.Backup) + ")"
			}
			fmt.Println(line)
		}
		if len(changes) == 0 && !*dry {
			fmt.Println("    already up to date")
		}
		if h.Notes != "" {
			notes = append(notes, h.Name+": "+h.Notes)
		}
		hc := cfg.Harnesses[h.ID]
		hc.Enabled = true
		cfg.Harnesses[h.ID] = hc
	}
	if *dry {
		fmt.Println("\nDry run: nothing was written.")
		return nil
	}
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if len(notes) > 0 {
		fmt.Println("\nNotes")
		for _, n := range notes {
			fmt.Println("  -", n)
		}
	}
	fmt.Printf("\nDone. New sessions in these harnesses join the bus automatically (restart open ones).\n")
	fmt.Printf("See who's online: ainterlinked agents   ·   Dashboard: ainterlinked serve   ·   Undo: ainterlinked uninstall\n")
	if failed > 0 {
		return fmt.Errorf("%d harness(es) failed (see above; their config files were left as they were)", failed)
	}
	return nil
}

func cmdUninstall(ctx context.Context, args []string) error {
	fs := newFlags("uninstall")
	dry := fs.Bool("dry-run", false, "show what would change without writing")
	names, err := parse(fs, args)
	if err != nil {
		return err
	}
	e := env()
	list, err := pickHarnesses(e, names, false)
	if err != nil {
		return err
	}
	for _, h := range list {
		w := h.Wired(e)
		if !w.MCP && !w.Hooks && !w.Legacy && len(names) == 0 {
			continue
		}
		fmt.Printf("• %s\n", h.Name)
		tx := harness.NewTx(*dry, os.Stdout)
		h.Uninstall(e, tx)
		changes, err := tx.Commit()
		if err != nil {
			fmt.Printf("    FAILED: %v\n", err)
			continue
		}
		for _, c := range changes {
			fmt.Printf("    %s %s\n", c.Action, tilde(c.Path))
		}
		if len(changes) == 0 && !*dry {
			fmt.Println("    nothing to remove")
		}
	}
	if !*dry {
		fmt.Println("\nRemoved. Backups of every edited file sit next to it (*.bak-ainterlinked-*). The bus data stays in", tilde(config.Home()))
	}
	return nil
}

func cmdScan(ctx context.Context, args []string) error {
	e := env()
	cfg := loadConfig()
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HARNESS\tDETECTED\tMCP\tHOOKS\tBUS\tDELIVERY\tEVIDENCE")
	for _, h := range harness.All() {
		ev := h.Detect(e)
		w := h.Wired(e)
		det, evs := "no", "-"
		if len(ev) > 0 {
			det, evs = "yes", tilde(ev[0])
		}
		hk := "-"
		if h.Hooks {
			hk = yn(w.Hooks)
		}
		bus := "on"
		if !cfg.HarnessEnabled(h.ID) {
			bus = "muted"
		}
		tier := h.Tier
		if !w.MCP && !w.Hooks {
			tier, bus = "-", "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", h.ID, det, yn(w.MCP), hk, bus, tier, evs)
	}
	tw.Flush()
	fmt.Println("\nDelivery: push = reaches idle sessions · turn = injected at prompt/tool/stop · pull = agent must call read_inbox")
	return nil
}

func yn(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func cmdEnable(on bool) func(context.Context, []string) error {
	return func(ctx context.Context, args []string) error {
		if len(args) != 1 {
			return errors.New("usage: ainterlinked enable|disable <harness>")
		}
		h := harness.ByID(args[0])
		if h == nil {
			return fmt.Errorf("unknown harness %q (known: %s)", args[0], knownIDs())
		}
		cfg, err := config.LoadFile()
		if err != nil {
			return err
		}
		cfg.Harnesses[h.ID] = config.HarnessCfg{Enabled: on}
		if err := config.Save(cfg); err != nil {
			return err
		}
		if on {
			fmt.Printf("%s can talk on the bus.\n", h.Name)
		} else {
			fmt.Printf("%s is muted: its hooks and MCP server stay silent (wiring kept; `ainterlinked uninstall %s` removes it).\n", h.Name, h.ID)
		}
		return nil
	}
}

func cmdConfig(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "--path" {
		fmt.Println(config.Path())
		return nil
	}
	if _, err := os.Stat(config.Path()); err != nil {
		if err := config.Save(config.Default()); err != nil {
			return err
		}
	}
	b, err := os.ReadFile(config.Path())
	if err != nil {
		return err
	}
	fmt.Printf("# %s\n%s", config.Path(), b)
	return nil
}
