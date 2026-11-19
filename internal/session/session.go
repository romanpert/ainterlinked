// Package session derives who "this" agent is: harness, project and anchor
// process, from the working directory and the process tree.
package session

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/romanpert/interlinked/internal/config"
	"github.com/romanpert/interlinked/internal/core"
	"github.com/romanpert/interlinked/internal/proc"
)

// Context describes the current harness session.
type Context struct {
	Harness     string
	Cwd         string
	ProjectPath string
	Project     string
	Branch      string
	Machine     string
	AnchorPID   int
	AnchorName  string
}

// Detect builds the session context for harness running in cwd.
func Detect(cfg config.Config, harness, cwd string) Context {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if abs, err := filepath.Abs(cwd); err == nil {
		cwd = abs
	}
	root := GitRoot(cwd)
	if root == "" {
		root = cwd
	}
	pid, name := proc.Anchor()
	return Context{
		Harness:     harness,
		Cwd:         cwd,
		ProjectPath: NormPath(root),
		Project:     filepath.Base(root),
		Branch:      Branch(root),
		Machine:     cfg.Machine,
		AnchorPID:   pid,
		AnchorName:  name,
	}
}

// NormPath makes paths comparable across processes (case-insensitive on Windows).
func NormPath(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

// GitRoot walks up from dir to the nearest directory containing .git.
func GitRoot(dir string) string {
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// Branch reads the current branch from .git/HEAD (worktrees included).
func Branch(root string) string {
	gitPath := filepath.Join(root, ".git")
	st, err := os.Stat(gitPath)
	if err != nil {
		return ""
	}
	gitDir := gitPath
	if !st.IsDir() { // worktree / submodule: "gitdir: <path>"
		b, err := os.ReadFile(gitPath)
		if err != nil {
			return ""
		}
		gitDir = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(root, gitDir)
		}
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	h := strings.TrimSpace(string(head))
	if strings.HasPrefix(h, "ref: refs/heads/") {
		return strings.TrimPrefix(h, "ref: refs/heads/")
	}
	if len(h) >= 7 {
		return "detached@" + h[:7]
	}
	return ""
}

// Register builds the registration request for this session.
func (c Context) Register(via, delivery, sessionID string) core.RegisterReq {
	return core.RegisterReq{
		Harness:     c.Harness,
		Machine:     c.Machine,
		Project:     c.Project,
		ProjectPath: c.ProjectPath,
		Branch:      c.Branch,
		AnchorPID:   c.AnchorPID,
		SessionID:   sessionID,
		Via:         via,
		Delivery:    delivery,
		Name:        os.Getenv("INTERLINKED_NAME"),
		Role:        os.Getenv("INTERLINKED_ROLE"),
	}
}
