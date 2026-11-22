package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/hub"
	"github.com/romanpert/ainterlinked/internal/render"
)

type prop = map[string]any

func obj(props prop, required ...string) prop {
	o := prop{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}

func str(desc string) prop { return prop{"type": "string", "description": desc} }
func boo(desc string) prop { return prop{"type": "boolean", "description": desc} }
func num(desc string) prop { return prop{"type": "integer", "description": desc} }
func strs(desc string) prop {
	return prop{"type": "array", "items": prop{"type": "string"}, "description": desc}
}
func nums(desc string) prop {
	return prop{"type": "array", "items": prop{"type": "integer"}, "description": desc}
}

type tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema prop   `json:"inputSchema"`
	Annotations prop   `json:"annotations,omitempty"`
}

func ro() prop { return prop{"readOnlyHint": true} }

func toolDefs(l core.Limits) []tool {
	budget, lapse := "", ""
	if l.Broadcasts() {
		budget = fmt.Sprintf(" Broadcasts ('*', 'project', 'project:<name>', 'harness:<name>') come from a budget of %d that refills one every %d minutes; names, 'branch' and '#topic' are never limited.", l.BroadcastBurst, l.BroadcastRefillMinutes)
	}
	if l.IdleLapseMinutes > 0 {
		lapse = fmt.Sprintf(" Your reservations lapse %d min after your turn ends.", l.IdleLapseMinutes)
	}
	auto := ""
	if l.AutoReserveMinutes > 0 {
		auto = " With edit hooks you rarely need it: editing a file reserves it for you automatically (renewed by each edit)."
	}
	return []tool{
		{"whoami", "Your identity on the ainterlinked bus, unread count and who else is online in your project.", obj(prop{}), ro()},
		{"list_agents", "List AI agents (any harness) online now: name, harness, project, branch, state, current activity, and what each has done lately (facts, not a score).",
			obj(prop{"project": str("Only this project name"), "harness": str("Only this harness, e.g. codex"), "include_offline": boo("Include agents seen in the last 7 days")}), ro()},
		{"set_status", "Tell peers what you're doing. Call when you start/finish work or get blocked.",
			obj(prop{"state": prop{"type": "string", "enum": core.States}, "activity": str("Short description of the current work"),
				"role": str("Your role in the team, e.g. reviewer"), "name": str("Rename yourself, ideally after your task (old names keep reaching you)"),
				"continue_as":        str("Continue an offline agent of this checkout (after a restart or a switch of IDE/provider/account): take its name, unread mail, threads, tasks and reservations"),
				"share_load_seconds": num("Only if the user asks: publish this machine's CPU, memory and disk every N seconds while this session is open; 0 stops")}), nil},
		{"send_message", "Message other agents. to: a name (full delivery), 'branch' or 'branch:<name>', '#topic' (its subscribers), 'project', 'project:<name>', 'harness:<name>', '*', or a comma list. Group messages arrive as each recipient chose (one-line briefs by default): put the point in the subject/first line." + budget + " Omit to when replying.",
			obj(prop{"to": str("Recipient(s)"), "body": str("Message text (max 16 KB; reference files by path)"), "subject": str("Optional short subject (shown in briefs)"),
				"reply_to": num("The #number of the message you're answering (keeps the thread)"), "urgent": boo("Read first; it still follows each recipient's delivery choice (only the human's urgent mail overrides it)")}, "body"), nil},
		{"read_inbox", "Read your unread messages (marks them read). all=true shows recent history; threads=true lists your recent conversations to pick one up again.",
			obj(prop{"all": boo("Include already-read messages"), "limit": num("Max messages, default 20"), "threads": boo("List your recent threads instead")}), nil},
		{"set_delivery", "Tune what peer mail costs your context. No arguments shows your settings and the project's topics.",
			obj(prop{"broadcasts": prop{"type": "string", "enum": []string{core.ModeFull, core.ModeBrief, core.ModeOff}, "description": "Group messages: full, brief (one line, default) or off (only the human's urgent mail still arrives); group mail never holds your turn open"},
				"inject":       prop{"type": "string", "enum": []string{core.InjectEveryTool, core.InjectTurn, core.InjectManual}, "description": "When mail enters your context: after tool calls, only at prompt/turn end, or never (you call read_inbox)"},
				"max_messages": num("Messages per injection"), "max_chars": num("Characters per injection (500-40000)"),
				"inline_chars": num("Longer messages arrive as a gist and a pointer (200-40000)"),
				"subscribe":    strs("Topics to receive (send_message to='#topic'); 'name:brief' for one-line delivery"), "unsubscribe": strs("Topics to drop"),
				"reset": boo("Back to defaults (topics kept)")}), nil},
		{"wait_for_messages", "Block until a message arrives or the timeout passes, then return it. Use to wait for a peer's answer or for work.",
			obj(prop{"timeout_seconds": num("Default 50, max 600")}), nil},
		{"get_thread", "Show a whole conversation thread by any message id in it.", obj(prop{"message_id": num("Message id")}, "message_id"), ro()},
		{"handover", "What sessions that left this checkout knew. No arguments lists them, one line each; id reads one in full. note leaves your own for whoever continues your work (one is written from your transcript when you leave, so add a note only to say something it wouldn't).",
			obj(prop{"id": num("Handover to read"), "note": str("Your own handover: where the work stands, what's next, pointers to files/commits")}), nil},
		{"create_task", "Add a task to a shared board (default board: your project). Others can claim it.",
			obj(prop{"title": str("Task title"), "body": str("Details / acceptance criteria"), "board": str("Board name, default your project"),
				"priority": num("0 urgent, 1 high, 2 normal (default), 3 low"), "depends_on": nums("Task ids that must finish first"),
				"labels": strs("Labels, e.g. frontend"), "assignee": str("Agent name to assign directly")}, "title"), nil},
		{"list_tasks", "Show a task board.",
			obj(prop{"board": str("Board name; default your project; '*' for all"), "status": prop{"type": "string", "enum": core.TaskStatuses},
				"mine": boo("Only tasks assigned to you"), "open": boo("Hide done/cancelled")}), ro()},
		{"list_boards", "List task boards with counts per status.", obj(prop{}), ro()},
		{"claim_task", "Atomically take a todo task (no one else can take it). Without id, claims the most urgent ready task on the board.",
			obj(prop{"id": num("Task id"), "board": str("Board for auto-pick"), "label": str("Only auto-pick tasks with this label")}), nil},
		{"want_task", "Say you need a task someone else holds or that is waiting. One counter per agent; the holder sees the count, never a message per request. Use it instead of messaging the holder.",
			obj(prop{"id": num("Task id")}, "id"), nil},
		{"update_task", "Change a task: status (todo, doing, blocked, review, done, cancelled), result, title, body, priority or assignee ('me', a name, or '' to unassign). A task someone holds changes only by its holder, its creator or the human. The creator is told when it's done or blocked.",
			obj(prop{"id": num("Task id"), "status": prop{"type": "string", "enum": core.TaskStatuses}, "result": str("Outcome / notes (when done or blocked)"),
				"title": str("New title"), "body": str("New body"), "priority": num("0-3"), "assignee": str("Agent name, 'me' or ''")}, "id"), nil},
		{"reserve_files", "Claim files/folders ahead of editing them, e.g. for a refactor (globs ok: src/api/**, *.md; trailing / = folder), or queue for held ones with wait=true." + auto + lapse + " Keep it narrow; release when done.",
			obj(prop{"paths": strs("Paths relative to the project root"), "reason": str("Why (defaults to the task title)"), "ttl_minutes": num("Default 30"),
				"wait": boo("If held, queue: it becomes yours, and you're told, when the holder lets go"),
				"task": num("Board task this is for: claims it if free; the reservation ends when it's done")}, "paths"), nil},
		{"release_files", "Release your file reservations (all of them if paths is omitted) and your queue places for them; the next in line gets them. With to, hand them instead to an agent queued for them (any of them, not only the first): they keep what's left of your term.",
			obj(prop{"paths": strs("Patterns to release"), "to": str("Agent queued for them to hand them to")}), nil},
		{"list_reservations", "Show active file reservations and the queue in your project.", obj(prop{"all_projects": boo("Every project")}), ro()},
		{"activity_feed", "Recent bus activity: joins, status changes, messages, tasks, reservations.",
			obj(prop{"limit": num("Default 30"), "project": str("Only this project")}), ro()},
	}
}

