package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Some files are ledgers that many agents keep at once: an open-items list
// each session prunes, a decision log each one appends to. Held whole they
// become a queue (2026-11-22, one repo: 230 of 255 queue waits in a day were
// for four such files, each held for minutes over a one-line edit).
//
// The owner can make them shared: nobody holds them, so editing one reserves
// nothing and no reservation blocks it. Taking files out of reservations is
// the owner's call, so only the human sets the list (`ainterlinked shared
// accept` reads it from the repo's covenant, shows it and records it); the
// hub never reads it from the repo, which agents can write. It names files,
// not globs: a glob could free the whole repo.

// SharedReq sets a project's shared files; By is the human. No files: none.
type SharedReq struct {
	By          string   `json:"by"`
	ProjectPath string   `json:"project_path"`
	Files       []string `json:"files,omitempty"`
}

// SharedList is a project's shared files, project-relative.
type SharedList struct {
	ProjectPath string   `json:"project_path"`
	Files       []string `json:"files"`
}

func sharedKey(projectPath string) string { return "shared:" + projectPath }

// SetShared records the files of a project nobody holds. Only the human sets it.
func (s *Store) SetShared(ctx context.Context, r SharedReq) (SharedList, error) {
	if me, err := s.Get(ctx, r.By); err != nil || me.Harness != "human" {
		return SharedList{}, errors.New("only the owner shares files (the ainterlinked CLI, outside any agent's session)")
	}
	if strings.TrimSpace(r.ProjectPath) == "" {
		return SharedList{}, errors.New("project_path is required")
	}
	set := map[string]bool{}
	for _, f := range r.Files {
		if strings.ContainsAny(f, "*?[") || strings.HasSuffix(f, "/") || strings.HasSuffix(f, `\`) {
			return SharedList{}, fmt.Errorf("%s: shared files are named one by one, not by glob or folder", f)
		}
		p, err := NormPattern(r.ProjectPath, f)
		if err != nil {
			return SharedList{}, err
		}
		if p == "**" {
			return SharedList{}, fmt.Errorf("%s: that is the whole project", f)
		}
		set[p] = true
	}
	list := SharedList{ProjectPath: r.ProjectPath, Files: []string{}}
	for p := range set {
		list.Files = append(list.Files, p)
	}
	sort.Strings(list.Files)
	if len(list.Files) == 0 {
		_, err := s.db.ExecContext(ctx, `DELETE FROM meta WHERE key=?`, sharedKey(r.ProjectPath))
		return list, err
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)`,
		sharedKey(r.ProjectPath), strings.Join(list.Files, "\n"))
	return list, err
}

// GetShared returns a project's shared files.
func (s *Store) GetShared(ctx context.Context, projectPath string) (SharedList, error) {
	list := SharedList{ProjectPath: projectPath, Files: []string{}}
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, sharedKey(projectPath)).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return list, nil
	}
	if err != nil {
		return list, err
	}
	list.Files = strings.Split(v, "\n")
	return list, nil
}

// isShared reports whether the owner made the project-relative file rel
// shared. Options.Shared, when set, answers instead.
func (s *Store) isShared(projectPath, rel string) bool {
	if rel == "" || strings.ContainsAny(rel, "*?[") {
		return false
	}
	if s.shared != nil {
		return s.shared(projectPath, rel)
	}
	list, err := s.GetShared(context.Background(), projectPath)
	if err != nil {
		return false // unreadable: reservations work as usual
	}
	for _, f := range list.Files {
		if f == rel {
			return true
		}
	}
	return false
}

// sharedRefusal answers a reservation asked for a shared file.
func sharedRefusal(projectPath, rel string) Lock {
	return Lock{ProjectPath: projectPath, Pattern: rel, Agent: SystemSender,
		Reason: "shared by the owner: nobody holds it. Edit only your own lines (the covenant's rules for it say how); no reservation is needed or possible"}
}
