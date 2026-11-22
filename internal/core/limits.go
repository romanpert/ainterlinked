package core

import (
	"fmt"
	"time"
)

// Limits are the bus's terms, the user's to change ([limits] in config.toml).
// The starting values exist because a reservation or a queue place with no
// end blocks everyone else; 0 turns a limit off.
type Limits struct {
	BroadcastBurst         int `toml:"broadcast_burst"`          // broadcasts an agent may send in a row
	BroadcastRefillMinutes int `toml:"broadcast_refill_minutes"` // one more becomes available this often
	AutoReserveMinutes     int `toml:"auto_reserve_minutes"`     // editing a file reserves it this long; 0: editing reserves nothing
	IdleLapseMinutes       int `toml:"idle_lapse_minutes"`       // reservations lapse this long after the holder's turn ended
	FolderUseMinutes       int `toml:"folder_use_minutes"`       // a folder or glob lapses this long without edits in it
	ProjectUseMinutes      int `toml:"project_use_minutes"`      // the same, for the whole project
	AbandonTaskMinutes     int `toml:"abandon_task_minutes"`     // a task goes back to todo when its holder is offline this long
	QueueMinutes           int `toml:"queue_minutes"`            // a place in a queue lasts this long; 0: while the agent is online
	IdleGrantMinutes       int `toml:"idle_grant_minutes"`       // a queued reservation granted to an idle agent lasts at most this
	BriefHours             int `toml:"brief_hours"`              // unread one-line briefs are shown this long
	MaxHolds               int `toml:"max_holds"`                // times peer mail may continue a session between two user prompts
}

// DefaultLimits are the terms of a user who set none.
func DefaultLimits() Limits {
	return Limits{
		BroadcastBurst:         BroadcastBurst,
		BroadcastRefillMinutes: int(BroadcastRefill.Minutes()),
		AutoReserveMinutes:     int(AutoLease.Minutes()),
		IdleLapseMinutes:       int(IdleLapse.Minutes()),
		FolderUseMinutes:       int(FolderUse.Minutes()),
		ProjectUseMinutes:      int(ProjectUse.Minutes()),
		AbandonTaskMinutes:     int(AbandonAfter.Minutes()),
		QueueMinutes:           int(WaitTTL.Minutes()),
		IdleGrantMinutes:       int(idleGrantTTL.Minutes()),
		BriefHours:             int(BriefTTL.Hours()),
		MaxHolds:               6,
	}
}

// maxLimit keeps any of them, in minutes or hours, far from overflowing.
const maxLimit = 1_000_000

// Check refuses values that mean nothing.
func (l Limits) Check() error {
	for _, f := range []struct {
		name string
		v    int
	}{
		{"broadcast_burst", l.BroadcastBurst}, {"broadcast_refill_minutes", l.BroadcastRefillMinutes},
		{"auto_reserve_minutes", l.AutoReserveMinutes}, {"idle_lapse_minutes", l.IdleLapseMinutes},
		{"folder_use_minutes", l.FolderUseMinutes}, {"project_use_minutes", l.ProjectUseMinutes},
		{"abandon_task_minutes", l.AbandonTaskMinutes}, {"queue_minutes", l.QueueMinutes},
		{"idle_grant_minutes", l.IdleGrantMinutes}, {"brief_hours", l.BriefHours}, {"max_holds", l.MaxHolds},
	} {
		if f.v < 0 || f.v > maxLimit {
			return fmt.Errorf("limits.%s = %d: use 0 (no limit) to %d", f.name, f.v, maxLimit)
		}
	}
	return nil
}

// Broadcasts reports whether broadcasts are limited.
func (l Limits) Broadcasts() bool { return l.BroadcastBurst > 0 && l.BroadcastRefillMinutes > 0 }

// HeldOut reports whether n automatic continuations use up what the user allows.
func (l Limits) HeldOut(n int) bool { return l.MaxHolds > 0 && n >= l.MaxHolds }

func minutes(n int) time.Duration { return time.Duration(n) * time.Minute }

// SetLimits applies the user's terms from now on (the hub does when
// config.toml changes).
func (s *Store) SetLimits(l Limits) { s.limits.Store(&l) }

func (s *Store) lim() Limits { return *s.limits.Load() }

// briefCut is the time before which an unread brief is stale news.
func (s *Store) briefCut() int64 {
	h := s.lim().BriefHours
	if h == 0 {
		return 0
	}
	return s.ms() - (time.Duration(h) * time.Hour).Milliseconds()
}
