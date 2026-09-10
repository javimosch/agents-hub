package main

import "github.com/javimosch/agents-hub/internal/out"

// spendCmd reports the day's cost against the allowance.
//
// The counter lives in bkn because spending is a fact shared by every machine
// on the account; the allowance lives in the profile because it is a decision
// made on one machine. Asking "what has today cost" should not require
// reading a log.
func spendCmd(fl *flags) {
	h, cfg := hubFor(fl)
	s, err := h.SpentToday()
	if err != nil {
		fail(err, "read today's spend")
	}
	fields := map[string]any{
		"day": s.Day, "tokens": s.Tokens, "turns": s.Turns,
		"budget": cfg.DailyTokenBudget,
	}
	if cfg.DailyTokenBudget > 0 {
		fields["remaining"] = cfg.DailyTokenBudget - s.Tokens
		fields["spent"] = s.Tokens >= cfg.DailyTokenBudget
	}
	out.Data(fields)
}
