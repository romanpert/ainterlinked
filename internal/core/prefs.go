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

// Delivery modes of a message for one recipient.
const (
	ModeFull  = "full"  // the whole body reaches the model
	ModeBrief = "brief" // one line (subject or the start of the body) + get_thread id
	ModeOff   = "off"   // not delivered at all (urgent messages still are)
)

// Injection cadences: when hooks put unread mail into the model's context.
const (
	InjectEveryTool = "every_tool" // after tool calls, at prompts and at stop
	InjectTurn      = "turn"       // only when the user prompts and when the turn ends
	InjectManual    = "manual"     // never: the agent calls read_inbox itself
)

// DefaultBroadcasts is how group messages (project, branch, *, harness) reach
// an agent that hasn't chosen: one line each, so twenty-odd sessions in one
// repo don't fill each other's context with announcements.
const DefaultBroadcasts = ModeBrief

// BriefTTL is how long an unread brief stays worth showing. An agent that
// comes back after hours isn't shown stale announcements; read_inbox all=true
// still has them.
const BriefTTL = 6 * time.Hour

// Prefs are an agent's own delivery settings. Zero values mean "hub/config
// default", so an agent only stores what it changed.
type Prefs struct {
	Broadcasts  string            `json:"broadcasts,omitempty"`   // full | brief | off
	Inject      string            `json:"inject,omitempty"`       // every_tool | turn | manual
	MaxMessages int               `json:"max_messages,omitempty"` // per injection
	MaxChars    int               `json:"max_chars,omitempty"`    // per injection
	Topics      map[string]string `json:"topics,omitempty"`       // topic -> full | brief
}

// PrefsReq changes an agent's prefs. Empty fields are left unchanged.
type PrefsReq struct {
	Agent       string   `json:"agent"`
	Broadcasts  string   `json:"broadcasts,omitempty"`
	Inject      string   `json:"inject,omitempty"`
	MaxMessages int      `json:"max_messages,omitempty"`
	MaxChars    int      `json:"max_chars,omitempty"`
	Subscribe   []string `json:"subscribe,omitempty"`   // topics; "name:brief" for one-line delivery
	Unsubscribe []string `json:"unsubscribe,omitempty"` // topics
	Reset       bool     `json:"reset,omitempty"`       // back to defaults (topics kept)
}

// Limits on what an agent may ask for, so a bad value can't flood a context
// or starve an agent of its mail.
const (
	maxPrefMessages = 50
	maxPrefChars    = 40000
	maxTopics       = 50
)

func validMode(m string, allowOff bool) bool {
	return m == ModeFull || m == ModeBrief || (allowOff && m == ModeOff)
}

func validInject(m string) bool {
	return m == InjectEveryTool || m == InjectTurn || m == InjectManual
}

// normTopic turns "#Reload", "reload" or " reload " into "reload".
func normTopic(t string) string {
	t = strings.TrimPrefix(strings.TrimSpace(t), "#")
	return sanitizeName(t)
}

// GetPrefs returns an agent's prefs (defaults left as zero values).
func (s *Store) GetPrefs(ctx context.Context, agent string) (Prefs, error) {
	return s.prefs(ctx, s.db, agent)
}

func (s *Store) prefs(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, agent string) (Prefs, error) {
	var p Prefs
	err := q.QueryRowContext(ctx, `SELECT broadcasts,inject,max_messages,max_chars FROM prefs WHERE agent_id=?`, agent).
		Scan(&p.Broadcasts, &p.Inject, &p.MaxMessages, &p.MaxChars)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	rows, err := q.QueryContext(ctx, `SELECT topic,mode FROM subscriptions WHERE agent_id=? ORDER BY topic`, agent)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var t, m string
		if err := rows.Scan(&t, &m); err != nil {
			return p, err
		}
		if p.Topics == nil {
			p.Topics = map[string]string{}
		}
		p.Topics[t] = m
	}
	return p, rows.Err()
}

