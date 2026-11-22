package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/romanpert/ainterlinked/internal/redact"
	"github.com/romanpert/ainterlinked/internal/transcript"
)

// A handover passes on what a session knew to whoever carries its work on,
// by reference: the next session in the same checkout sees one line per
// handover (who, where, how big, where the work stood) and reads the body
// only if it wants it. Context is the reader's to spend.
//
// The hub writes a session's handover from its harness's transcript once it
// has left (Claude Code's last compaction summary, the user's requests
// since, its last report), so leaving costs the session nothing. A session
// can also leave its own note (the handover tool); its own words are never
// replaced by the automatic one.

// Handover is one session's hand-over of its work.
type Handover struct {
	ID          int64  `json:"id"`
	AgentID     string `json:"agent_id"`
	Name        string `json:"name"`
	Harness     string `json:"harness"`
	ProjectPath string `json:"project_path"`
	Branch      string `json:"branch,omitempty"`
	Source      string `json:"source"` // "auto" (from the transcript) or "agent" (its own note)
	Gist        string `json:"gist"`
	Body        string `json:"body,omitempty"` // only when one is read
	Chars       int    `json:"chars"`
	CreatedAt   int64  `json:"created_at"`
	TakenBy     string `json:"taken_by,omitempty"`
	machine     string
}

// HandoverReq reads one handover (ID > 0), or lists those left in the
// agent's checkout.
type HandoverReq struct {
	Agent string `json:"agent"`
	ID    int64  `json:"id,omitempty"`
	Taken bool   `json:"taken,omitempty"` // list the ones this agent took over instead
	Start bool   `json:"start,omitempty"` // the list for a session's start: as many as the user's handover_offers
}

// HandoverNoteReq leaves the agent's own handover for its session.
type HandoverNoteReq struct {
	Agent string `json:"agent"`
	Note  string `json:"note"`
}

const (
	sourceAuto  = "auto"
	sourceAgent = "agent"
	// maxNote bounds a handover an agent writes itself.
	maxNote = 16000
	// handoversListed bounds the handover tool's list.
	handoversListed = 10
	// handoverWindow is how far back handovers are offered (as long as
	// they are kept: Retention).
	handoverWindow = Retention
)

// LeaveHandover records the agent's own handover of its current session.
func (s *Store) LeaveHandover(ctx context.Context, r HandoverNoteReq) (Handover, error) {
	me, err := s.Get(ctx, r.Agent)
	if err != nil {
		return Handover{}, err
	}
	note := redact.Secrets(strings.TrimSpace(r.Note))
	if note == "" {
		return Handover{}, errors.New("an empty note hands nothing over")
	}
	if len(note) > maxNote {
		return Handover{}, fmt.Errorf("a note is at most %d characters; point to files for the rest", maxNote)
	}
	h := Handover{AgentID: me.ID, Name: me.Name, Harness: me.Harness, ProjectPath: me.ProjectPath, Branch: me.Branch,
		Source: sourceAgent, Gist: clip(strings.Join(strings.Fields(note), " "), 160), Body: note, machine: me.Machine}
	return h, s.saveHandover(ctx, h, me.SessionID)
}

// saveHandover stores h as the handover of that session, replacing the
// previous one, except that an automatic one never replaces an agent's own.
// Credential-looking text is hidden first: a handover is read by other
// sessions, possibly of other providers. The gist is one line of text.
func (s *Store) saveHandover(ctx context.Context, h Handover, session string) error {
	h.Body = redact.Secrets(h.Body)
	h.Gist = oneLine(redact.Secrets(h.Gist), 160)
	_, err := s.db.ExecContext(ctx, `INSERT INTO handovers(agent_id,session_id,name,harness,machine,project_path,branch,source,gist,body,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(agent_id,session_id) DO UPDATE SET name=excluded.name, branch=excluded.branch, source=excluded.source,
		  gist=excluded.gist, body=excluded.body, created_at=excluded.created_at, taken_by=''
		WHERE handovers.source<>'agent' OR excluded.source='agent'`,
		h.AgentID, session, h.Name, h.Harness, h.machine, h.ProjectPath, h.Branch, h.Source, h.Gist, h.Body, s.ms())
	return err
}

