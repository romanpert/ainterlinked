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
	Delivered []string `json:"delivered_to,omitempty"` // recipients that get the full body
	Briefed   []string `json:"briefed_to,omitempty"`   // recipients that get one line
	// Away are full recipients that won't see it until their session
	// resumes (idle without push delivery, or offline): don't block on them.
	Away []string `json:"away,omitempty"`
	Mode string   `json:"mode,omitempty"` // for a reader: full or brief
}

// SystemSender is the sender id/name of notices the hub itself sends (a
// reservation you queued for is now yours, someone is waiting for yours).
const SystemSender = "ainterlinked"

// LegacySystemSender is the id notices were sent under before the rename.
const LegacySystemSender = "interlinked"

// SendReq sends a message. To accepts an agent name or id, "*" (everyone
// online), "project" (online agents in the sender's project), "branch"
// (those also on the sender's git branch), "branch:<name>", "#topic"
// (subscribers of a topic in the sender's project), "project:<name>",
// "harness:<name>", or a comma-separated mix.
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

// Send stores a message and fans it out to its recipients, each in the
// delivery mode it chose: named recipients get the full body, group mail
// follows the recipient's broadcast/topic preference (urgent too, unless the
// human sent it). A broadcast spends one of the sender's BroadcastBurst.
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
		if parent.FromID == SystemSender || parent.FromID == LegacySystemSender {
			return Message{}, errors.New("that was an automatic notice from the hub; reply to an agent by name")
		}
		r.To, display = parent.FromID, parent.From // resolve by id, show the name
	}
	if strings.TrimSpace(r.To) == "" {
		return Message{}, errors.New("to is required (agent name, 'project', 'branch', '#topic', '*', 'project:<name>' or 'harness:<name>')")
	}
	recips, err := s.recipients(ctx, sender, r.To, r.Urgent)
	if err != nil {
		return Message{}, err
	}
	if len(recips) == 0 {
		return Message{}, fmt.Errorf("no recipients for %q (nobody online there, or they muted broadcasts: name an agent)", r.To)
	}
	if len(recips) == 1 && strings.TrimSpace(r.To) == recips[0].ID {
		display = recips[0].Name // addressed by id (hub notices, task updates): show the name
	}
	var out Message
	err = s.tx(ctx, func(tx *sql.Tx) error {
		now := s.ms()
		if isBroadcast(r.To, sender) && !isHuman(sender) {
			if err := s.lim().spendBroadcast(tx, now, sender); err != nil {
				return err
			}
		}
		res, err := tx.Exec(`INSERT INTO messages(thread_id,reply_to,sender_id,sender_name,target,subject,body,urgent,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			parent.ThreadID, r.ReplyTo, sender.ID, sender.Name, oneLine(display, 200), oneLine(r.Subject, 200), r.Body, b2i(r.Urgent), now)
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
		out = Message{ID: id, ThreadID: thread, ReplyTo: r.ReplyTo, FromID: sender.ID, From: sender.Name, Target: display,
			Subject: r.Subject, Body: r.Body, Urgent: r.Urgent, CreatedAt: now}
		for _, rc := range recips {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO receipts(message_id,agent_id,mode,direct) VALUES(?,?,?,?)`, id, rc.ID, rc.mode, b2i(rc.direct)); err != nil {
				return err
			}
			if rc.mode == ModeBrief {
				out.Briefed = append(out.Briefed, rc.Name)
			} else {
				out.Delivered = append(out.Delivered, rc.Name)
				if away(rc.Agent) {
					out.Away = append(out.Away, rc.Name)
				}
			}
		}
		_, _ = tx.Exec(`UPDATE agents SET last_seen=? WHERE id=?`, now, sender.ID)
		// The feed is readable by every agent and device: who wrote to whom
		// and the subject, never the body.
		line := fmt.Sprintf("%s → %s", sender.Name, feedTarget(display, len(recips)))
		if r.Subject != "" {
			line += ": " + clip(oneLine(r.Subject, 200), 120)
		}
		event(tx, now, &sender, "message", line)
		return nil
	})
	return out, err
}

// feedTarget keeps feed lines short: a group shows its name and size, not
// twenty recipient names.
func feedTarget(display string, n int) string {
	if n > 3 {
		return fmt.Sprintf("%s (%d)", oneLine(display, 60), n)
	}
	return oneLine(display, 120)
}

