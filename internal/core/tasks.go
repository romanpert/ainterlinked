package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Task is an item on a shared board.
type Task struct {
	ID          int64    `json:"id"`
	Board       string   `json:"board"`
	Title       string   `json:"title"`
	Body        string   `json:"body,omitempty"`
	Status      string   `json:"status"`
	Priority    int      `json:"priority"`
	AssigneeID  string   `json:"assignee_id,omitempty"`
	Assignee    string   `json:"assignee,omitempty"`
	CreatedBy   string   `json:"created_by,omitempty"`
	CreatedByID string   `json:"created_by_id,omitempty"`
	DependsOn   []int64  `json:"depends_on,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	Result      string   `json:"result,omitempty"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
	Blocked     bool     `json:"blocked_by_deps,omitempty"`
	// Wanted is how many other agents said they need this task (WantTask).
	Wanted int `json:"wanted,omitempty"`
	// Terms are the conditions the holder accepted by claiming it.
	Terms string `json:"terms,omitempty"`
	// ArchivedAt is when the task left the board (archive.go); 0 on a board.
	ArchivedAt int64 `json:"archived_at,omitempty"`
	// Snippet is the text around a search match (search.go).
	Snippet string `json:"snippet,omitempty"`
	// Untouched is how many whole days an open task has gone without an
	// update (stale.go); 0 for a closed one.
	Untouched int `json:"untouched_days,omitempty"`
}

// Tasks are property. A held task (it has an assignee) belongs to its
// holder; its creator and the human keep a say over it. Anyone else may only
// claim a free task, or say they need one with WantTask: a deduplicated
// counter on the task, never a stream of messages that would push the holder
// to rush the work or fill its context.
//
// A holder offline for longer than AbandonAfter is no longer using the task:
// it goes back to todo, with its body and result kept, and the holder is told
// when it comes back. Claiming states these terms up front. The user can
// change them (Limits).
const AbandonAfter = 30 * time.Minute

func (l Limits) claimTerms() string {
	terms := "yours while you use it: only you, its creator or the human change it"
	if l.AbandonTaskMinutes > 0 {
		terms += fmt.Sprintf("; if you are offline for over %s it goes back to todo (notes kept)", minutes(l.AbandonTaskMinutes))
	}
	return terms
}

func isHuman(a Agent) bool { return a.Harness == "human" }

// mayChange reports whether by may change t beyond claiming it.
func mayChange(t Task, by Agent) bool {
	switch {
	case isHuman(by), t.AssigneeID == by.ID, t.CreatedByID == by.ID:
		return true
	case t.AssigneeID != "":
		return false
	}
	return t.CreatedByID == "" // a free task with no known creator is anyone's to edit
}

// ownerOf names who decides about t, for error messages.
func ownerOf(t Task) string {
	if t.Assignee != "" {
		return t.Assignee
	}
	return t.CreatedBy
}

// isClaim reports whether u, on the free task t, only takes it for by.
func isClaim(u TaskUpdate, t Task, assignee *Agent, by Agent) bool {
	if t.AssigneeID != "" || t.Status != "todo" || u.Title != "" || u.Body != nil || u.Priority != nil || u.Result != "" {
		return false
	}
	toMe := assignee != nil && assignee.ID == by.ID
	return (u.Status == "doing" && (assignee == nil || toMe)) || (u.Status == "" && toMe)
}

// TaskStatuses in board order.
var TaskStatuses = []string{"todo", "doing", "blocked", "review", "done", "cancelled"}

const taskCols = `id,board,title,body,status,priority,assignee_id,assignee,created_by,created_by_id,depends_on,labels,result,created_at,updated_at`

// scanTask reads taskCols, then any extra columns into extra.
func scanTask(r scanner, extra ...any) (Task, error) {
	var t Task
	var deps, labels string
	err := r.Scan(append([]any{&t.ID, &t.Board, &t.Title, &t.Body, &t.Status, &t.Priority, &t.AssigneeID, &t.Assignee,
		&t.CreatedBy, &t.CreatedByID, &deps, &labels, &t.Result, &t.CreatedAt, &t.UpdatedAt}, extra...)...)
	t.DependsOn = parseIDs(deps)
	t.Labels = splitList(labels)
	return t, err
}

