package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/romanpert/ainterlinked/internal/core"
)

// cmdBudget shows or sets the owner's cap on what one session may spend,
// in billed tokens (fresh input, cache writes and output; cache reads
// aside). Over it, an agent's tool calls are refused until the owner raises
// it: the agent stops and says so.
//
//	ainterlinked budget            show the cap
//	ainterlinked budget 2M         one session may spend 2M billed tokens
//	ainterlinked budget off        no cap
func cmdBudget(ctx context.Context, args []string) error {
	c, cfg, err := openHub()
	if err != nil {
		return err
	}
	defer c.Close()
	if len(args) == 0 {
		var b core.Budget
		if err := c.Call(ctx, "get_budget", struct{}{}, &b); err != nil {
			return err
		}
		if b.SessionTokens == 0 {
			fmt.Println("no cap: sessions spend what they need (ainterlinked budget <tokens> sets one, e.g. 5M)")
			return nil
		}
		fmt.Printf("one session may spend %s billed tokens (fresh input, cache writes, output)\n", core.FormatTokens(b.SessionTokens))
		return nil
	}
	n, err := parseTokens(args[0])
	if err != nil {
		return err
	}
	me, err := human(ctx, c, cfg)
	if err != nil {
		return err
	}
	var b core.Budget
	if err := c.Call(ctx, "budget", core.BudgetReq{By: me, SessionTokens: n}, &b); err != nil {
		return err
	}
	if n == 0 {
		fmt.Println("cap removed")
		return nil
	}
	fmt.Printf("one session may now spend %s billed tokens; over it, its tool calls are refused until you raise it\n", core.FormatTokens(n))
	return nil
}

// parseTokens reads "off", "0", "500000", "500k", "2M", "1.5M".
func parseTokens(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "off" || s == "none" {
		return 0, nil
	}
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "k"):
		mult, s = 1e3, strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "m"):
		mult, s = 1e6, strings.TrimSuffix(s, "m")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0, errors.New(`usage: ainterlinked budget [<tokens>|off]   (tokens like 500k or 2M)`)
	}
	return int64(f * mult), nil
}
