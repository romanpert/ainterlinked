package core

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The recipient's attention is its own property. A broadcast lands in the
// context of agents that never asked for it, so each sender has a budget of
// them that refills with time: enough to announce what everyone must know (a
// reload, a branch switch, a release), not enough to chat to the whole bus.
// Mail to a name and #topic posts (their subscribers chose them) are never
// limited; the human isn't either.
const (
	BroadcastBurst  = 6                // broadcasts a sender may send in a row
	BroadcastRefill = 10 * time.Minute // one more becomes available this often
)

// isBroadcast reports whether a target reaches agents that didn't choose to
// hear from the sender: everyone ("*"), a whole project, or a whole harness.
// A #topic reaches its subscribers, who opted in; "branch" reaches those
// sharing the sender's branch, its immediate collaborators. Neither counts.
// "branch:<other>" reaches a crowd the sender doesn't work with, so it does.
func isBroadcast(to string, sender Agent) bool {
	for _, part := range strings.Split(to, ",") {
		part = strings.TrimSpace(part)
		switch {
		case part == "*" || part == "all" || part == "everyone",
			part == "project" || part == "team",
			strings.HasPrefix(part, "project:"),
			strings.HasPrefix(part, "harness:"):
			return true
		case strings.HasPrefix(part, "branch:"):
			if b := strings.TrimSpace(strings.TrimPrefix(part, "branch:")); b != "" && b != sender.Branch {
				return true
			}
		}
	}
	return false
}

// spendBroadcast takes one broadcast from the sender's budget, inside the
// send's transaction, or refuses with when the next one is available.
//
// The bucket is kept as one time, full_at: when it would be full again.
// Each broadcast pushes it BroadcastRefill later (starting from now if it
// was already full); a broadcast is allowed while that stays within
// BroadcastBurst refills of now. That is a token bucket of BroadcastBurst
// tokens refilling one per BroadcastRefill, without a background refill.
func spendBroadcast(tx *sql.Tx, now int64, sender Agent) error {
	var fullAt int64
	err := tx.QueryRow(`SELECT full_at FROM broadcast_budget WHERE agent_id=?`, sender.ID).Scan(&fullAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	refill := BroadcastRefill.Milliseconds()
	next := max(fullAt, now) + refill
	if next-now > BroadcastBurst*refill {
		return broadcastRefused(now, next-BroadcastBurst*refill)
	}
	_, err = tx.Exec(`INSERT INTO broadcast_budget(agent_id,full_at) VALUES(?,?)
		ON CONFLICT(agent_id) DO UPDATE SET full_at=excluded.full_at`, sender.ID, next)
	return err
}

// broadcastRefused states the rule, when the sender may broadcast again and
// what to do meanwhile.
func broadcastRefused(now, again int64) error {
	wait := time.Duration(again-now) * time.Millisecond
	return fmt.Errorf("broadcast refused: the recipient's attention is its own, so each agent may send up to %d broadcasts in a row ('*', 'project', 'project:<name>', 'harness:<name>') and gets one more every %d minutes. You can broadcast again in %s (at %s). Meanwhile name the agents who need this (list_agents shows who works on what) or post to a #topic, whose subscribers chose to hear it",
		BroadcastBurst, int(BroadcastRefill.Minutes()), wait.Round(time.Second), time.UnixMilli(again).Format("15:04"))
}