func parseIDs(s string) []int64 {
	var out []int64
	for _, p := range splitList(s) {
		if n, err := strconv.ParseInt(strings.TrimPrefix(p, "#"), 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func joinIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func validTaskStatus(st string) bool {
	for _, v := range TaskStatuses {
		if v == st {
			return true
		}
	}
	return false
}

// TaskReq creates a task.
type TaskReq struct {
	By        string   `json:"by"`
	Board     string   `json:"board,omitempty"`
	Title     string   `json:"title"`
	Body      string   `json:"body,omitempty"`
	Priority  *int     `json:"priority,omitempty"` // 0 urgent … 3 low; default 2
	DependsOn []int64  `json:"depends_on,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	Assignee  string   `json:"assignee,omitempty"`
}

func (s *Store) boardFor(a Agent, board string) string {
	if b := strings.TrimSpace(board); b != "" {
		return strings.ToLower(oneLine(b, 60))
	}
	if a.Project != "" {
		return strings.ToLower(oneLine(a.Project, 60))
	}
	return "default"
}

// CreateTask adds a task to a board (default: the creator's project).
func (s *Store) CreateTask(ctx context.Context, r TaskReq) (Task, error) {
	if strings.TrimSpace(r.Title) == "" {
		return Task{}, errors.New("title is required")
	}
	if err := checkLabels(r.Labels); err != nil {
		return Task{}, err
	}
	by, err := s.Get(ctx, r.By)
	if err != nil {
		return Task{}, err
	}
	prio := 2
	if r.Priority != nil {
		prio = clampPrio(*r.Priority)
	}
	var assignee Agent
	if r.Assignee != "" {
		if assignee, err = s.Resolve(ctx, r.Assignee); err != nil {
			return Task{}, err
		}
	}
	board := s.boardFor(by, r.Board)
	var out Task
	err = s.tx(ctx, func(tx *sql.Tx) error {
		for _, d := range r.DependsOn {
			var n int // an archived task is a finished one: depending on it is fine
			if err := tx.QueryRow(`SELECT (SELECT count(*) FROM tasks WHERE id=?)+(SELECT count(*) FROM archived_tasks WHERE id=?)`, d, d).Scan(&n); err != nil || n == 0 {
				return fmt.Errorf("depends_on: no task #%d", d)
			}
		}
		now := s.ms()
		res, err := tx.Exec(`INSERT INTO tasks(board,title,body,status,priority,assignee_id,assignee,created_by,created_by_id,depends_on,labels,created_at,updated_at)
			VALUES(?,?,?,'todo',?,?,?,?,?,?,?,?,?)`, board, oneLine(r.Title, 200), r.Body, prio, assignee.ID, assignee.Name, by.Name, by.ID,
			joinIDs(r.DependsOn), strings.Join(cleanLabels(r.Labels), ","), now, now)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		event(tx, now, &by, "task", fmt.Sprintf("%s created #%d on %s: %s", by.Name, id, board, r.Title))
		out, err = scanTask(tx.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id=?`, id))
		return err
	})
	return out, err
}

func clampPrio(p int) int {
	if p < 0 {
		return 0
	}
	if p > 3 {
		return 3
	}
	return p
}

