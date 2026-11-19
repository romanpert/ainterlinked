package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Message is a note from one agent to one or more others.
type Message struct {
	ID        int64    `json:"id"`
	ThreadID  int64    `json:"thread_id"`
	ReplyTo   int64    `json:"reply_to,omitempty"`
	FromID    string   `json:"from_id"`
	From      string   `json:"from"`
	Target    string   `json:"to"`
	Subject   string   `json:"subject,omitempty"`
	Body      string   `json:"body"`
	Urgent    bool     `json:"urgent,omitempty"`
	CreatedAt int64    `json:"created_at"`
	Delivered []string `json:"delivered_to,omitempty"`
}

// SendReq sends a message. To accepts an agent name or id, "*" (everyone
// online), "project" (online agents in the sender's project),
// "project:<name>", "harness:<name>", or a comma-separated mix.
type SendReq struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Subject string `json:"subject,omitempty"`
	Body    string `json:"body"`
	ReplyTo int64  `json:"reply_to,omitempty"`
	Urgent  bool   `json:"urgent,omitempty"`
}

const msgCols = `id,thread_id,reply_to,sender_id,sender_name,target,subject,body,urgent,created_at`

func scanMsg(r scanner) (Message, error) {
	var m Message
	var urgent int
	err := r.Scan(&m.ID, &m.ThreadID, &m.ReplyTo, &m.FromID, &m.From, &m.Target, &m.Subject, &m.Body, &urgent, &m.CreatedAt)
	m.Urgent = urgent == 1
	return m, err
}

// MaxBody caps message size so one agent can't flood another's context.
const MaxBody = 16000

// Send stores a message and fans it out to its recipients.
func (s *Store) Send(ctx context.Context, r SendReq) (Message, error) {
	if strings.TrimSpace(r.Body) == "" {
		return Message{}, errors.New("body is required")
	}
	if len(r.Body) > MaxBody {
		return Message{}, fmt.Errorf("body too long (%d > %d bytes); put big content in a file and send its path", len(r.Body), MaxBody)
	}
	sender, err := s.Get(ctx, r.From)
	if err != nil {
		return Message{}, err
	}
	var parent Message
	if r.ReplyTo > 0 {
		parent, err = scanMsg(s.db.QueryRowContext(ctx, `SELECT `+msgCols+` FROM messages WHERE id=?`, r.ReplyTo))
		if err != nil {
			return Message{}, fmt.Errorf("reply_to: no message #%d", r.ReplyTo)
		}
	}
	display := r.To
	if strings.TrimSpace(r.To) == "" && r.ReplyTo > 0 {
		r.To, display = parent.FromID, parent.From // resolve by id, show the name
	}
	if strings.TrimSpace(r.To) == "" {
		return Message{}, errors.New("to is required (agent name, '*', 'project', 'project:<name>' or 'harness:<name>')")
	}
	recips, err := s.recipients(ctx, sender, r.To)
	if err != nil {
		return Message{}, err
	}
	if len(recips) == 0 {
		return Message{}, fmt.Errorf("no recipients online for %q", r.To)
	}
	var out Message
	err = s.tx(ctx, func(tx *sql.Tx) error {
		now := s.ms()
		res, err := tx.Exec(`INSERT INTO messages(thread_id,reply_to,sender_id,sender_name,target,subject,body,urgent,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			parent.ThreadID, r.ReplyTo, sender.ID, sender.Name, display, oneLine(r.Subject, 200), r.Body, b2i(r.Urgent), now)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		thread := parent.ThreadID
		if thread == 0 {
			thread = id
			if _, err := tx.Exec(`UPDATE messages SET thread_id=? WHERE id=?`, id, id); err != nil {
				return err
			}
		}
		names := make([]string, 0, len(recips))
		for _, a := range recips {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO receipts(message_id,agent_id) VALUES(?,?)`, id, a.ID); err != nil {
				return err
			}
			names = append(names, a.Name)
		}
		_, _ = tx.Exec(`UPDATE agents SET last_seen=? WHERE id=?`, now, sender.ID)
		summary := r.Subject
		if summary == "" {
			summary = r.Body
		}
		event(tx, now, &sender, "message", fmt.Sprintf("%s → %s: %s", sender.Name, strings.Join(names, ", "), clip(summary, 120)))
		out = Message{ID: id, ThreadID: thread, ReplyTo: r.ReplyTo, FromID: sender.ID, From: sender.Name, Target: display,
			Subject: r.Subject, Body: r.Body, Urgent: r.Urgent, CreatedAt: now, Delivered: names}
		return nil
	})
	return out, err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) recipients(ctx context.Context, sender Agent, to string) ([]Agent, error) {
	seen := map[string]bool{sender.ID: true}
	var out []Agent
	add := func(list []Agent) {
		for _, a := range list {
			if !seen[a.ID] {
				seen[a.ID] = true
				out = append(out, a)
			}
		}
	}
	for _, part := range strings.Split(to, ",") {
		part = strings.TrimSpace(part)
		switch {
		case part == "":
		case part == "*" || part == "all" || part == "everyone":
			list, err := s.List(ctx, AgentFilter{})
			if err != nil {
				return nil, err
			}
			add(list)
		case part == "project" || part == "team":
			if sender.ProjectPath == "" {
				return nil, errors.New(`you have no project; use "project:<name>" or "*"`)
			}
			list, err := s.List(ctx, AgentFilter{Project: sender.ProjectPath})
			if err != nil {
				return nil, err
			}
			add(list)
		case strings.HasPrefix(part, "project:"):
			list, err := s.List(ctx, AgentFilter{Project: strings.TrimPrefix(part, "project:")})
			if err != nil {
				return nil, err
			}
			add(list)
		case strings.HasPrefix(part, "harness:"):
			list, err := s.List(ctx, AgentFilter{Harness: strings.TrimPrefix(part, "harness:")})
			if err != nil {
				return nil, err
			}
			add(list)
		default:
			a, err := s.Resolve(ctx, part)
			if err != nil {
				return nil, err
			}
			if a.ID == sender.ID {
				return nil, errors.New("you can't message yourself")
			}
			add([]Agent{a})
		}
	}
	return out, nil
}

