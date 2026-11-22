package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/session"
	"github.com/romanpert/ainterlinked/internal/transcript"
)

// cmdContext shows what fills sessions' context windows, from transcripts
// already on this disk. It runs when asked and changes nothing: no session
// is contacted and no figure is published.
//
//	ainterlinked context            this project's sessions, last 24 hours
//	ainterlinked context <session>  one session, in full
//	ainterlinked context --all --hours 72
func cmdContext(_ context.Context, args []string) error {
	fs := newFlags("context")
	all := fs.Bool("all", false, "sessions of every project")
	hours := fs.Float64("hours", 24, "sessions active in the last `N` hours")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 || *hours <= 0 {
		return errors.New("usage: ainterlinked context [<session id>] [--all] [--hours N] [--json]")
	}
	if len(pos) == 1 {
		return oneContext(pos[0], *asJSON)
	}
	var dirs []string
	where := "every project"
	if !*all {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		dirs = []string{wd, session.Canonical(wd)}
		if root := session.GitRoot(wd); root != "" {
			dirs = append(dirs, root)
		}
		where = filepath.Base(wd)
	}
	fills := readFills(transcript.ClaudeSessions(dirs, time.Now().Add(-time.Duration(*hours*float64(time.Hour)))))
	if *asJSON {
		return printJSON(fills)
	}
	if len(fills) == 0 {
		fmt.Printf("no Claude Code session with a transcript in %s in the last %gh (--all, --hours N)\n", where, *hours)
		return nil
	}
	fmt.Printf("%s · Claude Code sessions active in the last %gh: %d\n\n", where, *hours, len(fills))
	printFills(os.Stdout, fills)
	return nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// readFills reads transcripts a few at a time, in order. One that can't be
// read (a session without a request yet) is left out.
func readFills(paths []string) []transcript.Fill {
	out := make([]*transcript.Fill, len(paths))
	sem := make(chan struct{}, max(1, runtime.NumCPU()/2))
	var wg sync.WaitGroup
	for i, p := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if f, err := transcript.ReadFill(p); err == nil {
				out[i] = &f
			}
		}()
	}
	wg.Wait()
	fills := make([]transcript.Fill, 0, len(out))
	for _, f := range out {
		if f != nil {
			fills = append(fills, *f)
		}
	}
	return fills
}

// oneContext shows the session whose id is, or starts with, who.
func oneContext(who string, asJSON bool) error {
	var hits []string
	for _, p := range transcript.ClaudeSessions(nil, time.Time{}) {
		id := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		if id == who {
			hits = []string{p}
			break
		}
		if len(who) >= 6 && strings.HasPrefix(id, who) {
			hits = append(hits, p)
		}
	}
	if len(hits) == 0 {
		return fmt.Errorf("no session %q with a Claude Code transcript on this machine (ainterlinked context --all lists them)", who)
	}
	if len(hits) > 1 {
		return fmt.Errorf("%d sessions start with %q: give more of the id", len(hits), who)
	}
	f, err := transcript.ReadFill(hits[0])
	if err != nil {
		return fmt.Errorf("%s: %w", who, err)
	}
	if asJSON {
		return printJSON(f)
	}
	printFill(os.Stdout, f)
	return nil
}

// printFills: a line per session, then where the tokens went in all of them.
func printFills(w io.Writer, fills []transcript.Fill) {
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "session\tstart\tnow\tpeak\tcompacted\ttitle\tmost of its growth")
	for _, f := range fills {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", short(f.Session), core.FormatTokens(f.Start), core.FormatTokens(f.Now),
			core.FormatTokens(f.Peak), compactions(f.Compactions), title(f.Title, 32), topPieces(f.Grown, 2))
	}
	tw.Flush()
	starts := make([]int64, 0, len(fills))
	begin, grown := map[string]int64{}, map[string]int64{}
	for _, f := range fills {
		starts = append(starts, f.Start)
		for _, p := range f.Begin {
			begin[p.Name] += p.Tokens
		}
		for _, p := range f.Grown {
			grown[p.Name] += p.Tokens
		}
	}
	fmt.Fprintf(w, "\nBefore doing anything, a session holds %s tokens (median):\n", core.FormatTokens(median(starts)))
	printPieces(w, summed(begin, int64(len(fills))), 10)
	if len(grown) > 0 {
		fmt.Fprintf(w, "\nWhat they added since, all together:\n")
		printPieces(w, summed(grown, 1), 10)
	}
	fmt.Fprintf(w, "\nOne in full: ainterlinked context <session>\n")
}

