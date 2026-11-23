package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/romanpert/ainterlinked/internal/hub"
)

// cmdHub runs the hub process (clients start it themselves when needed).
// `hub status` says whether one is running and what it knows of the caller.
func cmdHub(ctx context.Context, args []string) error {
	hub.Version = Version
	if len(args) > 0 && args[0] == "status" {
		r, err := hub.Dial(hub.SocketPath())
		if err != nil {
			fmt.Println("no hub process running: clients open the database themselves (identity is then claimed, not verified)")
			return nil
		}
		defer r.Close()
		var h hub.Hello
		if err := r.Call(ctx, "hello", nil, &h); err != nil {
			return err
		}
		fmt.Printf("hub process %s, pid %d\nyou: pid %d, under %s (pid %d)", h.Version, h.PID, h.Peer, orUnknown(h.Harness), h.Anchor)
		switch {
		case !h.Agent:
			fmt.Println(", the human operator")
		case h.Why != "":
			fmt.Println(", not the human operator:\n" + h.Why)
		default: // an older hub process
			fmt.Println(", an agent's harness")
		}
		return nil
	}
	err := hub.RunDaemon(ctx, loadConfig())
	if errors.Is(err, hub.ErrRunning) {
		fmt.Println("a hub process is already running")
		return nil
	}
	return err
}

// hubMode describes how this client reaches the hub, for doctor.
func hubMode(ctx context.Context, c hub.Client) string {
	r, ok := c.(*hub.Remote)
	if !ok {
		return "in-process: no hub process, so identity is claimed, not verified"
	}
	var h hub.Hello
	if err := r.Call(ctx, "hello", nil, &h); err != nil {
		return "hub process (no hello: " + err.Error() + ")"
	}
	return fmt.Sprintf("hub process %s (pid %d): identity from the kernel", h.Version, h.PID)
}

func orUnknown(s string) string {
	if s == "" {
		return "an unknown process"
	}
	return s
}
