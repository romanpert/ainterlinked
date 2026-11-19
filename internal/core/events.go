package core

import "context"

// Event is one line of the shared activity feed.
type Event struct {
	ID      int64  `json:"id"`
	TS      int64  `json:"ts"`
	AgentID string `json:"agent_id,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Project string `json:"project,omitempty"`
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
}

// FeedReq selects feed events.
type FeedReq struct {
	SinceID int64  `json:"since_id,omitempty"`
	Project string `json:"project,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

// Feed returns events newest-last. With SinceID it returns only newer ones.
func (s *Store) Feed(ctx context.Context, r FeedReq) ([]Event, error) {
	if r.Limit <= 0 || r.Limit > 500 {
		r.Limit = 50
	}
	q := `SELECT id,ts,agent_id,agent_name,project,kind,summary FROM events WHERE id>?`
	args := []any{r.SinceID}
	if r.Project != "" {
		q += ` AND lower(project)=lower(?)`
		args = append(args, r.Project)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, r.Limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.TS, &e.AgentID, &e.Agent, &e.Project, &e.Kind, &e.Summary); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// Note appends a free-form event (e.g. "deployed staging") to the feed.
func (s *Store) Note(ctx context.Context, agent, kind, summary string) error {
	a, err := s.Get(ctx, agent)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	event(tx, s.ms(), &a, kind, summary)
	return tx.Commit()
}