// away reports whether a recipient only sees new mail once its session
// resumes: offline, or idle between turns without a push channel (hook-only
// delivery waits for the next prompt, which may be hours away).
func away(a Agent) bool {
	if !a.Online || a.State == stateLeaving {
		return true
	}
	return (a.State == "idle" || a.State == "done") && !strings.Contains(a.Delivery, "push")
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// recipient is one agent a message goes to and how. direct means it was
// addressed by name or id: only such mail may hold its turn open.
type recipient struct {
	Agent
	mode   string
	direct bool
}

// recipients expands a target into agents and their delivery modes. The
// recipient's attention is its own: its broadcast and topic choices hold
// for urgent mail too. Only the human, whose sessions these are, can push
// urgent mail past them.
func (s *Store) recipients(ctx context.Context, sender Agent, to string, urgent bool) ([]recipient, error) {
	override := urgent && isHuman(sender)
	mode := map[string]string{}
	direct := map[string]bool{}
	var order []Agent
	add := func(a Agent, m string, named bool) {
		if a.ID == sender.ID || m == ModeOff || m == "" {
			return
		}
		if override {
			m = ModeFull
		}
		if _, ok := mode[a.ID]; !ok {
			order = append(order, a)
		}
		mode[a.ID] = stronger(mode[a.ID], m)
		direct[a.ID] = direct[a.ID] || named
	}
	group := func(list []Agent) {
		for _, a := range list {
			m := s.broadcastMode(ctx, a.ID)
			if override && m == ModeOff {
				m = ModeFull
			}
			add(a, m, false)
		}
	}
	needProject := func() error {
		if sender.ProjectPath == "" {
			return errors.New(`you have no project; use "project:<name>" or "*"`)
		}
		return nil
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
			group(list)
		case part == "project" || part == "team":
			if err := needProject(); err != nil {
				return nil, err
			}
			list, err := s.List(ctx, AgentFilter{Project: sender.ProjectPath})
			if err != nil {
				return nil, err
			}
			group(list)
		case part == "branch" || strings.HasPrefix(part, "branch:"):
			if err := needProject(); err != nil {
				return nil, err
			}
			branch := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(part, "branch"), ":"))
			if branch == "" {
				branch = sender.Branch
			}
			if branch == "" {
				return nil, errors.New("you are not on a git branch; use branch:<name>")
			}
			list, err := s.List(ctx, AgentFilter{Project: sender.ProjectPath})
			if err != nil {
				return nil, err
			}
			var on []Agent
			for _, a := range list {
				if strings.EqualFold(a.Branch, branch) {
					on = append(on, a)
				}
			}
			group(on)
		case strings.HasPrefix(part, "#"):
			if err := needProject(); err != nil {
				return nil, err
			}
			topic := normTopic(part)
			if topic == "" {
				return nil, fmt.Errorf("bad topic %q", part)
			}
			subs, err := s.subscribers(ctx, sender.ProjectPath, topic)
			if err != nil {
				return nil, err
			}
			for _, rc := range subs {
				add(rc.Agent, rc.mode, false)
			}
		case strings.HasPrefix(part, "project:"):
			list, err := s.List(ctx, AgentFilter{Project: strings.TrimPrefix(part, "project:")})
			if err != nil {
				return nil, err
			}
			group(list)
		case strings.HasPrefix(part, "harness:"):
			list, err := s.List(ctx, AgentFilter{Harness: strings.TrimPrefix(part, "harness:")})
			if err != nil {
				return nil, err
			}
			group(list)
		default:
			a, err := s.Resolve(ctx, part)
			if err != nil {
				return nil, err
			}
			if a.ID == sender.ID {
				return nil, errors.New("you can't message yourself")
			}
			add(a, ModeFull, true)
		}
	}
	out := make([]recipient, 0, len(order))
	for _, a := range order {
		out = append(out, recipient{a, mode[a.ID], direct[a.ID]})
	}
	return out, nil
}

// subscribers are the online agents of a project subscribed to a topic.
func (s *Store) subscribers(ctx context.Context, projectPath, topic string) ([]recipient, error) {
	list, err := s.List(ctx, AgentFilter{Project: projectPath})
	if err != nil {
		return nil, err
	}
	var out []recipient
	for _, a := range list {
		var m string
		err := s.db.QueryRowContext(ctx, `SELECT mode FROM subscriptions WHERE agent_id=? AND topic=?`, a.ID, topic).Scan(&m)
		if err == nil {
			out = append(out, recipient{Agent: a, mode: m})
		}
	}
	return out, nil
}