// WriteHandovers writes the handovers of up to n sessions that have left
// since their last one was written, from their transcripts, as far back as
// the user's handover_days (0: it writes none). Only the hub process calls
// it (it reads transcripts); it returns how many it wrote. A transcript is
// read only for the checkout the agent registered from (read checks it).
func (s *Store) WriteHandovers(ctx context.Context, n int, read func(harness, session, project string) (transcript.Handover, bool)) (int, error) {
	days := s.lim().HandoverDays
	if days == 0 {
		return 0, nil
	}
	since := s.now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,a.name,a.harness,a.machine,a.project_path,a.branch,a.session_id FROM agents a
		LEFT JOIN handovers h ON h.agent_id=a.id AND h.session_id=a.session_id
		WHERE a.online=0 AND a.session_id<>'' AND a.project_path<>'' AND a.harness<>'human' AND a.last_seen>=?
		  AND (h.id IS NULL OR (h.source='auto' AND h.created_at<a.last_seen))
		ORDER BY a.last_seen DESC LIMIT ?`, since, n)
	if err != nil {
		return 0, err
	}
	type left struct {
		h       Handover
		session string
	}
	var todo []left
	for rows.Next() {
		var l left
		if err := rows.Scan(&l.h.AgentID, &l.h.Name, &l.h.Harness, &l.h.machine, &l.h.ProjectPath, &l.h.Branch, &l.session); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, l)
	}
	rows.Close()
	wrote := 0
	for _, l := range todo {
		got, ok := read(l.h.Harness, l.session, l.h.ProjectPath)
		h := l.h
		h.Source = sourceAuto
		if ok {
			h.Gist, h.Body = got.Gist, got.Body
		}
		// Nothing to hand over is recorded too (empty), so it isn't read again.
		if err := s.saveHandover(ctx, h, l.session); err != nil {
			return wrote, err
		}
		if ok {
			wrote++
		}
	}
	return wrote, nil
}

// Handovers lists the handovers left in the agent's checkout on this
// machine by sessions that are gone and whose work nobody has taken over,
// the agent's branch first, newest first, without their bodies.
func (s *Store) Handovers(ctx context.Context, r HandoverReq) ([]Handover, error) {
	me, err := s.Get(ctx, r.Agent)
	if err != nil {
		return nil, err
	}
	limit := handoversListed
	if r.Start {
		limit = s.lim().HandoverOffers
	}
	if me.ProjectPath == "" || limit == 0 {
		return nil, nil
	}
	q, args := `SELECT `+handoverCols+` FROM handovers h LEFT JOIN agents a ON a.id=h.agent_id
		WHERE h.machine=? AND h.project_path=? AND h.body<>'' AND h.taken_by='' AND h.agent_id<>? AND coalesce(a.online,0)=0 AND h.created_at>=?
		ORDER BY h.branch=? DESC, h.created_at DESC LIMIT ?`,
		[]any{me.Machine, me.ProjectPath, me.ID, s.now().Add(-handoverWindow).UnixMilli(), me.Branch, limit}
	if r.Taken {
		q, args = `SELECT `+handoverCols+` FROM handovers h WHERE h.taken_by=? AND h.body<>'' ORDER BY h.created_at DESC LIMIT ?`, []any{me.ID, limit}
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Handover
	for rows.Next() {
		h, err := scanHandover(rows)
		if err != nil {
			return nil, err
		}
		h.Body = ""
		out = append(out, h)
	}
	return out, rows.Err()
}

// GetHandover reads one handover in full. Sessions in the same checkout on
// the same machine may read it, as may whoever took the work over.
func (s *Store) GetHandover(ctx context.Context, r HandoverReq) (Handover, error) {
	me, err := s.Get(ctx, r.Agent)
	if err != nil {
		return Handover{}, err
	}
	h, err := scanHandover(s.db.QueryRowContext(ctx, `SELECT `+handoverCols+` FROM handovers h WHERE h.id=? AND h.body<>''`, r.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return Handover{}, fmt.Errorf("no handover #%d", r.ID)
	}
	if err != nil {
		return Handover{}, err
	}
	if h.TakenBy != me.ID && h.AgentID != me.ID && (h.machine != me.Machine || h.ProjectPath != me.ProjectPath) {
		return Handover{}, fmt.Errorf("handover #%d was left in another checkout", r.ID)
	}
	return h, nil
}

// takeHandovers gives the handovers of an agent whose work is continued to
// its successor (continue_as): they stop being offered to others.
func takeHandovers(tx *sql.Tx, from, to string) error {
	_, err := tx.Exec(`UPDATE handovers SET taken_by=? WHERE agent_id=? AND taken_by=''`, to, from)
	return err
}

const handoverCols = `h.id,h.agent_id,h.name,h.harness,h.machine,h.project_path,h.branch,h.source,h.gist,h.body,length(h.body),h.created_at,h.taken_by`

func scanHandover(r interface{ Scan(...any) error }) (Handover, error) {
	var h Handover
	err := r.Scan(&h.ID, &h.AgentID, &h.Name, &h.Harness, &h.machine, &h.ProjectPath, &h.Branch, &h.Source, &h.Gist, &h.Body, &h.Chars, &h.CreatedAt, &h.TakenBy)
	return h, err
}
