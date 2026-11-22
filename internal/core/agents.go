package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Agent is one live harness session on the bus.
type Agent struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Harness     string `json:"harness"`
	Model       string `json:"model,omitempty"`
	Role        string `json:"role,omitempty"`
	Machine     string `json:"machine"`
	Project     string `json:"project"`
	ProjectPath string `json:"project_path"`
	Branch      string `json:"branch,omitempty"`
	// AnchorPID and SessionID pick the agent a registration resumes, which
	// hands out its key (keys.go): they stay inside the hub and are never
	// serialized, so listing agents doesn't tell anyone how to become one.
	AnchorPID int    `json:"-"`
	SessionID string `json:"-"`
	State     string `json:"state"`
	Activity  string `json:"activity,omitempty"`
	Via       string `json:"via"`
	Delivery  string `json:"delivery"`
	Online    bool   `json:"online"`
	StartedAt int64  `json:"started_at"`
	LastSeen  int64  `json:"last_seen"`
	Unread    int    `json:"unread,omitempty"`
	// Aka are earlier names of this agent (renames, adopted identities);
	// messages to them still reach it.
	Aka []string `json:"aka,omitempty"`
	// StateSince is when it entered its current state (ms).
	StateSince int64 `json:"state_since,omitempty"`
	// Key is set only in Register's answer: the session's proof that it is
	// this agent, carried by every call that acts as it (keys.go). Never shown.
	Key string `json:"key,omitempty"`
}

// ErrSuperseded means the agent went offline and its session has since
// registered again under a new agent; the caller should register again.
var ErrSuperseded = errors.New("agent superseded by a newer registration of the same session; register again")

// States an agent can report.
var States = []string{"idle", "working", "waiting", "blocked", "done"}

