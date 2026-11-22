package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/romanpert/ainterlinked/internal/transcript"
)

// Tokens are the owner's money (or the owner's plan's quota): every token a
// session spends has an author, is in plain sight, and stays within the
// owner's terms. The hub reads the spend from the harness's own transcript,
// which the agent doesn't write, and the owner's cap is checked before
// every tool call, which the agent can't skip.
//
// Whose spend it is follows whose turn it was. On a turn the user started
// it is the user's order. On a turn held open by peer mail it was spent at
// those peers' request: their facts show it, so asking others for work
// isn't free for the asker while the owner pays.

// Causes stored besides a sender's agent id.
const (
	causeOwn    = transcript.CauseOwn  // a turn the user started
	causeOthers = transcript.CausePeer // a turn another session started outside ainterlinked
	causeHeld   = "~held"              // held for mail the hub no longer has
)

// Spend is one session's spend so far.
type Spend struct {
	Agent    string            `json:"agent"`
	Own      transcript.Tokens `json:"own"`       // on turns the user started
	ForPeers transcript.Tokens `json:"for_peers"` // on turns peers started or held open
	Known    bool              `json:"known"`     // read from the harness's transcript
	Cap      int64             `json:"cap,omitempty"`
	Over     bool              `json:"over,omitempty"`
}

// Billed is what the cap counts (transcript.Tokens.Billed).
func (s Spend) Billed() int64 { return s.Own.Billed() + s.ForPeers.Billed() }

// SpendReq asks for an agent's spend, read up to now.
type SpendReq struct {
	Agent string `json:"agent"`
}

// BudgetReq sets the owner's cap: billed tokens one session may spend
// (0: no cap). By is the human: the owner's terms are the owner's to set.
type BudgetReq struct {
	By            string `json:"by"`
	SessionTokens int64  `json:"session_tokens"`
}

// Budget is the owner's current terms.
type Budget struct {
	SessionTokens int64 `json:"session_tokens"`
}

const budgetKey = "budget:session_tokens"

