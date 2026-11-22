package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/covenant"
	"github.com/romanpert/ainterlinked/internal/session"
)

// cmdShared shows or sets the files of the repo here that nobody holds:
// ledgers many agents keep at once. The repo's covenant declares them
// ("## shared: NEXT.md, docs/log.md"); the owner accepts the declaration,
// and only then does the hub stop reserving them.
//
//	ainterlinked shared          what the covenant declares and what is in force
//	ainterlinked shared accept   show the covenant's declaration and, confirmed, put it in force
//	ainterlinked shared accept NEXT.md docs/log.md   share exactly these (no prompt)
//	ainterlinked shared off      nothing shared here
func cmdShared(ctx context.Context, args []string) error {
	c, cfg, err := openHub()
	if err != nil {
		return err
	}
	defer c.Close()
	here := session.Detect(cfg, "human", "")
	root := session.GitRoot(here.Cwd)
	if root == "" {
		return errors.New("not inside a git repository: shared files belong to a repo")
	}
	cov, err := covenant.Load(root)
	if err != nil {
		return err
	}
	declared := cov.SharedFiles()
	var now core.SharedList
	if err := c.Call(ctx, "get_shared", core.SharedReq{ProjectPath: here.ProjectPath}, &now); err != nil {
		return err
	}
	verb := ""
	if len(args) > 0 {
		verb = args[0]
	}
	switch verb {
	case "":
		fmt.Printf("in force for %s: %s\n", root, listOrNone(now.Files))
		fmt.Printf("declared in %s: %s\n", covenant.File, listOrNone(declared))
		for _, p := range cov.Problems() {
			if strings.Contains(p, "shared") {
				fmt.Println("  " + printable(p))
			}
		}
		if !sameFiles(now.Files, declared) {
			fmt.Println("they differ: `ainterlinked shared accept` puts the declaration in force")
		}
		return nil
	case "accept", "off":
	default:
		return fmt.Errorf("unknown %q: use `ainterlinked shared`, `shared accept` or `shared off`", verb)
	}
	me, err := human(ctx, c, cfg)
	if err != nil {
		return err
	}
	var files []string
	switch {
	case verb == "off":
	case len(args) > 1:
		files = args[1:] // named by the owner: exactly these
	case len(declared) == 0:
		return fmt.Errorf("%s declares no shared files (a section \"## shared: NEXT.md, docs/log.md\")", covenant.File)
	default:
		// What gets accepted is what the owner sees now and confirms: the
		// covenant may have changed since they last looked at it.
		fmt.Printf("in force now: %s\nto share, as %s declares now: %s\n", listOrNone(now.Files), covenant.File, listOrNone(declared))
		if !terminal() {
			return errors.New("not a terminal, so nothing to confirm with: name the files instead, `ainterlinked shared accept NEXT.md docs/log.md`")
		}
		fmt.Print("share exactly these files, so nobody holds them? [y/N] ")
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" && a != "s" && a != "si" && a != "sí" {
			return errors.New("not accepted: nothing changed")
		}
		files = declared
	}
	var got core.SharedList
	if err := c.Call(ctx, "shared", core.SharedReq{By: me, ProjectPath: here.ProjectPath, Files: files}, &got); err != nil {
		return err
	}
	if len(got.Files) == 0 {
		fmt.Println("nothing shared here: editing any file reserves it again")
		return nil
	}
	fmt.Printf("shared, nobody holds them: %s\nediting them reserves nothing and no reservation blocks them; agents follow the covenant's rules for them\n",
		listOrNone(got.Files))
	return nil
}

func listOrNone(fs []string) string {
	if len(fs) == 0 {
		return "none"
	}
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = printable(f)
	}
	return strings.Join(out, ", ")
}

// printable shows text from the repo (which agents can write) as itself,
// or quoted when it holds anything a terminal would act on or hide (escape
// sequences, carriage returns, bidi controls): what the owner reads before
// accepting must be what gets accepted.
func printable(s string) string {
	if strings.IndexFunc(s, func(r rune) bool { return !strconv.IsPrint(r) }) >= 0 {
		return strconv.QuoteToASCII(s)
	}
	return s
}

// terminal reports whether stdin is an interactive console.
func terminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// sameFiles compares two lists of project-relative files, ignoring order
// and (as the hub does on Windows) case.
func sameFiles(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, f := range a {
		seen[strings.ToLower(f)]++
	}
	for _, f := range b {
		k := strings.ToLower(strings.TrimPrefix(f, "./"))
		if seen[k] == 0 {
			return false
		}
		seen[k]--
	}
	return true
}