// notice queues a hub-generated message for one agent, inside the caller's
// tx. It is addressed to that agent, so it counts as direct mail.
func notice(tx *sql.Tx, now int64, to Agent, subject, body string) {
	res, err := tx.Exec(`INSERT INTO messages(thread_id,reply_to,sender_id,sender_name,target,subject,body,urgent,created_at) VALUES(0,0,?,?,?,?,?,0,?)`,
		SystemSender, SystemSender, to.Name, oneLine(subject, 200), body, now)
	if err != nil {
		return
	}
	id, _ := res.LastInsertId()
	_, _ = tx.Exec(`UPDATE messages SET thread_id=? WHERE id=?`, id, id)
	_, _ = tx.Exec(`INSERT OR IGNORE INTO receipts(message_id,agent_id,mode,direct) VALUES(?,?,?,1)`, id, to.ID, ModeFull)
}

// InboxReq reads an agent's messages.
type InboxReq struct {
	Agent      string `json:"agent"`
	All        bool   `json:"all,omitempty"`   // include already-read messages
	Peek       bool   `json:"peek,omitempty"`  // don't mark as read
	Limit      int    `json:"limit,omitempty"` // default 20
	UrgentOnly bool   `json:"urgent_only,omitempty"`
	Mode       string `json:"mode,omitempty"`   // only mail delivered in this mode: full or brief
	Direct     bool   `json:"direct,omitempty"` // only mail addressed to this agent by name or id
	// Budget caps the characters taken in one read (0 = no cap); messages
	// past it stay unread for the next one instead of being marked read
	// and cut off. The first message is always taken.
	Budget int `json:"budget,omitempty"`
}

// unreadFilter hides briefs older than BriefTTL: stale announcements.
const unreadFilter = ` AND NOT (r.mode='brief' AND m.created_at < ?)`

// Cost estimates the characters a message takes when shown to a model.
func Cost(m Message) int {
	head := len(m.From) + len(m.Target) + 120
	if m.Mode == ModeBrief {
		return head + 200
	}
	return head + len(m.Body) + len(m.Subject)
}