func (s *Server) ensure(ctx context.Context) (string, error) {
	if err := s.register(); err != nil {
		if errors.Is(err, errDisabled) {
			return "", fmt.Errorf("ainterlinked is disabled for %s (ainterlinked enable %s)", s.harness, s.harness)
		}
		return "", fmt.Errorf("ainterlinked: can't reach the hub (%v); run `ainterlinked doctor`", err)
	}
	return s.agentID(), nil
}

func (s *Server) callTool(ctx context.Context, name string, raw json.RawMessage) (string, bool) {
	me, err := s.ensure(ctx)
	if err != nil {
		return err.Error(), true
	}
	text, err := s.dispatch(ctx, me, name, raw)
	if err != nil {
		return "error: " + err.Error(), true
	}
	return text, false
}

func (s *Server) dispatch(ctx context.Context, me, name string, raw json.RawMessage) (string, error) {
	var a map[string]json.RawMessage
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", fmt.Errorf("arguments must be an object")
		}
	}
	get := func(k string, v any) {
		r, ok := a[k]
		if !ok || json.Unmarshal(r, v) == nil {
			return
		}
		// A number sent as text ("#1037", "1037") still counts: models copy
		// ids as they see them.
		var s string
		if json.Unmarshal(r, &s) == nil {
			_ = json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(s), "#")), v)
		}
	}
	c := s.client
	switch name {
	case "whoami":
		var self core.Agent
		if err := c.Call(ctx, "agent", hub.IDParam{ID: me}, &self); err != nil {
			return "", err
		}
		var u hub.UnreadResult
		_ = c.Call(ctx, "unread", hub.AgentParam{Agent: me}, &u)
		var peers []core.Agent
		_ = c.Call(ctx, "agents", core.AgentFilter{}, &peers)
		var b strings.Builder
		fmt.Fprintf(&b, "You are %s (%s, id %s) in %s", self.Name, self.Harness, self.ID, self.Project)
		if self.Branch != "" {
			fmt.Fprintf(&b, "@%s", self.Branch)
		}
		fmt.Fprintf(&b, ". Delivery: %s. Unread: %d.\n", deliveryHelp(self.Delivery), u.Total)
		if len(self.Aka) > 0 {
			fmt.Fprintf(&b, "Also reached as: %s.\n", strings.Join(self.Aka, ", "))
		}
		var p core.Prefs
		_ = c.Call(ctx, "prefs", hub.AgentParam{Agent: me}, &p)
		b.WriteString("Your delivery: " + s.prefsLine(p) + " (set_delivery to change)\n")
		b.WriteString(render.Agents(peers, me))
		return b.String(), nil
	case "set_delivery":
		r := core.PrefsReq{Agent: me}
		get("broadcasts", &r.Broadcasts)
		get("inject", &r.Inject)
		get("max_messages", &r.MaxMessages)
		get("max_chars", &r.MaxChars)
		get("inline_chars", &r.InlineChars)
		get("subscribe", &r.Subscribe)
		get("unsubscribe", &r.Unsubscribe)
		get("reset", &r.Reset)
		var p core.Prefs
		changed := len(a) > 0
		if changed {
			if err := c.Call(ctx, "set_prefs", r, &p); err != nil {
				return "", err
			}
		} else if err := c.Call(ctx, "prefs", hub.AgentParam{Agent: me}, &p); err != nil {
			return "", err
		}
		var self core.Agent
		_ = c.Call(ctx, "agent", hub.IDParam{ID: me}, &self)
		var topics map[string]int
		_ = c.Call(ctx, "topics", hub.ProjectParam{ProjectPath: self.ProjectPath}, &topics)
		out := s.prefsLine(p)
		if changed {
			out = "ok: " + out
		}
		if len(topics) > 0 {
			out += "\nTopics in this project (subscribers): " + core.SortedTopics(topics)
		}
		return out, nil
	case "list_agents":
		var f core.AgentFilter
		get("project", &f.Project)
		get("harness", &f.Harness)
		get("include_offline", &f.IncludeOffline)
		var list []core.Agent
		if err := c.Call(ctx, "agents", f, &list); err != nil {
			return "", err
		}
		names := make([]string, 0, len(list))
		for _, a := range list {
			names = append(names, a.Name)
		}
		var facts map[string]core.Facts
		_ = c.Call(ctx, "facts", hub.FactsParam{Viewer: me, Names: names}, &facts) // an older hub has none: the list still stands
		return render.Agents(list, me) + render.FactsBlock(list, facts) + s.loadLine(ctx), nil
	case "set_status":
		r := core.StatusReq{ID: me}
		var share *float64
		get("share_load_seconds", &share)
		if share != nil && *share != 0 && (*share < 1 || *share > maxShareSeconds) {
			return "", errors.New("share_load_seconds is 1 or more (0 stops)")
		}
		get("state", &r.State)
		get("activity", &r.Activity)
		get("role", &r.Role)
		get("name", &r.Name)
		get("continue_as", &r.ContinueAs)
		var out core.Agent
		if err := c.Call(ctx, "status", r, &out); err != nil {
			return "", err
		}
		s.mu.Lock()
		s.me = out
		s.mu.Unlock()
		msg := fmt.Sprintf("ok: %s is %s%s", out.Name, out.State, prefixed(" — ", out.Activity))
		if r.ContinueAs != "" {
			var u hub.UnreadResult
			_ = c.Call(ctx, "unread", hub.AgentParam{Agent: me}, &u)
			var mine []core.Task
			_ = c.Call(ctx, "tasks", core.TaskFilter{Agent: me, Mine: true, Open: true}, &mine)
			msg += fmt.Sprintf("\nContinuing %s: %d unread message(s), %d open task(s) of yours. read_inbox threads=true lists your conversations.", r.ContinueAs, u.Total, len(mine))
			var took []core.Handover
			if c.Call(ctx, "handovers", core.HandoverReq{Agent: me, Taken: true}, &took) == nil && len(took) > 0 {
				msg += fmt.Sprintf("\nIts handover: #%d, ~%s tokens (handover id=%d reads it).", took[0].ID, core.FormatTokens(int64(took[0].Chars/4)), took[0].ID)
			}
			if len(mine) > 0 {
				msg += "\n" + render.Tasks(mine)
			}
		}
		if share != nil {
			line, err := s.shareLoad(ctx, time.Duration(*share*float64(time.Second)))
			if err != nil {
				return "", fmt.Errorf("%s, but the load isn't published: %w", msg, err)
			}
			msg += "\n" + line
		}
		return msg, nil
	case "send_message":
		r := core.SendReq{From: me}
		get("to", &r.To)
		get("body", &r.Body)
		get("subject", &r.Subject)
		get("reply_to", &r.ReplyTo)
		get("urgent", &r.Urgent)
		var m core.Message
		if err := c.Call(ctx, "send", r, &m); err != nil {
			return "", err
		}
		return sentLine(m), nil
	case "read_inbox":
		var threads bool
		get("threads", &threads)
		if threads {
			p := hub.ThreadsParam{Agent: me}
			get("limit", &p.Limit)
			var list []core.ThreadSummary
			if err := c.Call(ctx, "threads", p, &list); err != nil {
				return "", err
			}
			return render.Threads(list), nil
		}
		r := core.InboxReq{Agent: me}
		get("all", &r.All)
		get("limit", &r.Limit)
		var list []core.Message
		if err := c.Call(ctx, "inbox", r, &list); err != nil {
			return "", err
		}
		return render.PeerMessages(list, 0), nil
	case "wait_for_messages":
		t := 50
		get("timeout_seconds", &t)
		if t <= 0 || t > 600 {
			t = 50
		}
		_ = c.Call(ctx, "status", core.StatusReq{ID: me, State: "waiting", Quiet: true}, nil)
		var list []core.Message
		err := c.Call(ctx, "wait", hub.WaitParam{Agent: me, Timeout: t}, &list)
		_ = c.Call(context.Background(), "status", core.StatusReq{ID: me, State: "working", Quiet: true}, nil)
		if err != nil {
			return "", err
		}
		if len(list) == 0 {
			return fmt.Sprintf("No messages after %ds.", t), nil
		}
		return render.PeerMessages(list, 0), nil
	case "handover":
		var id int64
		var note string
		get("id", &id)
		get("note", &note)
		switch {
		case note != "":
			var h core.Handover
			if err := c.Call(ctx, "handover_note", core.HandoverNoteReq{Agent: me, Note: note}, &h); err != nil {
				return "", err
			}
			return "ok: your handover is left for whoever continues your work; a later note replaces it.", nil
		case id > 0:
			var h core.Handover
			if err := c.Call(ctx, "handover", core.HandoverReq{Agent: me, ID: id}, &h); err != nil {
				return "", err
			}
			return render.Handover(h), nil
		}
		var list []core.Handover
		if err := c.Call(ctx, "handovers", core.HandoverReq{Agent: me}, &list); err != nil {
			return "", err
		}
		if len(list) == 0 {
			return "No handovers left in this checkout by sessions that are gone.", nil
		}
		return render.Handovers(list), nil
	case "get_thread":
		var id int64
		get("message_id", &id)
		var list []core.Message
		if err := c.Call(ctx, "thread", hub.ThreadParam{Agent: me, ID: id}, &list); err != nil {
			return "", err
		}
		return render.PeerMessages(list, 0), nil
	case "create_task":
		r := core.TaskReq{By: me}
		get("title", &r.Title)
		get("body", &r.Body)
		get("board", &r.Board)
		if _, ok := a["priority"]; ok {
			var p int
			get("priority", &p)
			r.Priority = &p
		}
		get("depends_on", &r.DependsOn)
		get("labels", &r.Labels)
		get("assignee", &r.Assignee)
		var t core.Task
		if err := c.Call(ctx, "task_create", r, &t); err != nil {
			return "", err
		}
		return "created " + render.Task(t), nil
	case "list_tasks":
		f := core.TaskFilter{Agent: me}
		get("board", &f.Board)
		get("status", &f.Status)
		get("mine", &f.Mine)
		get("open", &f.Open)
		var list []core.Task
		if err := c.Call(ctx, "tasks", f, &list); err != nil {
			return "", err
		}
		return render.Tasks(list), nil
	case "list_boards":
		var list []core.Board
		if err := c.Call(ctx, "boards", hub.Empty{}, &list); err != nil {
			return "", err
		}
		return render.Boards(list), nil
	case "claim_task":
		var id int64
		get("id", &id)
		if id > 0 {
			var t core.Task
			if err := c.Call(ctx, "task_claim", hub.ClaimParam{Agent: me, ID: id}, &t); err != nil {
				return "", err
			}
			return "claimed " + render.Task(t), nil
		}
		p := hub.NextParam{Agent: me}
		get("board", &p.Board)
		get("label", &p.Label)
		var r hub.NextResult
		if err := c.Call(ctx, "task_next", p, &r); err != nil {
			return "", err
		}
		if !r.OK {
			return "Nothing ready to claim on that board.", nil
		}
		return "claimed " + render.Task(*r.Task), nil
	case "want_task":
		var id int64
		get("id", &id)
		var t core.Task
		if err := c.Call(ctx, "task_want", hub.ClaimParam{Agent: me, ID: id}, &t); err != nil {
			return "", err
		}
		return "noted: " + render.Task(t), nil
	case "update_task":
		u := core.TaskUpdate{By: me}
		get("id", &u.ID)
		get("status", &u.Status)
		get("result", &u.Result)
		get("title", &u.Title)
		if _, ok := a["body"]; ok {
			var b string
			get("body", &b)
			u.Body = &b
		}
		if _, ok := a["priority"]; ok {
			var p int
			get("priority", &p)
			u.Priority = &p
		}
		if _, ok := a["assignee"]; ok {
			var as string
			get("assignee", &as)
			u.Assignee = &as
		}
		var t core.Task
		if err := c.Call(ctx, "task_update", u, &t); err != nil {
			return "", err
		}
		return "updated " + render.Task(t), nil
	case "reserve_files":
		r := core.LockReq{Agent: me, TTL: s.cfg.Locks.TTLMinutes}
		get("paths", &r.Paths)
		get("reason", &r.Reason)
		get("ttl_minutes", &r.TTL)
		get("wait", &r.Wait)
		get("task", &r.Task)
		var res core.LockResult
		if err := c.Call(ctx, "reserve", r, &res); err != nil {
			return "", err
		}
		var b strings.Builder
		if res.Task != nil {
			b.WriteString("for " + render.Tasks([]core.Task{*res.Task}) + "\n")
		}
		if len(res.Granted) > 0 {
			b.WriteString("reserved:\n" + render.Locks(res.Granted))
		}
		switch {
		case len(res.Queued) > 0:
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString("held by others, so you're queued (you'll be told when it's yours; the holder was told you wait):\n" + render.Waiters(res.Queued))
		case len(res.Conflicts) > 0:
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString("NOT reserved — held by others (wait=true queues you; or pick other work):\n" + render.Locks(res.Conflicts))
		}
		return b.String(), nil
	case "release_files":
		p := hub.ReleaseParam{Agent: me}
		get("paths", &p.Paths)
		get("to", &p.To)
		var r hub.ReleaseResult
		if err := c.Call(ctx, "release", p, &r); err != nil {
			return "", err
		}
		if p.To != "" {
			return fmt.Sprintf("ceded %d reservation(s) to %s", r.Released, p.To), nil
		}
		return fmt.Sprintf("released %d reservation(s)", r.Released), nil
	case "list_reservations":
		var all bool
		get("all_projects", &all)
		p := hub.ProjectParam{}
		if !all {
			var self core.Agent
			if err := c.Call(ctx, "agent", hub.IDParam{ID: me}, &self); err != nil {
				return "", err
			}
			p.ProjectPath = self.ProjectPath
		}
		var list []core.Lock
		if err := c.Call(ctx, "locks", p, &list); err != nil {
			return "", err
		}
		out := render.Locks(list)
		var queue []core.Waiter
		if err := c.Call(ctx, "waiters", p, &queue); err == nil && len(queue) > 0 {
			out += "\n" + render.Waiters(queue)
		}
		return out, nil
	case "activity_feed":
		r := core.FeedReq{Limit: 30}
		get("limit", &r.Limit)
		get("project", &r.Project)
		var list []core.Event
		if err := c.Call(ctx, "feed", r, &list); err != nil {
			return "", err
		}
		return render.Feed(list) + s.loadLine(ctx), nil
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

// loadLine is this machine's load as its own line, or "" when no session publishes it.
func (s *Server) loadLine(ctx context.Context) string {
	var m core.Machine
	if s.client.Call(ctx, "machine", hub.Empty{}, &m) != nil {
		return ""
	}
	return prefixed("\n", render.Machine(m))
}

func (s *Server) prefsLine(p core.Prefs) string {
	inject := core.InjectEveryTool
	if !s.cfg.Delivery.OnTool {
		inject = core.InjectTurn
	}
	return render.Prefs(p, s.cfg.Delivery.MaxMessages, s.cfg.Delivery.MaxChars, s.cfg.Delivery.InlineChars, inject)
}

// sentLine tells the sender who got what, without listing twenty names.
func sentLine(m core.Message) string {
	names := func(list []string) string {
		if len(list) > 6 {
			return fmt.Sprintf("%s and %d more", strings.Join(list[:5], ", "), len(list)-5)
		}
		return strings.Join(list, ", ")
	}
	var parts []string
	if len(m.Delivered) > 0 {
		parts = append(parts, "in full to "+names(m.Delivered))
	}
	if len(m.Briefed) > 0 {
		parts = append(parts, fmt.Sprintf("as a one-line brief to %d agent(s) (%s)", len(m.Briefed), names(m.Briefed)))
	}
	line := fmt.Sprintf("sent #%d %s", m.ID, strings.Join(parts, "; "))
	if len(m.Away) > 0 {
		line += fmt.Sprintf(". Not active now: %s; they read it when their session resumes, so don't block on a reply (or pick a working agent).", names(m.Away))
	}
	return line
}

func prefixed(p, s string) string {
	if s == "" {
		return ""
	}
	return p + s
}

func deliveryHelp(d string) string {
	switch {
	case strings.Contains(d, "push"):
		return "push (messages arrive even while idle)"
	case strings.Contains(d, "turn"):
		return "turn (messages are injected on your next prompt/tool call/stop)"
	}
	return "pull (call read_inbox or wait_for_messages to receive)"
}