// TaskFilter narrows ListTasks.
type TaskFilter struct {
	Agent    string `json:"agent,omitempty"` // caller, for Mine and the default board
	Board    string `json:"board,omitempty"` // "" = caller's project board, "*" = all boards
	Status   string `json:"status,omitempty"`
	Assignee string `json:"assignee,omitempty"`
	Mine     bool   `json:"mine,omitempty"`
	Open     bool   `json:"open,omitempty"` // exclude done/cancelled
	// Query searches tasks on boards and archived ones (search.go); Limit
	// caps the hits (default 20).
	Query string `json:"query,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// taskScope is f's board, status and assignee conditions, to append after
// WHERE: the columns are the same in tasks and archived_tasks.
func (s *Store) taskScope(ctx context.Context, f TaskFilter) (string, []any) {
	var q string
	var args []any
	var me Agent
	if f.Agent != "" {
		me, _ = s.Get(ctx, f.Agent)
	}
	board := f.Board
	if board == "" && me.ID != "" {
		board = s.boardFor(me, "")
	}
	if board != "" && board != "*" {
		q += ` AND board=?`
		args = append(args, strings.ToLower(board))
	}
	if f.Status != "" {
		q += ` AND status=?`
		args = append(args, f.Status)
	}
	if f.Open {
		q += ` AND status NOT IN ('done','cancelled')`
	}
	if f.Mine && me.ID != "" {
		q += ` AND assignee_id=?`
		args = append(args, me.ID)
	} else if f.Assignee != "" {
		q += ` AND lower(assignee)=lower(?)`
		args = append(args, strings.TrimPrefix(f.Assignee, "@"))
	}
	return q, args
}

// ListTasks returns tasks ordered by status, priority and age.
func (s *Store) ListTasks(ctx context.Context, f TaskFilter) ([]Task, error) {
	if err := s.tx(ctx, s.abandon); err != nil {
		return nil, err
	}
	scope, args := s.taskScope(ctx, f)
	q := `SELECT ` + taskCols + ` FROM tasks WHERE 1=1` + scope +
		` ORDER BY CASE status WHEN 'doing' THEN 0 WHEN 'blocked' THEN 1 WHEN 'review' THEN 2 WHEN 'todo' THEN 3 ELSE 4 END, priority, id LIMIT 500`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	rows.Close()
	return out, s.decorate(ctx, out)
}

// decorate fills in, on tasks still on a board, whether they wait on
// unfinished dependencies and how many agents want them.
func (s *Store) decorate(ctx context.Context, out []Task) error {
	wanted := map[int64]int{}
	wr, err := s.db.QueryContext(ctx, `SELECT task_id, count(*) FROM task_wants GROUP BY task_id`)
	if err != nil {
		return err
	}
	for wr.Next() {
		var id int64
		var n int
		if err := wr.Scan(&id, &n); err != nil {
			wr.Close()
			return err
		}
		wanted[id] = n
	}
	if err := wr.Err(); err != nil {
		wr.Close()
		return err
	}
	wr.Close()
	for i := range out {
		if out[i].ArchivedAt > 0 {
			continue
		}
		out[i].Blocked = s.depsPending(ctx, nil, out[i].DependsOn)
		out[i].Wanted = wanted[out[i].ID]
		out[i].Untouched = s.untouchedDays(out[i])
	}
	return nil
}

// depsPending reports whether a dependency is still open. One that is no
// longer on a board was archived, which only closed tasks are: finished.
func (s *Store) depsPending(ctx context.Context, tx *sql.Tx, deps []int64) bool {
	for _, d := range deps {
		var st string
		var err error
		if tx != nil {
			err = tx.QueryRow(`SELECT status FROM tasks WHERE id=?`, d).Scan(&st)
		} else {
			err = s.db.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id=?`, d).Scan(&st)
		}
		if err == nil && st != "done" && st != "cancelled" {
			return true
		}
	}
	return false
}

// TaskUpdate changes a task. Nil/empty fields are left unchanged.
type TaskUpdate struct {
	ID       int64   `json:"id"`
	By       string  `json:"by"`
	Status   string  `json:"status,omitempty"`
	Title    string  `json:"title,omitempty"`
	Body     *string `json:"body,omitempty"`
	Priority *int    `json:"priority,omitempty"`
	Assignee *string `json:"assignee,omitempty"` // "" unassigns, "me" assigns the caller
	Result   string  `json:"result,omitempty"`
}