// SetPrefs applies an agent's own delivery settings and returns the result.
func (s *Store) SetPrefs(ctx context.Context, r PrefsReq) (Prefs, error) {
	if r.Broadcasts != "" && !validMode(r.Broadcasts, true) {
		return Prefs{}, errors.New("broadcasts must be full, brief or off")
	}
	if r.Inject != "" && !validInject(r.Inject) {
		return Prefs{}, errors.New("inject must be every_tool, turn or manual")
	}
	if r.MaxMessages < 0 || r.MaxMessages > maxPrefMessages {
		return Prefs{}, fmt.Errorf("max_messages must be 1-%d", maxPrefMessages)
	}
	if r.MaxChars < 0 || (r.MaxChars > 0 && r.MaxChars < 500) || r.MaxChars > maxPrefChars {
		return Prefs{}, fmt.Errorf("max_chars must be 500-%d", maxPrefChars)
	}
	me, err := s.Get(ctx, r.Agent)
	if err != nil {
		return Prefs{}, err
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if r.Reset {
			if _, err := tx.Exec(`DELETE FROM prefs WHERE agent_id=?`, me.ID); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`INSERT INTO prefs(agent_id,broadcasts,inject,max_messages,max_chars) VALUES(?,?,?,?,?)
			ON CONFLICT(agent_id) DO UPDATE SET
			  broadcasts=CASE WHEN excluded.broadcasts<>'' THEN excluded.broadcasts ELSE broadcasts END,
			  inject=CASE WHEN excluded.inject<>'' THEN excluded.inject ELSE inject END,
			  max_messages=CASE WHEN excluded.max_messages>0 THEN excluded.max_messages ELSE max_messages END,
			  max_chars=CASE WHEN excluded.max_chars>0 THEN excluded.max_chars ELSE max_chars END`,
			me.ID, r.Broadcasts, r.Inject, r.MaxMessages, r.MaxChars)
		if err != nil {
			return err
		}
		for _, raw := range r.Unsubscribe {
			if _, err := tx.Exec(`DELETE FROM subscriptions WHERE agent_id=? AND topic=?`, me.ID, normTopic(raw)); err != nil {
				return err
			}
		}
		for _, raw := range r.Subscribe {
			topic, mode := raw, ModeFull
			if i := strings.LastIndex(raw, ":"); i > 0 {
				topic, mode = raw[:i], strings.ToLower(strings.TrimSpace(raw[i+1:]))
			}
			t := normTopic(topic)
			if t == "" {
				return fmt.Errorf("bad topic %q", raw)
			}
			if !validMode(mode, false) {
				return fmt.Errorf("topic %q: mode must be full or brief", raw)
			}
			if _, err := tx.Exec(`INSERT INTO subscriptions(agent_id,topic,mode) VALUES(?,?,?)
				ON CONFLICT(agent_id,topic) DO UPDATE SET mode=excluded.mode`, me.ID, t, mode); err != nil {
				return err
			}
		}
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM subscriptions WHERE agent_id=?`, me.ID).Scan(&n); err != nil {
			return err
		}
		if n > maxTopics {
			return fmt.Errorf("at most %d topics", maxTopics)
		}
		return nil
	})
	if err != nil {
		return Prefs{}, err
	}
	return s.GetPrefs(ctx, me.ID)
}

// Topics lists the topics with subscribers in a project, with their counts,
// so an agent can see which channels exist before posting.
func (s *Store) Topics(ctx context.Context, projectPath string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.topic, count(*) FROM subscriptions s JOIN agents a ON a.id=s.agent_id
		WHERE a.online=1 AND a.project_path=? GROUP BY s.topic`, projectPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var t string
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			return nil, err
		}
		out[t] = n
	}
	return out, rows.Err()
}

// SortedTopics renders a topic map deterministically ("a(2), b(1)").
func SortedTopics(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("#%s(%d)", k, m[k])
	}
	return strings.Join(parts, ", ")
}

// broadcastMode is how group mail reaches an agent.
func (s *Store) broadcastMode(ctx context.Context, agent string) string {
	var m string
	_ = s.db.QueryRowContext(ctx, `SELECT broadcasts FROM prefs WHERE agent_id=?`, agent).Scan(&m)
	if validMode(m, true) {
		return m
	}
	return DefaultBroadcasts
}

// stronger keeps the richer of two delivery modes.
func stronger(a, b string) string {
	rank := map[string]int{"": 0, ModeOff: 1, ModeBrief: 2, ModeFull: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}
