package hub

import (
	"context"
	"fmt"
	"strings"

	"github.com/javimosch/agents-hub/internal/provider"
)

// DefaultContextLimit is assumed when nothing better is known. It is on the
// small side on purpose: compacting a thread that did not need it costs one
// summarisation, while not compacting one that did costs the whole turn.
const DefaultContextLimit = 32000

// DefaultCompactAt is the fraction of the window at which a thread is folded
// down. Compaction itself needs room to work -- the summariser has to be sent
// the conversation it is summarising -- so waiting for the window to be nearly
// full is waiting until it is too late.
const DefaultCompactAt = 0.6

// CompactPolicy is when and how hard to fold a thread down.
type CompactPolicy struct {
	// Limits maps a model name to its context window, learned from a
	// provider's refusal or configured by hand.
	Limits map[string]int
	At     float64
	// OnLearn is called when a provider names a window this policy did not
	// know. Learning it only in memory would mean rediscovering it -- at the
	// cost of a refused call -- on every future invocation.
	OnLearn func(model string, limit int)
	// Keep is how many recent messages survive compaction verbatim. The last
	// few turns carry the thread of the conversation in a way a summary
	// cannot, so they are never folded.
	Keep int
}

// LimitFor returns the window this policy believes a model has.
func (p CompactPolicy) LimitFor(model string) int {
	if n, ok := p.Limits[model]; ok && n > 0 {
		return n
	}
	return DefaultContextLimit
}

// Threshold is the token count at which a thread should be compacted.
func (p CompactPolicy) Threshold(model string) int {
	at := p.At
	if at <= 0 || at >= 1 {
		at = DefaultCompactAt
	}
	return int(float64(p.LimitFor(model)) * at)
}

// Due reports whether a thread's measured prompt size has crossed the line
// AND whether folding it again could plausibly help.
//
// The second half matters more than it looks. Once a thread is over the
// threshold it stays over it until a turn measures smaller, so without this
// every subsequent turn would pay for another summarisation -- forever, if
// the size is coming from a large system prompt or a recalled block rather
// than from the conversation. One compaction that did not shrink the prompt
// is enough to conclude that another will not either.
func (p CompactPolicy) Due(th *Thread, model string) bool {
	if th == nil || th.LastPrompt <= 0 || th.LastPrompt < p.Threshold(model) {
		return false
	}
	if th.CompactPrompt > 0 && th.LastPrompt >= th.CompactPrompt {
		return false
	}
	return true
}

// CompactReport says what a compaction did, so it is never invisible.
type CompactReport struct {
	Compacted  bool   `json:"compacted"`
	Reason     string `json:"reason,omitempty"`
	FoldedUpTo int    `json:"folded_up_to_seq,omitempty"`
	Messages   int    `json:"messages_folded,omitempty"`
	Tokens     int    `json:"tokens_spent,omitempty"`
	Was        int    `json:"prompt_tokens_before,omitempty"`
	// Partial says the summariser itself had to be given less than the whole
	// fold, so the note covers fewer messages than the seq range implies.
	Partial bool   `json:"partial,omitempty"`
	Error   string `json:"error,omitempty"`
}

// renderFold lays out what the summariser is asked to read.
func renderFold(previous string, fold []Message, summaryCap int) string {
	var sb strings.Builder
	if summaryCap > 0 && len(previous) > summaryCap {
		// Truncated rather than dropped: an earlier note that no longer fits
		// still says more than nothing, and the caller is told the fold was
		// partial so the loss is on the record.
		previous = previous[:summaryCap] + "\n[...earlier notes truncated]"
	}
	if previous != "" {
		// An earlier summary is folded in too, so a long-lived thread ends up
		// with one note rather than a chain of summaries of summaries.
		fmt.Fprintf(&sb, "Notes from earlier in this conversation:\n%s\n\n", previous)
	}
	for _, m := range fold {
		content := m.Content
		if m.Role == provider.RoleTool {
			content = "[tool result] " + content
		}
		fmt.Fprintf(&sb, "%s: %s\n\n", m.Role, strings.TrimSpace(content))
	}
	return sb.String()
}

const compactPrompt = `Summarise this conversation for your own future reference.

Keep: decisions made and why, facts established, identifiers, file paths,
open questions, and anything the next turn would otherwise have to ask again.
Drop: pleasantries, restated context, and anything already superseded.

Write it as notes, not prose, and stay under 400 words. You are writing for
yourself, so completeness beats readability.`

// Compact folds a thread's older messages into a summary.
//
// The messages are not deleted. The summary is stored on the thread with the
// seq it covers, and replay skips what it covers -- so the record of what was
// actually said survives for anyone who later needs to know why an agent did
// something.
func (h *Hub) Compact(ctx context.Context, p provider.Provider, th *Thread, a Agent, pol CompactPolicy, reason string) (*CompactReport, error) {
	rep := &CompactReport{Reason: reason, Was: th.LastPrompt}
	keep := pol.Keep
	if keep <= 0 {
		keep = 4
	}
	history, err := h.HistoryAfter(th.ID, th.SummarySeq, 0)
	if err != nil {
		return rep, err
	}
	if len(history) <= keep {
		// Nothing to fold: the thread is already as short as it gets, and the
		// prompt is large for some other reason -- a huge system prompt, a
		// recalled block, a tool result. Saying so beats pretending to act.
		rep.Error = "nothing to compact: the conversation is already short"
		return rep, nil
	}
	fold := history[:len(history)-keep]

	// The summariser is the same agent, with its tools and memory removed:
	// summarising is not a task that should be able to call anything.
	plain := a
	plain.System = compactPrompt

	// The conversation that no longer fits may not fit into a summarisation
	// request either -- which is exactly the case this whole mechanism exists
	// to rescue. So a refusal here halves what is being folded and tries
	// again, rather than giving up and handing the caller the original error.
	var turn *Turn
	// Both halve on each retry. Halving only the fold does not help when the
	// bulk is an earlier summary, which is exactly the case on a thread that
	// has been folded before.
	summaryCap := 4000
	for attempt := 0; ; attempt++ {
		turn, err = h.Chat(ctx, p, ChatOptions{
			Agent: plain, Message: renderFold(th.Summary, fold, summaryCap), NoStore: true,
		})
		if err == nil {
			break
		}
		if _, isCtx := provider.IsContextError(err); !isCtx || len(fold) < 2 || attempt >= 6 {
			rep.Error = err.Error()
			return rep, err
		}
		fold = fold[len(fold)/2:]
		summaryCap /= 2
		rep.Partial = true
	}
	summary := strings.TrimSpace(turn.Reply.Content)
	if summary == "" {
		rep.Error = "the summariser returned nothing; leaving the thread alone"
		return rep, nil
	}

	last := fold[len(fold)-1].Seq
	if last <= 0 {
		// A fold that ends at seq 0 cannot be recorded, because 0 is how the
		// thread says "nothing folded". Folding one message is not worth a
		// summarisation call anyway.
		rep.Error = "nothing worth folding yet"
		return rep, nil
	}
	if _, err := h.C.Patch(RefThreads, th.ID, map[string]any{
		"summary": summary, "summary_seq": last,
		"compacted_at_prompt": th.LastPrompt, "updated_at": now(),
	}); err != nil {
		return rep, err
	}
	th.Summary, th.SummarySeq, th.CompactPrompt = summary, last, th.LastPrompt
	rep.Compacted = true
	rep.FoldedUpTo = last
	rep.Messages = len(fold)
	rep.Tokens = turn.Usage.TotalTokens
	return rep, nil
}
