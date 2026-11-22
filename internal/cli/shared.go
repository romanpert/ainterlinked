package cli

import (
	"context"
	"errors"
	"fmt"
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
//	ainterlinked shared accept   put the covenant's declaration in force
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
				fmt.Println("  " + p)
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
	files := declared
	if verb == "off" {
		files = nil
	} else if len(files) == 0 {
		return fmt.Errorf("%s declares no shared files (a section \"## shared: NEXT.md, docs/log.md\")", covenant.File)
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
		strings.Join(got.Files, ", "))
	return nil
}

func listOrNone(fs []string) string {
	if len(fs) == 0 {
		return "none"
	}
	return strings.Join(fs, ", ")
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