func printFill(w io.Writer, f transcript.Fill) {
	fmt.Fprintf(w, "session %s · %s\n%s\nnow %s tokens · peak %s", f.Session, f.Model, title(f.Title, 100), core.FormatTokens(f.Now), core.FormatTokens(f.Peak))
	for _, c := range f.Compactions {
		fmt.Fprintf(w, "\ncompacted%s at %s", map[bool]string{true: " by itself"}[c.Auto], core.FormatTokens(c.From))
		if c.To > 0 {
			fmt.Fprintf(w, ", went on from %s", core.FormatTokens(c.To))
		}
	}
	fmt.Fprintf(w, "\n\nIts first request: %s tokens\n", core.FormatTokens(f.Start))
	printPieces(w, f.Begin, 12)
	if len(f.Tools) > 0 {
		fmt.Fprintln(w, "\nLargest tool definitions:")
		printPieces(w, f.Tools, 8)
	}
	if len(f.Files) > 0 {
		fmt.Fprintln(w, "\nLargest instruction files:")
		printPieces(w, f.Files, 8)
	}
	if len(f.Grown) > 0 {
		fmt.Fprintln(w, "\nWhat it added since:")
		printPieces(w, f.Grown, 12)
	}
	fmt.Fprintln(w, "\nTotals are the API's counts; pieces are shares by length of text.")
}

func printPieces(w io.Writer, ps []transcript.Piece, most int) {
	total := int64(0)
	for _, p := range ps {
		total += p.Tokens
	}
	if total == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', tabwriter.AlignRight)
	rest := int64(0)
	for i, p := range ps {
		if i >= most || p.Tokens == 0 {
			rest += p.Tokens
			continue
		}
		fmt.Fprintf(tw, "%s\t%d%%\t  %s\n", core.FormatTokens(p.Tokens), p.Tokens*100/total, plain(p.Name))
	}
	if rest > 0 {
		fmt.Fprintf(tw, "%s\t%d%%\t  %s\n", core.FormatTokens(rest), rest*100/total, "the rest")
	}
	tw.Flush()
}

// summed lists a map's pieces, each divided by n, largest first.
func summed(m map[string]int64, n int64) []transcript.Piece {
	out := make([]transcript.Piece, 0, len(m))
	for k, v := range m {
		out = append(out, transcript.Piece{Name: k, Tokens: v / n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tokens != out[j].Tokens {
			return out[i].Tokens > out[j].Tokens
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func topPieces(ps []transcript.Piece, n int) string {
	total := int64(0)
	for _, p := range ps {
		total += p.Tokens
	}
	if total == 0 {
		return "-"
	}
	var out []string
	for _, p := range ps[:min(n, len(ps))] {
		out = append(out, fmt.Sprintf("%s %d%%", plain(p.Name), p.Tokens*100/total))
	}
	return strings.Join(out, ", ")
}

func compactions(cs []transcript.Compaction) string {
	if len(cs) == 0 {
		return "-"
	}
	last := cs[len(cs)-1]
	return fmt.Sprintf("%d× (at %s)", len(cs), core.FormatTokens(last.From))
}

func median(ns []int64) int64 {
	s := append([]int64(nil), ns...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

// title is a session's, on one line of at most n characters.
func title(s string, n int) string {
	s = strings.Join(strings.Fields(plain(s)), " ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n-1]) + "…"
	}
	if s == "" {
		return "-"
	}
	return s
}

// plain is s without what a terminal would act on: titles, tools' names and
// paths come from transcripts, not from us.
func plain(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