// RegisterReq announces a harness session.
type RegisterReq struct {
	Harness     string `json:"harness"`
	Name        string `json:"name,omitempty"`
	Role        string `json:"role,omitempty"`
	Model       string `json:"model,omitempty"`
	Machine     string `json:"machine"`
	Project     string `json:"project"`
	ProjectPath string `json:"project_path"`
	Branch      string `json:"branch,omitempty"`
	AnchorPID   int    `json:"anchor_pid,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	Via         string `json:"via"`      // "mcp" or "hook"
	Delivery    string `json:"delivery"` // push | turn | pull
}

const agentCols = `id,name,harness,model,role,machine,project,project_path,branch,anchor_pid,session_id,state,activity,via,delivery,online,started_at,last_seen,aka,state_since`

type scanner interface{ Scan(...any) error }

func scanAgent(r scanner) (Agent, error) {
	var a Agent
	var online int
	var aka string
	err := r.Scan(&a.ID, &a.Name, &a.Harness, &a.Model, &a.Role, &a.Machine, &a.Project, &a.ProjectPath,
		&a.Branch, &a.AnchorPID, &a.SessionID, &a.State, &a.Activity, &a.Via, &a.Delivery, &online, &a.StartedAt, &a.LastSeen, &aka, &a.StateSince)
	a.Online = online == 1
	a.Aka = splitList(aka)
	return a, err
}

// anchorGrace caps how long a live harness process alone keeps a silent
// agent online: after a crash its pid can be reused by an unrelated process.
const anchorGrace = 12 * time.Hour

func (s *Store) reap(tx *sql.Tx) error {
	cut := s.ms() - s.stale.Milliseconds()
	rows, err := tx.Query(`SELECT `+agentCols+` FROM agents WHERE online=1 AND last_seen < ?`, cut)
	if err != nil {
		return err
	}
	var stale []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			rows.Close()
			return err
		}
		stale = append(stale, a)
	}
	rows.Close()
	gone := s.withoutLiveAnchors(stale)
	for i := range gone {
		if _, err := tx.Exec(`UPDATE agents SET online=0, state='offline' WHERE id=?`, gone[i].ID); err != nil {
			return err
		}
		why := " went offline (no heartbeat)"
		if gone[i].State == stateLeaving {
			why = " left"
		}
		event(tx, s.ms(), &gone[i], "left", gone[i].Name+why)
	}
	return nil
}

// withoutLiveAnchors drops agents whose harness process still runs on this
// machine. A session wired only through hooks sends nothing between tool
// calls; a long command or a long think must not take it offline (which
// would also free its name for another session and void its reservations).
func (s *Store) withoutLiveAnchors(stale []Agent) []Agent {
	if s.alive == nil || s.machine == "" || len(stale) == 0 {
		return stale
	}
	now := s.ms()
	anchored := func(a Agent) bool {
		return a.State != stateLeaving && a.Machine == s.machine && a.AnchorPID > 0 && now-a.LastSeen < anchorGrace.Milliseconds()
	}
	var pids []int
	for _, a := range stale {
		if anchored(a) {
			pids = append(pids, a.AnchorPID)
		}
	}
	if len(pids) == 0 {
		return stale
	}
	alive, err := s.alive(pids)
	if err != nil {
		return stale // can't tell: heartbeats decide
	}
	out := make([]Agent, 0, len(stale))
	for _, a := range stale {
		if !(anchored(a) && alive[a.AnchorPID]) {
			out = append(out, a)
		}
	}
	return out
}

// resumable finds the agent a session id resumes: the latest one with it
// whose harness is gone (a restart, a resumed session) or is the caller's.
// Never one still running as another process: a session id can be read, so
// it alone isn't who you are.
func (s *Store) resumable(tx *sql.Tx, r RegisterReq) (Agent, error) {
	rows, err := tx.Query(`SELECT `+agentCols+` FROM agents WHERE session_id=? AND (harness=? OR (?='copilot' AND harness IN ('copilot','copilot-cli','vscode')))
		ORDER BY last_seen DESC LIMIT 8`, r.SessionID, r.Harness, family(r.Harness))
	if err != nil {
		return Agent{}, err
	}
	var found []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			rows.Close()
			return Agent{}, err
		}
		found = append(found, a)
	}
	rows.Close()
	for _, a := range found {
		if a.AnchorPID <= 0 || a.AnchorPID == r.AnchorPID || !s.anchorLive(a) {
			return a, nil
		}
	}
	return Agent{}, sql.ErrNoRows
}

// anchorLive reports whether a's harness process still runs (or, where
// that can't be probed, whether a is online). Unknown counts as running.
func (s *Store) anchorLive(a Agent) bool {
	if s.alive == nil || a.Machine != s.machine {
		return a.Online
	}
	alive, err := s.alive([]int{a.AnchorPID})
	return err != nil || alive[a.AnchorPID]
}

func ukey(r RegisterReq) string {
	switch {
	case r.AnchorPID > 0:
		// Keyed by harness family, not name: VS Code's Copilot registers its
		// MCP server as "vscode" and its hooks as "copilot", and both must be
		// one agent; but Cline and Copilot inside the same VS Code process
		// (same anchor) must stay two.
		return fmt.Sprintf("%s|%s|pid:%d", family(r.Harness), r.Machine, r.AnchorPID)
	case r.SessionID != "":
		return fmt.Sprintf("%s|%s|sid:%s", r.Harness, r.Machine, r.SessionID)
	default:
		return fmt.Sprintf("%s|%s|rnd:%s", r.Harness, r.Machine, newID())
	}
}

func family(harness string) string {
	switch harness {
	case "copilot", "copilot-cli", "vscode":
		return "copilot"
	}
	return harness
}

func mergeVia(a, b string) string {
	set := map[string]bool{}
	for _, v := range strings.Split(a+","+b, ",") {
		if v = strings.TrimSpace(v); v != "" {
			set[v] = true
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// bestDelivery keeps the strongest delivery tier seen for an agent.
func bestDelivery(a, b string) string {
	rank := map[string]int{"": 0, "pull": 1, "turn": 2, "push": 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func (s *Store) nameFree(tx *sql.Tx, name, exceptID string) bool {
	var n int
	_ = tx.QueryRow(`SELECT count(*) FROM agents WHERE online=1 AND lower(name)=lower(?) AND id<>?`, name, exceptID).Scan(&n)
	return n == 0
}

// reservedNames can't be taken by agents: messages "from human" must really
// come from the operator.
var reservedNames = map[string]bool{"human": true, "user": true, "system": true, "operator": true, "admin": true, "owner": true, SystemSender: true, LegacySystemSender: true}

// nameReuseWindow keeps an automatic name from going to a new session while
// the session that had it may still be addressed by it: peers that saw
// "claude@api-3" an hour ago must not reach a stranger by that name.
const nameReuseWindow = 24 * time.Hour

// nameRecent reports whether another agent used name (as its name or an
// earlier one) recently, or still has unread mail under it.
func (s *Store) nameRecent(tx *sql.Tx, name, exceptID string) bool {
	var n int
	err := tx.QueryRow(`SELECT count(*) FROM agents WHERE id<>? AND (lower(name)=lower(?) OR instr(','||lower(aka)||',', ','||lower(?)||',')>0)
		AND (last_seen>? OR id IN (SELECT agent_id FROM receipts WHERE read_at=0))`,
		exceptID, name, name, s.ms()-nameReuseWindow.Milliseconds()).Scan(&n)
	return err != nil || n > 0 // can't tell: treat as taken
}

// pickName chooses a free name for agent exceptID.
func (s *Store) pickName(tx *sql.Tx, want, harness, project, exceptID string) string {
	base := sanitizeName(want)
	if reservedNames[base] && harness != "human" {
		base = ""
	}
	auto := base == ""
	if auto {
		base = sanitizeName(shortHarness(harness) + "@" + project)
	}
	if base == "" {
		base = "agent"
	}
	// A chosen name (a task alias) may follow on from an offline agent that
	// had it: that's a hand-over. An automatic one must be new.
	free := func(n string) bool {
		if !s.nameFree(tx, n, exceptID) {
			return false
		}
		return !auto || !s.nameRecent(tx, n, exceptID)
	}
	if free(base) {
		return base
	}
	for i := 2; i < 1000; i++ {
		n := fmt.Sprintf("%s-%d", base, i)
		if free(n) {
			return n
		}
	}
	return clip(base, 40) + "-" + newID() // unique without asking the database again
}

// IsAutoName reports whether name is one the hub generated (harness@project
// or harness@project-N) rather than one the agent chose.
func IsAutoName(name, harness, project string) bool {
	base := sanitizeName(shortHarness(harness) + "@" + project)
	if base == "" || !strings.HasPrefix(name, base) {
		return false
	}
	rest := strings.TrimPrefix(name, base)
	if rest == "" {
		return true
	}
	if !strings.HasPrefix(rest, "-") || len(rest) < 2 {
		return false
	}
	for _, r := range rest[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func sanitizeName(n string) string {
	n = strings.TrimSpace(strings.ToLower(n))
	var b strings.Builder
	for _, r := range n {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '@', r == '.':
			b.WriteRune(r)
		case r == ' ' || r == '/':
			b.WriteRune('-')
		}
	}
	return clip(b.String(), 48)
}

func shortHarness(h string) string {
	switch h {
	case "claude-code":
		return "claude"
	case "copilot-cli":
		return "copilot"
	case "vscode":
		return "copilot-vscode"
	}
	return h
}

// Register creates or refreshes the agent for a harness session. The same
// session seen through its MCP server and through its hooks resolves to one
// agent (same anchor PID), and a resumed session (same session id) revives
// its previous identity and unread mail.
func (s *Store) Register(ctx context.Context, r RegisterReq) (Agent, error) {
	if r.Harness == "" {
		return Agent{}, errors.New("harness is required")
	}
	r = sanitizeReg(r)
	if len(r.ProjectPath) > 4096 {
		// Rejected, not clipped: a clipped path would no longer match the real one.
		return Agent{}, errors.New("project path too long")
	}
	secret, err := s.hubSecret()
	if err != nil {
		return Agent{}, err
	}
	var out Agent
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if err := s.reap(tx); err != nil {
			return err
		}
		now := s.ms()
		key := ukey(r)
		row := tx.QueryRow(`SELECT `+agentCols+` FROM agents WHERE online=1 AND ukey=?`, key)
		a, err := scanAgent(row)
		if errors.Is(err, sql.ErrNoRows) && r.SessionID != "" {
			a, err = s.resumable(tx, r)
		}
		switch {
		case errors.Is(err, sql.ErrNoRows):
			a = Agent{ID: newID(), Harness: r.Harness, State: "idle", StartedAt: now}
			a.Name = s.pickName(tx, r.Name, r.Harness, r.Project, a.ID)
			_, err = tx.Exec(`INSERT INTO agents(id,name,harness,model,role,machine,project,project_path,branch,anchor_pid,session_id,ukey,online,state,via,delivery,started_at,last_seen,state_since)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,1,'idle',?,?,?,?,?)`,
				a.ID, a.Name, r.Harness, r.Model, r.Role, r.Machine, r.Project, r.ProjectPath, r.Branch, r.AnchorPID, r.SessionID, key, r.Via, r.Delivery, now, now, now)
			if err != nil {
				return err
			}
			event(tx, now, &Agent{ID: a.ID, Name: a.Name, Project: r.Project}, "joined",
				fmt.Sprintf("%s joined (%s, %s)", a.Name, r.Harness, r.Project))
		case err != nil:
			return err
		default:
			revived := !a.Online
			name := a.Name
			if r.Name != "" && sanitizeName(r.Name) != a.Name {
				name = s.pickName(tx, r.Name, r.Harness, r.Project, a.ID)
			} else if revived && !s.nameFree(tx, name, a.ID) {
				name = s.pickName(tx, "", r.Harness, r.Project, a.ID)
			}
			state := a.State
			if revived || state == stateLeaving {
				state = "idle"
			}
			if name != a.Name {
				if err := setAka(tx, a, name); err != nil {
					return err
				}
			}
			_, err = tx.Exec(`UPDATE agents SET name=?, model=COALESCE(NULLIF(?,''),model), role=COALESCE(NULLIF(?,''),role),
				machine=?, project=COALESCE(NULLIF(?,''),project), project_path=COALESCE(NULLIF(?,''),project_path),
				branch=COALESCE(NULLIF(?,''),branch), anchor_pid=CASE WHEN ?>0 THEN ? ELSE anchor_pid END,
				session_id=COALESCE(NULLIF(?,''),session_id), ukey=?, online=1, state_since=CASE WHEN state<>? THEN ? ELSE state_since END, state=?,
				via=?, delivery=?, last_seen=? WHERE id=?`,
				name, r.Model, r.Role, r.Machine, r.Project, r.ProjectPath, r.Branch, r.AnchorPID, r.AnchorPID,
				r.SessionID, key, state, now, state, mergeVia(a.Via, r.Via), bestDelivery(a.Delivery, r.Delivery), now, a.ID)
			if err != nil {
				return err
			}
			if revived {
				back := a
				back.Name = name
				event(tx, now, &back, "joined", name+" is back online")
			}
		}
		ak, err := issueKey(tx, secret, a.ID)
		if err != nil {
			return err
		}
		out, err = scanAgent(tx.QueryRow(`SELECT `+agentCols+` FROM agents WHERE id=?`, a.ID))
		out.Key = ak
		return err
	})
	return out, err
}

// ClaimsHuman reports whether a registration claims to be the operator.
// Only the CLI and the dashboard register the human (hub.Local.Human); the
// hub's "register" method refuses this claim from anyone else.
func ClaimsHuman(r RegisterReq) bool { return strings.EqualFold(sanitizeReg(r).Harness, "human") }

// StatusReq updates presence. Empty fields are left unchanged.
type StatusReq struct {
	ID       string `json:"id"`
	State    string `json:"state,omitempty"`
	Activity string `json:"activity,omitempty"`
	Role     string `json:"role,omitempty"`
	Model    string `json:"model,omitempty"`
	Name     string `json:"name,omitempty"`
	Quiet    bool   `json:"quiet,omitempty"` // don't log to the feed
	// ContinueAs takes over an offline agent's identity first (see ContinueAs).
	ContinueAs string `json:"continue_as,omitempty"`
}

// SetStatus records a heartbeat and optional state/activity changes.
func (s *Store) SetStatus(ctx context.Context, r StatusReq) (Agent, error) {
	r.Role, r.Model, r.Activity = oneLine(r.Role, 80), oneLine(r.Model, 80), oneLine(r.Activity, 200)
	if r.State != "" && !validState(r.State) {
		return Agent{}, fmt.Errorf("state must be one of %s", strings.Join(States, ", "))
	}
	if r.ContinueAs != "" {
		if _, err := s.ContinueAs(ctx, r.ID, r.ContinueAs); err != nil {
			return Agent{}, err
		}
	}
	var out Agent
	err := s.tx(ctx, func(tx *sql.Tx) error {
		a, err := scanAgent(tx.QueryRow(`SELECT `+agentCols+` FROM agents WHERE id=?`, r.ID))
		if err != nil {
			return fmt.Errorf("unknown agent %q", r.ID)
		}
		now := s.ms()
		name := a.Name
		if r.Name != "" && sanitizeName(r.Name) != a.Name {
			name = s.pickName(tx, r.Name, a.Harness, a.Project, a.ID)
		}
		if !a.Online {
			// While this agent was offline its session may have re-registered
			// as a new agent with the same key; never create a second live one.
			var dup int
			_ = tx.QueryRow(`SELECT count(*) FROM agents WHERE online=1 AND id<>? AND ukey=(SELECT ukey FROM agents WHERE id=?)`, a.ID, a.ID).Scan(&dup)
			if dup > 0 {
				return ErrSuperseded
			}
		}
		if !a.Online && !s.nameFree(tx, name, a.ID) {
			name = s.pickName(tx, "", a.Harness, a.Project, a.ID)
		}
		state := a.State
		if r.State != "" {
			state = r.State
		} else if !a.Online || a.State == stateLeaving {
			state = "idle" // a heartbeat after a departure: it stayed
		}
		if name != a.Name {
			if err := setAka(tx, a, name); err != nil {
				return err
			}
		}
		_, err = tx.Exec(`UPDATE agents SET name=?, state_since=CASE WHEN state<>? THEN ? ELSE state_since END, state=?, activity=CASE WHEN ?<>'' THEN ? ELSE activity END,
			role=COALESCE(NULLIF(?,''),role), model=COALESCE(NULLIF(?,''),model), online=1, last_seen=? WHERE id=?`,
			name, state, now, state, r.Activity, oneLine(r.Activity, 200), r.Role, r.Model, now, a.ID)
		if err != nil {
			return err
		}
		if !r.Quiet && (r.State != "" && r.State != a.State || r.Activity != "" && r.Activity != a.Activity) {
			msg := name + " → " + state
			if r.Activity != "" {
				msg += ": " + r.Activity
			}
			event(tx, now, &a, "status", msg)
		}
		out, err = scanAgent(tx.QueryRow(`SELECT `+agentCols+` FROM agents WHERE id=?`, a.ID))
		return err
	})
	return out, err
}

func validState(st string) bool {
	for _, v := range States {
		if v == st {
			return true
		}
	}
	return false
}

// stateLeaving marks an agent whose session said it ended but may come
// straight back (/clear, compaction, a resumed chat, an MCP restart).
const stateLeaving = "leaving"

// departGrace is how long a departing agent has to check in again before
// it goes offline. Seen in real use: sessions "left" and were back 2–8 s
// later, and each flap voided their queue places and reservations meanwhile.
const departGrace = 30 * time.Second

// Depart is a session's own goodbye: the agent stays online (keeping its
// reservations, queue places and name) for departGrace, and goes offline
// then unless it has checked in again. Its process may still be running,
// so the anchor check doesn't hold it.
func (s *Store) Depart(ctx context.Context, id string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		// Backdate last_seen so the ordinary reaper takes it after the grace.
		seen := s.ms() - s.stale.Milliseconds() + departGrace.Milliseconds()
		_, err := tx.Exec(`UPDATE agents SET state=?, state_since=?, last_seen=min(last_seen, ?) WHERE id=? AND online=1`, stateLeaving, s.ms(), seen, id)
		return err
	})
}

// Leave marks an agent offline at once.
func (s *Store) Leave(ctx context.Context, id string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		a, err := scanAgent(tx.QueryRow(`SELECT `+agentCols+` FROM agents WHERE id=?`, id))
		if err != nil || !a.Online {
			return nil
		}
		if _, err := tx.Exec(`UPDATE agents SET online=0, state='offline' WHERE id=?`, id); err != nil {
			return err
		}
		// Reservations are kept: they stop applying while the agent is offline
		// and come back if it returns (e.g. an MCP server restart) until they expire.
		event(tx, s.ms(), &a, "left", a.Name+" left")
		return nil
	})
}

// AgentFilter narrows List.
type AgentFilter struct {
	Project        string `json:"project,omitempty"`
	Harness        string `json:"harness,omitempty"`
	IncludeOffline bool   `json:"include_offline,omitempty"`
}

// List returns agents, online first, with their unread counts.
func (s *Store) List(ctx context.Context, f AgentFilter) ([]Agent, error) {
	if err := s.tx(ctx, s.settle); err != nil {
		return nil, err
	}
	q := `SELECT ` + agentCols + ` FROM agents WHERE 1=1`
	var args []any
	if !f.IncludeOffline {
		q += ` AND online=1`
	} else {
		q += ` AND last_seen > ?`
		args = append(args, s.ms()-7*24*3600*1000)
	}
	if f.Project != "" {
		q += ` AND (lower(project)=lower(?) OR project_path=?)`
		args = append(args, f.Project, f.Project)
	}
	if f.Harness != "" {
		q += ` AND harness=?`
		args = append(args, f.Harness)
	}
	q += ` ORDER BY online DESC, project, name`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	stale := s.ms() - BriefTTL.Milliseconds()
	for i := range out {
		_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM receipts r JOIN messages m ON m.id=r.message_id
			WHERE r.agent_id=? AND r.read_at=0`+unreadFilter, out[i].ID, stale).Scan(&out[i].Unread)
	}
	return out, nil
}