// InboxReq reads an agent's messages.
type InboxReq struct {
	Agent      string `json:"agent"`
	All        bool   `json:"all,omitempty"`   // include already-read messages
	Peek       bool   `json:"peek,omitempty"`  // don't mark as read
	Limit      int    `json:"limit,omitempty"` // default 20
	UrgentOnly bool   `json:"urgent_only,omitempty"`
}

// Inbox returns messages for an agent, oldest first, marking them read
// unless Peek is set.
func (s *Store) Inbox(ctx context.Context, r InboxReq) ([]Message, error) {
	if r.Limit <= 0 || r.Limit > 200 {
		r.Limit = 20
	}
	query := `SELECT m.` + strings.ReplaceAll(msgCols, ",", ",m.") + ` FROM receipts r JOIN messages m ON m.id=r.message_id WHERE r.agent_id=?`
	if !r.All {
		query += ` AND r.read_at=0`
	}
	if r.UrgentOnly {
		query += ` AND m.urgent=1`
	}
	// Unread: oldest first so nothing is skipped. History: the latest N.
	if r.All {
		query += ` ORDER BY m.id DESC LIMIT ?`
	} else {
		query += ` ORDER BY m.urgent DESC, m.id ASC LIMIT ?`
	}
	read := func(q interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}) ([]Message, error) {
		rows, err := q.QueryContext(ctx, query, r.Agent, r.Limit)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []Message
		for rows.Next() {
			m, err := scanMsg(rows)
			if err != nil {
				return nil, err
			}
			out = append(out, m)
		}
		return out, rows.Err()
	}
	var out []Message
	var err error
	if r.Peek {
		out, err = read(s.db)
	} else {
		// Select and mark in one immediate (write-locked) transaction, and
		// return only rows this call actually claimed: concurrent readers
		// (parallel tool hooks, wait_for_messages, the channel push) never
		// get the same message twice.
		err = s.tx(ctx, func(tx *sql.Tx) error {
			got, err := read(tx)
			if err != nil {
				return err
			}
			now := s.ms()
			for _, m := range got {
				res, err := tx.Exec(`UPDATE receipts SET read_at=? WHERE message_id=? AND agent_id=? AND read_at=0`, now, m.ID, r.Agent)
				if err != nil {
					return err
				}
				if n, _ := res.RowsAffected(); n == 1 || r.All {
					out = append(out, m)
				}
			}
			_, err = tx.Exec(`UPDATE agents SET last_seen=? WHERE id=? AND online=1`, now, r.Agent)
			return err
		})
	}
	if r.All {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, err
}

// Unread counts unread messages for an agent.
func (s *Store) Unread(ctx context.Context, agent string) (total, urgent int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(m.urgent),0) FROM receipts r JOIN messages m ON m.id=r.message_id
		WHERE r.agent_id=? AND r.read_at=0`, agent).Scan(&total, &urgent)
	return
}

// Wait blocks until the agent has unread messages or the timeout elapses,
// then returns them (marked read). It is the "real-time" primitive for
// harnesses that can only pull.
func (s *Store) Wait(ctx context.Context, agent string, timeout time.Duration) ([]Message, error) {
	if timeout <= 0 || timeout > 30*time.Minute {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	beat := time.Now()
	for {
		n, _, err := s.Unread(ctx, agent)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			return s.Inbox(ctx, InboxReq{Agent: agent})
		}
		if time.Now().After(deadline) {
			return nil, nil
		}
		if time.Since(beat) > 5*time.Second { // waiting counts as alive
			beat = time.Now()
			_, _ = s.db.ExecContext(ctx, `UPDATE agents SET last_seen=? WHERE id=? AND online=1`, s.ms(), agent)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
		}
	}
}

// Thread returns every message in a thread, oldest first. Only participants
// (sender or recipient of some message in it) may read it.
func (s *Store) Thread(ctx context.Context, agent string, id int64) ([]Message, error) {
	var thread int64
	if err := s.db.QueryRowContext(ctx, `SELECT thread_id FROM messages WHERE id=?`, id).Scan(&thread); err != nil {
		return nil, fmt.Errorf("no message #%d", id)
	}
	var n int
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM messages m LEFT JOIN receipts r ON r.message_id=m.id
		WHERE m.thread_id=? AND (m.sender_id=? OR r.agent_id=?)`, thread, agent, agent).Scan(&n)
	if n == 0 && agent != "" {
		return nil, fmt.Errorf("you are not part of thread #%d", thread)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+msgCols+` FROM messages WHERE thread_id=? ORDER BY id`, thread)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMsg(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// RecentMessages returns the latest messages across the bus (dashboard/CLI).
func (s *Store) RecentMessages(ctx context.Context, limit int) ([]Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+msgCols+` FROM messages ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMsg(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