// UpdateTask applies changes and, when a task is finished by someone other
// than its creator, tells the creator.
func (s *Store) UpdateTask(ctx context.Context, u TaskUpdate) (Task, error) {
	if u.Status != "" && !validTaskStatus(u.Status) {
		return Task{}, fmt.Errorf("status must be one of %s", strings.Join(TaskStatuses, ", "))
	}
	by, err := s.Get(ctx, u.By)
	if err != nil {
		return Task{}, err
	}
	var assignee *Agent
	if u.Assignee != nil {
		a := Agent{}
		switch strings.TrimSpace(*u.Assignee) {
		case "":
		case "me":
			a = by
		default:
			if a, err = s.Resolve(ctx, *u.Assignee); err != nil {
				return Task{}, err
			}
		}
		assignee = &a
	}
	cur, err := scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id=?`, u.ID))
	if err != nil {
		return Task{}, noTask(s.db, u.ID)
	}
	if !mayChange(cur, by) {
		if isClaim(u, cur, assignee, by) {
			return s.Claim(ctx, by.ID, u.ID)
		}
		return Task{}, fmt.Errorf("task #%d belongs to %s: only its holder, its creator or the human can change it. If you need it done, want_task records that (one counter the holder sees, no messages)", u.ID, ownerOf(cur))
	}
	if err := settleRule(cur, by, u.Status, assignee != nil); err != nil {
		return Task{}, err
	}
	var before, out Task
	err = s.tx(ctx, func(tx *sql.Tx) error {
		t, err := scanTask(tx.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id=?`, u.ID))
		if err != nil {
			return noTask(tx, u.ID)
		}
		if t.AssigneeID != cur.AssigneeID && !mayChange(t, by) {
			return fmt.Errorf("task #%d changed hands meanwhile; it is now %s's", u.ID, t.Assignee)
		}
		before = t
		if u.Status != "" {
			t.Status = u.Status
		}
		if u.Title != "" {
			t.Title = oneLine(u.Title, 200)
		}
		if u.Body != nil {
			t.Body = *u.Body
		}
		if u.Priority != nil {
			t.Priority = clampPrio(*u.Priority)
		}
		if assignee != nil {
			t.AssigneeID, t.Assignee = assignee.ID, assignee.Name
		}
		if u.Result != "" {
			t.Result = u.Result
		}
		if t.Status == "doing" && t.AssigneeID == "" {
			t.AssigneeID, t.Assignee = by.ID, by.Name
		}
		now := s.ms()
		_, err = tx.Exec(`UPDATE tasks SET status=?, title=?, body=?, priority=?, assignee_id=?, assignee=?, result=?, updated_at=? WHERE id=?`,
			t.Status, t.Title, t.Body, t.Priority, t.AssigneeID, t.Assignee, t.Result, now, t.ID)
		if err != nil {
			return err
		}
		msg := fmt.Sprintf("%s updated #%d", by.Name, t.ID)
		if t.Status != before.Status {
			msg = fmt.Sprintf("%s moved #%d %s → %s", by.Name, t.ID, before.Status, t.Status)
		}
		if (t.Status == "done" || t.Status == "cancelled") && t.Status != before.Status {
			// A finished task lets go of the files its assignee (or whoever
			// finished it) reserved for it; others' reservations are theirs.
			res, err := tx.Exec(`DELETE FROM locks WHERE task_id=? AND agent_id IN (?,?,?)`, t.ID, before.AssigneeID, t.AssigneeID, by.ID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				msg += fmt.Sprintf(" (released %d reservation(s))", n)
			}
			if _, err := tx.Exec(`DELETE FROM waiters WHERE task_id=?`, t.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM task_wants WHERE task_id=?`, t.ID); err != nil {
				return err
			}
			if err := s.promote(tx); err != nil {
				return err
			}
		}
		event(tx, now, &by, "task", msg+": "+t.Title)
		out, err = scanTask(tx.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id=?`, t.ID))
		return err
	})
	if err != nil {
		return out, err
	}
	// Tell the creator — by id, since default names get reused by later sessions.
	if (out.Status == "done" || out.Status == "blocked") && before.Status != out.Status && out.CreatedByID != "" && out.CreatedByID != by.ID {
		if creator, err := s.Get(ctx, out.CreatedByID); err == nil {
			body := fmt.Sprintf("Task #%d %q is now %s.", out.ID, out.Title, out.Status)
			if out.Result != "" {
				body += "\nResult: " + out.Result
			}
			_, _ = s.Send(ctx, SendReq{From: by.ID, To: creator.ID, Subject: fmt.Sprintf("task #%d %s", out.ID, out.Status), Body: body})
		}
	}
	return out, nil
}