// SetBudget records the owner's cap. Only the human sets it.
func (s *Store) SetBudget(ctx context.Context, r BudgetReq) (Budget, error) {
	if me, err := s.Get(ctx, r.By); err != nil || me.Harness != "human" {
		return Budget{}, errors.New("only the owner sets the budget (the ainterlinked CLI or dashboard, outside any agent's session)")
	}
	if r.SessionTokens < 0 {
		return Budget{}, errors.New("a cap can't be negative (0 removes it)")
	}
	if r.SessionTokens == 0 {
		_, err := s.db.ExecContext(ctx, `DELETE FROM meta WHERE key=?`, budgetKey)
		return Budget{}, err
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)`, budgetKey, strconv.FormatInt(r.SessionTokens, 10))
	return Budget{SessionTokens: r.SessionTokens}, err
}

// GetBudget returns the owner's cap.
func (s *Store) GetBudget(ctx context.Context) (Budget, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, budgetKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return Budget{}, nil
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return Budget{SessionTokens: n}, err
}

// GetSpend returns the agent's spend as last recorded, with the owner's
// cap. The hub process reads the agent's transcript up to now first
// (RecordSpend); nothing else reads transcripts, since only a long-lived
// process can read one once and then only what's new.
func (s *Store) GetSpend(ctx context.Context, r SpendReq) (Spend, error) {
	me, err := s.Get(ctx, r.Agent)
	if err != nil {
		return Spend{}, err
	}
	out := Spend{Agent: me.ID}
	rows, err := s.db.QueryContext(ctx, `SELECT cause,tin,cwrite,cread,tout FROM spend WHERE agent_id=?`, me.ID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var cause string
		var t transcript.Tokens
		if err := rows.Scan(&cause, &t.In, &t.CacheWrite, &t.CacheRead, &t.Out); err != nil {
			return out, err
		}
		out.Known = true
		if cause == causeOwn {
			out.Own = out.Own.Add(t)
		} else {
			out.ForPeers = out.ForPeers.Add(t)
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	b, err := s.GetBudget(ctx)
	out.Cap = b.SessionTokens
	out.Over = out.Cap > 0 && out.Billed() >= out.Cap
	return out, err
}

// RecordSpend stores what the agent's transcript says it spent.
func (s *Store) RecordSpend(ctx context.Context, agent string, ledger transcript.Ledger) error {
	me, err := s.Get(ctx, agent)
	if err != nil {
		return err
	}
	return s.storeSpend(ctx, me, ledger)
}

// storeSpend replaces the agent's rows with the ledger's totals (absolute
// figures: reading again from scratch gives the same rows). A turn held
// for peer mail is charged to the senders of those messages, split evenly;
// only mail the hub delivered to this agent counts, so a transcript can't
// charge anyone else.
func (s *Store) storeSpend(ctx context.Context, me Agent, ledger transcript.Ledger) error {
	by := map[string]transcript.Tokens{}
	for key, t := range ledger {
		switch key {
		case causeOwn, causeOthers:
			by[key] = by[key].Add(t)
			continue
		}
		senders, err := s.heldBy(ctx, me.ID, key)
		if err != nil {
			return err
		}
		if len(senders) == 0 {
			by[causeHeld] = by[causeHeld].Add(t)
			continue
		}
		for i, part := range split(t, len(senders)) {
			by[senders[i]] = by[senders[i]].Add(part)
		}
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		// A transcript only grows: a read that comes out smaller than
		// what's recorded (a file swapped for an emptier one, a forged
		// smaller total) is not believed, and the record stands.
		var was, total int64
		_ = tx.QueryRow(`SELECT coalesce(sum(tin+cwrite+cread+tout),0) FROM spend WHERE agent_id=?`, me.ID).Scan(&was)
		for _, t := range by {
			total += t.Billed() + t.CacheRead
		}
		if total < was {
			return nil
		}
		if _, err := tx.Exec(`DELETE FROM spend WHERE agent_id=?`, me.ID); err != nil {
			return err
		}
		now := s.ms()
		for cause, t := range by {
			if _, err := tx.Exec(`INSERT INTO spend(agent_id,cause,tin,cwrite,cread,tout,updated_at) VALUES(?,?,?,?,?,?,?)`,
				me.ID, cause, t.In, t.CacheWrite, t.CacheRead, t.Out, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// heldBy returns the distinct senders (other than me) of the messages in a
// held turn's key ("#12,#15") that were delivered to me, in a stable order.
func (s *Store) heldBy(ctx context.Context, me, key string) ([]string, error) {
	var ids []any
	for _, part := range strings.Split(key, ",") {
		if n, err := strconv.ParseInt(strings.TrimPrefix(part, "#"), 10, 64); err == nil && n > 0 {
			ids = append(ids, n)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	q := `SELECT DISTINCT m.sender_id FROM messages m JOIN receipts r ON r.message_id=m.id AND r.agent_id=?
		WHERE m.sender_id<>? AND m.id IN (?` + strings.Repeat(",?", len(ids)-1) + `)`
	rows, err := s.db.QueryContext(ctx, q, append([]any{me, me}, ids...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, rows.Err()
}

// split divides t into n parts that add up to t.
func split(t transcript.Tokens, n int) []transcript.Tokens {
	part := func(v int64, i int) int64 {
		q := v / int64(n)
		if int64(i) < v%int64(n) {
			q++
		}
		return q
	}
	out := make([]transcript.Tokens, n)
	for i := range out {
		out[i] = transcript.Tokens{In: part(t.In, i), CacheWrite: part(t.CacheWrite, i), CacheRead: part(t.CacheRead, i), Out: part(t.Out, i)}
	}
	return out
}

// spendFacts fills the token facts of the agents named (lower-cased), over
// the facts window: what they used, what of it went on peers' requests,
// and what their mail made others spend.
func (s *Store) spendFacts(ctx context.Context, since int64, names []string, out map[string]Facts) error {
	if len(names) == 0 {
		return nil
	}
	in := strings.TrimSuffix(strings.Repeat("?,", len(names)), ",")
	args := []any{since}
	for _, n := range names {
		args = append(args, strings.ToLower(n))
	}
	rows, err := s.db.QueryContext(ctx, `SELECT lower(a.name), sp.cause, sp.tin+sp.cwrite+sp.tout FROM spend sp JOIN agents a ON a.id=sp.agent_id
		WHERE sp.updated_at>=? AND lower(a.name) IN (`+in+`)`, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name, cause string
		var billed int64
		if err := rows.Scan(&name, &cause, &billed); err != nil {
			rows.Close()
			return err
		}
		f := out[name]
		f.Tokens += billed
		if cause != causeOwn {
			f.TokensForPeers += billed
		}
		out[name] = f
	}
	rows.Close()
	rows, err = s.db.QueryContext(ctx, `SELECT lower(a.name), sum(sp.tin+sp.cwrite+sp.tout) FROM spend sp JOIN agents a ON a.id=sp.cause
		WHERE sp.updated_at>=? AND lower(a.name) IN (`+in+`) GROUP BY lower(a.name)`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var billed int64
		if err := rows.Scan(&name, &billed); err != nil {
			return err
		}
		f := out[name]
		f.TokensCaused += billed
		out[name] = f
	}
	return rows.Err()
}

// FormatTokens writes a token count the way people read it: 950, 12k, 3.4M.
func FormatTokens(n int64) string {
	switch {
	case n >= 10_000_000:
		return fmt.Sprintf("%dM", n/1_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return strconv.FormatInt(n, 10)
}
