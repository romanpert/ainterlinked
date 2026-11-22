package core

import "strings"

// isShared reports whether the owner declared the project-relative file rel
// shared (Options.Shared): nobody holds it. Globs are never "a file".
func (s *Store) isShared(projectPath, rel string) bool {
	return s.shared != nil && rel != "" && !strings.ContainsAny(rel, "*?[") && s.shared(projectPath, rel)
}

// sharedRefusal answers a reservation asked for a shared file.
func sharedRefusal(projectPath, rel string) Lock {
	return Lock{ProjectPath: projectPath, Pattern: rel, Agent: SystemSender,
		Reason: "shared by the project's covenant: nobody holds it. Edit only your own lines (the covenant's rules for it say how); no reservation is needed or possible"}
}
