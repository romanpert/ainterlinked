package hooks

import (
	"context"
	"fmt"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/hub"
)

// overBudget says why the agent must stop once its session has spent the
// owner's cap; "" while under it, with no cap, or when the hub can't say
// (an older hub). The hub reads the spend from the harness's transcript,
// and this runs before every tool call: an agent can't act around it.
func overBudget(ctx context.Context, c hub.Client, me core.Agent) string {
	var sp core.Spend
	if err := c.Call(ctx, "spend", core.SpendReq{Agent: me.ID}, &sp); err != nil || !sp.Over {
		return ""
	}
	s := fmt.Sprintf("ainterlinked: this session has spent %s billed tokens, and the owner's cap for one session is %s",
		core.FormatTokens(sp.Billed()), core.FormatTokens(sp.Cap))
	if peers := sp.ForPeers.Billed(); peers > 0 {
		s += fmt.Sprintf(" (%s of it at peers' request)", core.FormatTokens(peers))
	}
	return s + ". Don't start anything else: tell the user where the work stands and what's left. Only the user can raise the cap (ainterlinked budget <tokens>, from their own terminal)."
}
