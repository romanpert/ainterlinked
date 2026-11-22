package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/romanpert/ainterlinked/internal/core"
)

// Nobody acts on behalf of another agent (see core/keys.go). A method in
// actors acts as the agent named by that param, and the hub runs it only
// with that agent's key. A Local remembers the keys of the agents it
// registered and attaches them itself, so callers just register and use
// their own id; a call naming an agent this process never registered as is
// refused. Another transport would send the key as params.key.
var actors = map[string]string{
	"status": "id", "leave": "id", "depart": "id",
	"send": "from", "inbox": "agent", "unread": "agent", "wait": "agent",
	"thread": "agent", "threads": "agent", "prefs": "agent", "set_prefs": "agent",
	"task_create": "by", "task_update": "by", "task_claim": "agent", "task_want": "agent", "task_next": "agent",
	"trespass": "writer", "turn": "agent", "spend": "agent", "budget": "by",
	"reserve": "agent", "release": "agent", "check_file": "agent", "touch_file": "agent",
	"note": "agent", "machine_sample": "agent",
}

// reads need no key: they name nobody's identity, or (register) they are
// how a session gets its key.
var reads = map[string]bool{
	"register": true, "agents": true, "agent": true, "resolve": true, "topics": true,
	"waiters": true, "messages": true, "tasks": true, "facts": true, "boards": true,
	"locks": true, "feed": true, "machine": true, "get_budget": true,
}

var errHuman = errors.New(`harness "human" is the operator's and only the ainterlinked CLI or dashboard registers it; register as your own harness`)

// actorOf returns the agent a method acts as, if it acts. The handler
// decodes params into a struct, and encoding/json matches field names in
// any case, so a "FROM" key would reach the handler's From while the check
// read "from". Params must name the actor exactly as the field is spelled
// and only once; anything else is refused rather than guessed.
func actorOf(method string, raw json.RawMessage) (id string, acts bool, err error) {
	field, ok := actors[method]
	if !ok {
		return "", false, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", true, fmt.Errorf("%s: params must be a JSON object: %w", method, err)
	}
	for k := range m {
		if k != field && strings.EqualFold(k, field) {
			return "", true, fmt.Errorf("%s: the acting agent goes in %q, spelled exactly; %q is refused", method, field, k)
		}
	}
	if v, ok := m[field]; ok {
		if err := json.Unmarshal(v, &id); err != nil {
			return "", true, fmt.Errorf("%s: %q must be an agent id string", method, field)
		}
	}
	return id, true, nil
}

func keyParam(raw json.RawMessage) string {
	var p struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(raw, &p)
	return p.Key
}

func (l *Local) remember(a core.Agent) {
	if a.ID == "" || a.Key == "" {
		return
	}
	l.mu.Lock()
	l.keys[a.ID] = a.Key
	l.mu.Unlock()
}

func (l *Local) key(id string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.keys[id]
}

// Human registers (or refreshes) the operator's agent. It is not a hub
// method: only code holding this machine's hub (the CLI, the dashboard)
// can be the human. Honest limit: so can any process of the same OS user
// that runs the CLI.
func (l *Local) Human(ctx context.Context, machine string) (core.Agent, error) {
	a, err := l.Store.Register(ctx, core.RegisterReq{
		Harness: "human", Name: "human", Machine: machine, SessionID: "human@" + machine, Via: "cli", Delivery: "pull",
	})
	if err == nil {
		l.remember(a)
	}
	return a, err
}

// Human returns the operator's agent through c, which must be this
// machine's hub.
func Human(ctx context.Context, c Client, machine string) (core.Agent, error) {
	h, ok := c.(interface {
		Human(context.Context, string) (core.Agent, error)
	})
	if !ok {
		return core.Agent{}, errors.New("the operator acts only through this machine's hub (the ainterlinked CLI or dashboard)")
	}
	return h.Human(ctx, machine)
}