// Get returns one agent by id.
func (s *Store) Get(ctx context.Context, id string) (Agent, error) {
	a, err := scanAgent(s.db.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, fmt.Errorf("unknown agent %q", id)
	}
	return a, err
}

// Resolve finds an agent by id or name (online agents first, then the most
// recently seen offline one, so mail to a sleeping agent is kept for it).
func (s *Store) Resolve(ctx context.Context, ref string) (Agent, error) {
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "@")
	if ref == "" {
		return Agent{}, errors.New("empty agent reference")
	}
	// Current names first, then earlier names of online agents (a renamed or
	// adopted identity), then the most recent offline holder (its mail is
	// kept for it).
	for _, q := range []string{
		`SELECT ` + agentCols + ` FROM agents WHERE id=? OR (online=1 AND lower(name)=lower(?)) LIMIT 1`,
		// Only a name nobody holds any more: an agent that once wore a name
		// must not intercept the mail of the agent that owns it now.
		`SELECT ` + agentCols + ` FROM agents WHERE online=1 AND instr(','||lower(aka)||',', ','||lower(?)||',')>0
			AND NOT EXISTS (SELECT 1 FROM agents h WHERE lower(h.name)=lower(?)) ORDER BY last_seen DESC LIMIT 1`,
		`SELECT ` + agentCols + ` FROM agents WHERE lower(name)=lower(?) AND ?<>'' ORDER BY last_seen DESC LIMIT 1`,
	} {
		a, err := scanAgent(s.db.QueryRowContext(ctx, q, ref, ref))
		if err == nil {
			return a, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return a, err
		}
	}
	// Unique prefix among online agents.
	rows, err := s.db.QueryContext(ctx, `SELECT `+agentCols+` FROM agents WHERE online=1 AND lower(name) LIKE lower(?) || '%'`, ref)
	if err != nil {
		return Agent{}, err
	}
	defer rows.Close()
	var hits []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return Agent{}, err
		}
		hits = append(hits, a)
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return Agent{}, fmt.Errorf("no agent named %q (use list_agents)", ref)
	default:
		names := make([]string, len(hits))
		for i, h := range hits {
			names[i] = h.Name
		}
		return Agent{}, fmt.Errorf("%q is ambiguous: %s", ref, strings.Join(names, ", "))
	}
}