// Claim atomically assigns a todo task to an agent. It fails if someone else
// holds it or its dependencies aren't finished.
func (s *Store) Claim(ctx context.Context, agent string, id int64) (Task, error) {
	me, err := s.Get(ctx, agent)
	if err != nil {
		return Task{}, err
	}
	var out Task
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if err := s.abandon(tx); err != nil {
			return err
		}
		t, err := scanTask(tx.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id=?`, id))
		if err != nil {
			return noTask(tx, id)
		}
		mine := t.AssigneeID == me.ID
		switch {
		case t.Status == "doing" && mine: // re-claiming your own task is a no-op
		case t.Status != "todo" && t.Assignee != "":
			return fmt.Errorf("task #%d is %s (%s)", id, t.Status, t.Assignee)
		case t.Status != "todo":
			return fmt.Errorf("task #%d is %s, not todo", id, t.Status)
		case t.AssigneeID != "" && !mine:
			return fmt.Errorf("task #%d is assigned to %s", id, t.Assignee)
		}
		if s.depsPending(ctx, tx, t.DependsOn) {
			return fmt.Errorf("task #%d waits on unfinished dependencies %v", id, t.DependsOn)
		}
		now := s.ms()
		res, err := tx.Exec(`UPDATE tasks SET status='doing', assignee_id=?, assignee=?, updated_at=? WHERE id=? AND status IN ('todo','doing')`,
			me.ID, me.Name, now, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("task #%d was taken concurrently", id)
		}
		_, _ = tx.Exec(`UPDATE agents SET state='working', activity=?, last_seen=? WHERE id=?`, clip(fmt.Sprintf("#%d %s", id, t.Title), 200), now, me.ID)
		event(tx, now, &me, "task", fmt.Sprintf("%s claimed #%d: %s", me.Name, id, t.Title))
		out, err = scanTask(tx.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id=?`, id))
		return err
	})
	if err == nil {
		out.Terms = s.lim().claimTerms()
	}
	return out, err
}

// wantStep reports whether n is a count at which the holder hears how many
// want its task: 1, 3, 9, 27… so even 100 000 requests make about ten notices.
func wantStep(n int) bool {
	for s := 1; s <= n; s *= 3 {
		if s == n {
			return true
		}
	}
	return false
}

