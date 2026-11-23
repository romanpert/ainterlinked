package core

import (
	"context"
	"strings"
	"time"
)

// Facts are what the bus has seen an agent do lately, by its name (a name
// carries over sessions with continue_as, so it is the agent's lineage).
// They are not a score and nothing ranks by them: whoever chooses whom to
// wait for, hand work to or trust weighs them as they see fit.
//
// What an agent did on a turn the user started, under the user's order,
// is the user's act, not the agent's: it isn't in its facts.
type Facts struct {
	Taken int `json:"taken,omitempty"` // tasks it took (claimed or was given) lately
	Done  int `json:"done,omitempty"`
	Open  int `json:"open,omitempty"` // of those, still doing, blocked or in review
	// Shell writes to files others held, on its own initiative, and how
	// many of those the holder (or the human) hasn't settled yet. Only the hub
	// records these (RecordTrespass), so nobody can pin one on someone else.
	Trespass     int `json:"trespass,omitempty"`
	TrespassOpen int `json:"trespass_open,omitempty"`
	// Reservations it holds now.
	Holds []string `json:"holds,omitempty"`
	// Messages it sent to many at once (project, *, a branch, a topic…).
	Broadcasts int `json:"broadcasts,omitempty"`
	// The viewer's own messages this agent hasn't read yet.
	UnreadFromViewer int `json:"unread_from_you,omitempty"`
	// Tokens its sessions used (billed: cache reads aside), of those spent
	// on turns peers started or held open, and what its mail made other
	// agents spend. Read from the harnesses' transcripts (spend.go).
	Tokens         int64 `json:"tokens,omitempty"`
	TokensForPeers int64 `json:"tokens_for_peers,omitempty"`
	TokensCaused   int64 `json:"tokens_caused,omitempty"`
}

// Empty says there's nothing to show.
func (f Facts) Empty() bool {
	return f.Taken == 0 && f.Trespass == 0 && len(f.Holds) == 0 && f.Broadcasts == 0 && f.UnreadFromViewer == 0 &&
		f.Tokens == 0 && f.TokensCaused == 0
}

// FactsWindow is how far back facts go: old deeds fade.
const FactsWindow = 7 * 24 * time.Hour

// Labels the trespass notice puts on its tasks (internal/hooks).
const (
	LabelRestitution = "restitution" // on the agent's own initiative
	LabelReconcile   = "reconcile"   // on the user's order: the user's act
)

// AgentFacts returns the facts of the agents named, as seen by viewer.
// A handful of grouped queries, whatever the number of names.
func (s *Store) AgentFacts(ctx context.Context, viewer string, names []string) (map[string]Facts, error) {
	out := map[string]Facts{}
	if len(names) == 0 {
		return out, nil
	}
	key := func(n string) string { return strings.ToLower(n) }
	in := strings.TrimSuffix(strings.Repeat("?,", len(names)), ",")
	args := func(extra ...any) []any {
		a := append([]any{}, extra...)
		for _, n := range names {
			a = append(a, key(n))
		}
		return a
	}
	now := s.ms()
	since := now - FactsWindow.Milliseconds()
	// Tasks taken, done, open: not the trespass ones (those aren't work it
	// chose). A task it still holds counts however old: sitting on a claim
	// is a fact too. Archived tasks count as well: archiving tidies a board,
	// it doesn't change what an agent did.
	trespassLabel := `(instr(','||labels||',', ',` + LabelRestitution + `,')>0 OR instr(','||labels||',', ',` + LabelReconcile + `,')>0)`
	if err := s.group(ctx, out, `SELECT lower(assignee), count(*), coalesce(sum(status='done'),0), coalesce(sum(status IN ('doing','blocked','review')),0)
		FROM `+everyTask+` WHERE (updated_at>? AND status<>'todo' OR status IN ('doing','blocked','review')) AND NOT `+trespassLabel+`
		AND lower(assignee) IN (`+in+`) GROUP BY lower(assignee)`,
		args(since), func(f *Facts, v []int64) { f.Taken, f.Done, f.Open = int(v[0]), int(v[1]), int(v[2]) }); err != nil {
		return nil, err
	}
	if err := s.group(ctx, out, `SELECT lower(assignee), count(*), coalesce(sum(status NOT IN ('done','cancelled')),0)
		FROM `+everyTask+` WHERE created_at>? AND instr(','||labels||',', ',`+LabelRestitution+`,')>0 AND lower(assignee) IN (`+in+`) GROUP BY lower(assignee)`,
		args(since), func(f *Facts, v []int64) { f.Trespass, f.TrespassOpen = int(v[0]), int(v[1]) }); err != nil {
		return nil, err
	}
	if err := s.group(ctx, out, `SELECT lower(sender_name), count(*) FROM messages
		WHERE created_at>? AND (target IN ('*','project','branch') OR instr(target, ':')>0 OR substr(target,1,1)='#') AND lower(sender_name) IN (`+in+`)
		GROUP BY lower(sender_name)`,
		args(since), func(f *Facts, v []int64) { f.Broadcasts = int(v[0]) }); err != nil {
		return nil, err
	}
	if viewer != "" {
		if err := s.group(ctx, out, `SELECT lower(a.name), count(*) FROM messages m JOIN receipts r ON r.message_id=m.id JOIN agents a ON a.id=r.agent_id
			WHERE m.sender_id=? AND m.created_at>? AND r.read_at=0 AND r.mode='full' AND lower(a.name) IN (`+in+`) GROUP BY lower(a.name)`,
			args(viewer, since), func(f *Facts, v []int64) { f.UnreadFromViewer = int(v[0]) }); err != nil {
			return nil, err
		}
	}
	if err := s.spendFacts(ctx, since, names, out); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT lower(agent_name), pattern FROM locks WHERE expires_at>? AND lower(agent_name) IN (`+in+`) ORDER BY id`, args(now)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, pat string
		if err := rows.Scan(&name, &pat); err != nil {
			return nil, err
		}
		f := out[name]
		f.Holds = append(f.Holds, pat)
		out[name] = f
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for k, f := range out {
		if f.Empty() {
			delete(out, k)
		}
	}
	return out, nil
}

// group runs a query whose rows are (name, n1, n2…) and folds them in.
func (s *Store) group(ctx context.Context, out map[string]Facts, q string, args []any, set func(*Facts, []int64)) error {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	for rows.Next() {
		var name string
		v := make([]int64, len(cols)-1)
		dst := []any{&name}
		for i := range v {
			dst = append(dst, &v[i])
		}
		if err := rows.Scan(dst...); err != nil {
			return err
		}
		f := out[name]
		set(&f, v)
		out[name] = f
	}
	return rows.Err()
}