// Inbox returns messages for an agent, oldest first, marking them read
// unless Peek is set.
func (s *Store) Inbox(ctx context.Context, r InboxReq) ([]Message, error) {
	if r.Limit <= 0 || r.Limit > 200 {
		r.Limit = 20
	}
	staleCut := s.briefCut()
	query := `SELECT m.` + strings.ReplaceAll(msgCols, ",", ",m.") + `,r.mode FROM receipts r JOIN messages m ON m.id=r.message_id WHERE r.agent_id=?`
	args := []any{r.Agent}
	if !r.All {
		query += ` AND r.read_at=0` + unreadFilter
		args = append(args, staleCut)
	}
	if r.UrgentOnly {
		query += ` AND m.urgent=1`
	}
	if r.Mode != "" {
		query += ` AND r.mode=?`
		args = append(args, r.Mode)
	}
	if r.Direct {
		query += ` AND r.direct=1`
	}
	// Unread: oldest first so nothing is skipped (full mail before briefs).
	// History: the latest N.
	if r.All {
		query += ` ORDER BY m.id DESC LIMIT ?`
	} else {
		query += ` ORDER BY m.urgent DESC, r.mode='brief', m.id ASC LIMIT ?`
	}
	args = append(args, r.Limit)
	read := func(q interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}) ([]Message, error) {
		rows, err := q.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []Message
		for rows.Next() {
			var m Message
			var urgent int
			if err := rows.Scan(&m.ID, &m.ThreadID, &m.ReplyTo, &m.FromID, &m.From, &m.Target, &m.Subject, &m.Body, &urgent, &m.CreatedAt, &m.Mode); err != nil {
				return nil, err
			}
			m.Urgent = urgent == 1
			out = append(out, m)
		}
		return out, rows.Err()
	}
	within := func(got []Message) []Message {
		if r.Budget <= 0 {
			return got
		}
		spent := 0
		for i, m := range got {
			spent += Cost(m)
			if spent > r.Budget && i > 0 {
				return got[:i]
			}
		}
		return got
	}
	var out []Message
	var err error
	if r.Peek {
		out, err = read(s.db)
		out = within(out)
	} else {
		// Select and mark in one immediate (write-locked) transaction, and
		// return only rows this call actually claimed: concurrent readers
		// (parallel tool hooks, wait_for_messages, the channel push) never
		// get the same message twice.
		err = s.tx(ctx, func(tx *sql.Tx) error {
			now := s.ms()
			if !r.All {
				// Stale briefs are dropped here, so they stop counting as unread.
				if _, err := tx.Exec(`UPDATE receipts SET read_at=? WHERE agent_id=? AND read_at=0 AND mode='brief'
					AND message_id IN (SELECT id FROM messages WHERE created_at < ?)`, now, r.Agent, staleCut); err != nil {
					return err
				}
			}
			got, err := read(tx)
			if err != nil {
				return err
			}
			got = within(got)
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

// Unread counts an agent's unread messages (stale briefs excluded).
func (s *Store) Unread(ctx context.Context, agent string) (total, urgent int, err error) {
	total, urgent, _, _, err = s.UnreadByMode(ctx, agent)
	return
}

// UnreadByMode also counts how many unread messages are delivered in full,
// and how many of those were addressed to the agent by name or id.
func (s *Store) UnreadByMode(ctx context.Context, agent string) (total, urgent, full, direct int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(m.urgent),0), coalesce(sum(r.mode='full'),0), coalesce(sum(r.direct),0)
		FROM receipts r JOIN messages m ON m.id=r.message_id
		WHERE r.agent_id=? AND r.read_at=0`+unreadFilter, agent, s.briefCut()).Scan(&total, &urgent, &full, &direct)
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

// inThread reports whether agent wrote or received a message of thread.
func (s *Store) inThread(ctx context.Context, thread int64, agent string) bool {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM messages m LEFT JOIN receipts r ON r.message_id=m.id
		WHERE m.thread_id=? AND (m.sender_id=? OR r.agent_id=?)`, thread, agent, agent).Scan(&n)
	return err == nil && n > 0
}

// Thread returns every message in a thread, oldest first. Only participants
// (sender or recipient of some message in it) may read it.
func (s *Store) Thread(ctx context.Context, agent string, id int64) ([]Message, error) {
	var thread int64
	if err := s.db.QueryRowContext(ctx, `SELECT thread_id FROM messages WHERE id=?`, id).Scan(&thread); err != nil {
		return nil, fmt.Errorf("no message #%d", id)
	}
	if agent != "" && !s.inThread(ctx, thread, agent) {
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

// ThreadSummary is one conversation an agent took part in.
type ThreadSummary struct {
	ThreadID int64   `json:"thread_id"`
	Subject  string  `json:"subject,omitempty"`
	Count    int     `json:"count"`
	Last     Message `json:"last"`
}

// Threads lists the agent's most recent conversations (as sender or as a
// full-delivery recipient), newest first, so work can be picked up again
// after a restart or a switch of IDE, provider or account.
func (s *Store) Threads(ctx context.Context, agent string, limit int) ([]ThreadSummary, error) {
	if limit <= 0 || limit > 100 {
		limit = 15
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.thread_id, count(*), max(m.id) FROM messages m
		WHERE m.thread_id IN (
		  SELECT thread_id FROM messages WHERE sender_id=?
		  UNION SELECT m2.thread_id FROM receipts r JOIN messages m2 ON m2.id=r.message_id WHERE r.agent_id=? AND r.mode='full')
		GROUP BY m.thread_id ORDER BY max(m.id) DESC LIMIT ?`, agent, agent, limit)
	if err != nil {
		return nil, err
	}
	type row struct {
		thread, last int64
		n            int
	}
	var list []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.thread, &x.n, &x.last); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, x)
	}
	rows.Close()
	out := make([]ThreadSummary, 0, len(list))
	for _, x := range list {
		last, err := scanMsg(s.db.QueryRowContext(ctx, `SELECT `+msgCols+` FROM messages WHERE id=?`, x.last))
		if err != nil {
			continue
		}
		var subj string
		_ = s.db.QueryRowContext(ctx, `SELECT subject FROM messages WHERE id=?`, x.thread).Scan(&subj)
		out = append(out, ThreadSummary{ThreadID: x.thread, Subject: subj, Count: x.n, Last: last})
	}
	return out, nil
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
