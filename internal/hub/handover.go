package hub

import (
	"path/filepath"

	"github.com/romanpert/ainterlinked/internal/session"
	"github.com/romanpert/ainterlinked/internal/transcript"
)

// handoverIn reads a session's handover only if its transcript ran in the
// agent's own checkout: the folder Claude Code recorded resolves, the same way
// registration resolves it (session.Detect), to exactly the project path the
// agent registered with. A session id or project path claimed at
// registration can't bring another checkout's transcript in, nor a parent
// folder's (a project of "C:\" doesn't own every repo below it).
func handoverIn(harness, sess, project string) (transcript.Handover, bool) {
	if project == "" {
		return transcript.Handover{}, false
	}
	h, ok := transcript.ReadHandover(harness, sess)
	if !ok || !filepath.IsAbs(h.Cwd) {
		return transcript.Handover{}, false
	}
	if checkout(h.Cwd) != project {
		return transcript.Handover{}, false
	}
	return h, true
}

// checkout is the project path of a working directory, as registration
// computes it: its git root, or the folder itself outside a repo.
func checkout(cwd string) string {
	cwd = session.Canonical(cwd)
	root := session.GitRoot(cwd)
	if root == "" {
		root = cwd
	}
	return session.NormPath(root)
}
