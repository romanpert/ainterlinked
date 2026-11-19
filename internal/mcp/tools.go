package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/romanpert/interlinked/internal/core"
	"github.com/romanpert/interlinked/internal/hub"
	"github.com/romanpert/interlinked/internal/render"
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

func toolDefs() []tool {
	return []tool{
		{"whoami", "Your identity on the interlinked bus, unread count and who else is online in your project.", obj(prop{}), ro()},
		{"list_agents", "List AI agents (any harness) online now: name, harness, project, branch, state, current activity.",
			obj(prop{"project": str("Only this project name"), "harness": str("Only this harness, e.g. codex"), "include_offline": boo("Include agents seen in the last 7 days")}), ro()},
		{"set_status", "Tell peers what you're doing. Call when you start/finish work or get blocked.",
			obj(prop{"state": prop{"type": "string", "enum": core.States}, "activity": str("Short description of the current work"),
				"role": str("Your role in the team, e.g. reviewer"), "name": str("Rename yourself (unique among online agents)")}), nil},
		{"send_message", "Message other agents. to: agent name, 'project' (your project's agents), 'project:<name>', 'harness:<name>', '*' (everyone), or a comma list. Omit to when replying.",
			obj(prop{"to": str("Recipient(s)"), "body": str("Message text (max 16 KB; reference files by path)"), "subject": str("Optional short subject"),
				"reply_to": num("Message id you're answering (keeps the thread)"), "urgent": boo("Ask recipients to look now")}, "body"), nil},
		{"read_inbox", "Read your unread messages (marks them read). all=true shows recent history instead.",
			obj(prop{"all": boo("Include already-read messages"), "limit": num("Max messages, default 20")}), nil},
		{"wait_for_messages", "Block until a message arrives or the timeout passes, then return it. Use to wait for a peer's answer or for work.",
			obj(prop{"timeout_seconds": num("Default 50, max 600")}), nil},
		{"get_thread", "Show a whole conversation thread by any message id in it.", obj(prop{"message_id": num("Message id")}, "message_id"), ro()},
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
		{"update_task", "Change a task: status (todo, doing, blocked, review, done, cancelled), result, title, body, priority or assignee ('me', a name, or '' to unassign). The creator is told when it's done or blocked.",
			obj(prop{"id": num("Task id"), "status": prop{"type": "string", "enum": core.TaskStatuses}, "result": str("Outcome / notes (when done or blocked)"),
				"title": str("New title"), "body": str("New body"), "priority": num("0-3"), "assignee": str("Agent name, 'me' or ''")}, "id"), nil},
		{"reserve_files", "Reserve files/folders before editing so other agents don't touch them (globs ok: src/api/**, *.md; trailing / = folder). Enforced on harnesses with edit hooks.",
			obj(prop{"paths": strs("Paths relative to the project root"), "reason": str("Why, e.g. task #3"), "ttl_minutes": num("Default 30")}, "paths"), nil},
		{"release_files", "Release your file reservations (all of them if paths is omitted).", obj(prop{"paths": strs("Patterns to release")}), nil},
		{"list_reservations", "Show active file reservations in your project.", obj(prop{"all_projects": boo("Every project")}), ro()},
		{"activity_feed", "Recent bus activity: joins, status changes, messages, tasks, reservations.",
			obj(prop{"limit": num("Default 30"), "project": str("Only this project")}), ro()},
	}
}

func (s *Server) ensure(ctx context.Context) (string, error) {
	if err := s.register(); err != nil {
		if errors.Is(err, errDisabled) {
			return "", fmt.Errorf("interlinked is disabled for %s (interlinked enable %s)", s.harness, s.harness)
		}
		return "", fmt.Errorf("interlinked: can't reach the hub (%v); run `interlinked doctor`", err)
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
		if r, ok := a[k]; ok {
			_ = json.Unmarshal(r, v)
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
		b.WriteString(render.Agents(peers, me))
		return b.String(), nil
	case "list_agents":
		var f core.AgentFilter
		get("project", &f.Project)
		get("harness", &f.Harness)
		get("include_offline", &f.IncludeOffline)
		var list []core.Agent
		if err := c.Call(ctx, "agents", f, &list); err != nil {
			return "", err
		}
		return render.Agents(list, me), nil
	case "set_status":
		r := core.StatusReq{ID: me}
		get("state", &r.State)
		get("activity", &r.Activity)
		get("role", &r.Role)
		get("name", &r.Name)
		var out core.Agent
		if err := c.Call(ctx, "status", r, &out); err != nil {
			return "", err
		}
		s.mu.Lock()
		s.me = out
		s.mu.Unlock()
		return fmt.Sprintf("ok: %s is %s%s", out.Name, out.State, prefixed(" — ", out.Activity)), nil
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
		return fmt.Sprintf("sent #%d to %s", m.ID, strings.Join(m.Delivered, ", ")), nil
	case "read_inbox":
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
		var res core.LockResult
		if err := c.Call(ctx, "reserve", r, &res); err != nil {
			return "", err
		}
		var b strings.Builder
		if len(res.Granted) > 0 {
			b.WriteString("reserved:\n" + render.Locks(res.Granted))
		}
		if len(res.Conflicts) > 0 {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString("NOT reserved — held by others (message them or pick other work):\n" + render.Locks(res.Conflicts))
		}
		return b.String(), nil
	case "release_files":
		p := hub.ReleaseParam{Agent: me}
		get("paths", &p.Paths)
		var r hub.ReleaseResult
		if err := c.Call(ctx, "release", p, &r); err != nil {
			return "", err
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
		return render.Locks(list), nil
	case "activity_feed":
		r := core.FeedReq{Limit: 30}
		get("limit", &r.Limit)
		get("project", &r.Project)
		var list []core.Event
		if err := c.Call(ctx, "feed", r, &list); err != nil {
			return "", err
		}
		return render.Feed(list), nil
	}
	return "", fmt.Errorf("unknown tool %q", name)
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