// WantTask records that agent needs task id done. Each agent counts once per
// task; the holder sees the count on the task and gets a one-line notice only
// at geometric steps, never one message per request.
func (s *Store) WantTask(ctx context.Context, agent string, id int64) (Task, error) {
	me, err := s.Get(ctx, agent)
	if err != nil {
		return Task{}, err
	}
	var out Task
	err = s.tx(ctx, func(tx *sql.Tx) error {
		t, err := scanTask(tx.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id=?`, id))
		if err != nil {
			return noTask(tx, id)
		}
		switch {
		case t.Status == "done" || t.Status == "cancelled":
			return fmt.Errorf("task #%d is already %s", id, t.Status)
		case t.AssigneeID == me.ID:
			return fmt.Errorf("task #%d is yours", id)
		}
		now := s.ms()
		res, err := tx.Exec(`INSERT OR IGNORE INTO task_wants(task_id,agent_id,created_at) VALUES(?,?,?)`, id, me.ID, now)
		if err != nil {
			return err
		}
		added, _ := res.RowsAffected()
		if err := tx.QueryRow(`SELECT count(*) FROM task_wants WHERE task_id=?`, id).Scan(&t.Wanted); err != nil {
			return err
		}
		if added > 0 && t.AssigneeID != "" && wantStep(t.Wanted) {
			notice(tx, now, Agent{ID: t.AssigneeID, Name: t.Assignee}, fmt.Sprintf("#%d is wanted", id),
				fmt.Sprintf("%d agent(s) are waiting on #%d %q. Nothing to answer: doing it well is the reply.", t.Wanted, id, t.Title))
		}
		out = t
		return nil
	})
	return out, err
}

// abandon returns to todo the tasks in progress whose holder has been offline
// for over AbandonAfter (or is gone), keeping their body and result, and
// tells the holder for when it comes back.
func (s *Store) abandon(tx *sql.Tx) error {
	if err := s.reap(tx); err != nil { // offline means what it means everywhere else
		return err
	}
	after := minutes(s.lim().AbandonTaskMinutes)
	if after == 0 {
		return nil
	}
	now := s.ms()
	rows, err := tx.Query(`SELECT t.id, t.title, t.assignee_id, t.assignee FROM tasks t LEFT JOIN agents a ON a.id=t.assignee_id
		WHERE t.status='doing' AND t.assignee_id<>'' AND (a.id IS NULL OR (a.online=0 AND a.last_seen<?))`, now-after.Milliseconds())
	if err != nil {
		return err
	}
	type held struct {
		id                int64
		title, aid, aname string
	}
	var list []held
	for rows.Next() {
		var h held
		if err := rows.Scan(&h.id, &h.title, &h.aid, &h.aname); err != nil {
			rows.Close()
			return err
		}
		list = append(list, h)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, h := range list {
		if _, err := tx.Exec(`UPDATE tasks SET status='todo', assignee_id='', assignee='', updated_at=? WHERE id=? AND status='doing'`, now, h.id); err != nil {
			return err
		}
		who := Agent{ID: h.aid, Name: h.aname}
		notice(tx, now, who, fmt.Sprintf("#%d back to todo", h.id),
			fmt.Sprintf("Task #%d %q went back to todo: you were offline for over %s. Its notes and result are kept; claim it again if it is still free.", h.id, h.title, after))
		event(tx, now, &who, "task", fmt.Sprintf("#%d back to todo: %s offline for over %s", h.id, h.aname, after))
	}
	return nil
}

// taskForReservation checks that me may reserve files for task id, claiming
// it when it is still free (claimed reports that this call claimed it).
func (s *Store) taskForReservation(ctx context.Context, me Agent, id int64) (t Task, claimed bool, err error) {
	t, err = scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id=?`, id))
	if err != nil {
		return Task{}, false, noTask(s.db, id)
	}
	switch {
	case t.Status == "done" || t.Status == "cancelled":
		return Task{}, false, fmt.Errorf("task #%d is %s", id, t.Status)
	case t.Status == "todo":
		t, err = s.Claim(ctx, me.ID, id)
		return t, err == nil, err
	case t.AssigneeID != "" && t.AssigneeID != me.ID:
		return Task{}, false, fmt.Errorf("task #%d is %s by %s; ask them, or queue for the files with wait=true", id, t.Status, t.Assignee)
	}
	return t, false, nil
}

// ClaimNext claims the most urgent ready task on a board (or returns
// ok=false when there's nothing to do).
func (s *Store) ClaimNext(ctx context.Context, agent, board, label string) (Task, bool, error) {
	tasks, err := s.ListTasks(ctx, TaskFilter{Agent: agent, Board: board, Status: "todo"})
	if err != nil {
		return Task{}, false, err
	}
	for _, t := range tasks {
		if t.Blocked || (t.AssigneeID != "" && t.AssigneeID != agent) {
			continue
		}
		if label != "" && !contains(t.Labels, label) {
			continue
		}
		got, err := s.Claim(ctx, agent, t.ID)
		if err == nil {
			return got, true, nil
		}
	}
	return Task{}, false, nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

// Board summarises one board.
type Board struct {
	Name   string         `json:"name"`
	Counts map[string]int `json:"counts"`
	Total  int            `json:"total"`
	// Stale counts the open tasks untouched for StaleDays or more, when the
	// user set [tasks] stale_after_days (stale.go).
	Stale     int `json:"stale,omitempty"`
	StaleDays int `json:"stale_days,omitempty"`
}

// Boards lists boards with task counts per status.
func (s *Store) Boards(ctx context.Context) ([]Board, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT board,status,count(*) FROM tasks GROUP BY board,status ORDER BY board`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	idx := map[string]int{}
	var out []Board
	for rows.Next() {
		var b, st string
		var n int
		if err := rows.Scan(&b, &st, &n); err != nil {
			return nil, err
		}
		i, ok := idx[b]
		if !ok {
			i = len(out)
			idx[b] = i
			out = append(out, Board{Name: b, Counts: map[string]int{}})
		}
		out[i].Counts[st] = n
		out[i].Total += n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close() // the hub has one connection: free it for the next query
	return out, s.countStale(ctx, out)
}
