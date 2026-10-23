package contextmgr

import (
	"context"
	"fmt"
	"strings"

	"harness/internal/events"
	"harness/internal/session"
)

const HandOffInstruction = "Write a compact hand-off using exactly these headings:\nDONE:\nNEXT:\nFILES CHANGED:\nOPEN QUESTIONS:"

type OneRuleInput struct {
	Used, Window, Fixed int
	SummaryPct          float64
	Pin                 string
	HandOffID           string
	RefusalSentence     string
	RefusalAttempt      int
	Summarize           func([]events.Message, int) (string, error)
	Source              *events.CompactionSummaryData
}

type OneRuleResult struct {
	Triggered, Changed                                  bool
	Retry                                               bool
	Stop                                                string
	BeforeChangeable, AfterChangeable                   int
	HandOffTokens, HandOffRequestTokens, CrossingTokens int
	KeptIDs                                             []string
}

// Compact is the sole decision point for changing a model request's context.
func (c *Compactor) Compact(ctx context.Context, s *session.Session, runID string, in OneRuleInput) OneRuleResult {
	result := OneRuleResult{BeforeChangeable: tokenSum(s.MessagesCopy()), CrossingTokens: in.Used}
	if in.RefusalAttempt >= 2 {
		result.Triggered, result.Stop = true, in.RefusalSentence
		return result
	}
	line := in.Fixed + int(in.SummaryPct*float64(max(0, in.Window-in.Fixed)))
	if in.RefusalSentence == "" && in.Used < line {
		return result
	}
	result.Triggered = true
	if in.Fixed >= in.Window {
		result.Stop = fmt.Sprintf("fixed prompt is %d tokens and does not fit the %d-token window", in.Fixed, in.Window)
		return result
	}
	messages := s.MessagesCopy()
	keep := oneRuleKeep(messages, in.Pin, max(0, in.Window-in.Fixed)/4)
	older, kept := splitOneRule(messages, keep)
	handoffID := in.HandOffID
	if handoffID == "" {
		handoffID = runID + "-handoff"
	}
	// Item 2we: older results that are at least PruneShare of the changeable
	// context are stubbed first; the summary runs only if that is not enough.
	prune := oneRulePrune{}
	if changeable := in.Used - in.Fixed; changeable > 0 && float64(tokenSum(prunable(older))) >= PruneShare*float64(changeable) {
		messages, prune = pruneOlder(messages, keep)
		older, kept = splitOneRule(messages, keep)
		if in.Used-prune.saved < line {
			note := events.Message{ID: handoffID, Role: "harness", Category: "summary", Content: fmt.Sprintf("%s%d results, %d tokens removed; nothing summarized", PrunedPrefix, len(prune.ids), prune.tokens)}
			note.Tokens, note.Estimated = estimateTokens(note.Content), true
			next := append([]events.Message{note}, messages...)
			result.HandOffTokens, result.KeptIDs = note.Tokens, messageIDs(messages)
			result.AfterChangeable = tokenSum(next)
			s.ReplaceMessages(next)
			s.RecordCompaction(result.AfterChangeable - result.BeforeChangeable)
			result.Changed, result.Retry = true, in.RefusalSentence != ""
			c.publishOneRule(s, runID, in, result, "", prune, prune.ids, note)
			return result
		}
	}
	request := append(append([]events.Message(nil), older...), events.Message{Role: "user", Content: HandOffInstruction})
	result.HandOffRequestTokens = tokenSum(request[:len(request)-1]) + estimateTokens(HandOffInstruction)
	maxSummary := max(500, int(.03*float64(max(0, in.Window-in.Fixed))))
	text, reason := oneRuleSummary(ctx, request, maxSummary, in.Summarize)
	handoff := events.Message{ID: handoffID, Role: "harness", Category: "summary", Content: text, Tokens: estimateTokens(text), Estimated: true}
	result.HandOffTokens = handoff.Tokens
	result.KeptIDs = messageIDs(kept)
	result.AfterChangeable = handoff.Tokens + tokenSum(kept)
	s.ReplaceMessages(append([]events.Message{handoff}, kept...))
	s.RecordCompaction(result.AfterChangeable - result.BeforeChangeable)
	result.Changed, result.Retry = true, in.RefusalSentence != ""
	if result.AfterChangeable > in.Window-in.Fixed {
		priorAfter := result.AfterChangeable
		kept = newestOneRule(messages, in.Pin)
		result.AfterChangeable = handoff.Tokens + tokenSum(kept)
		result.KeptIDs = messageIDs(kept)
		s.ReplaceMessages(append([]events.Message{handoff}, kept...))
		s.RecordCompactionDelta(result.AfterChangeable - priorAfter)
		result.Stop = oneRuleTooBig(kept, in.Fixed, in.Window, result.AfterChangeable)
	}
	removed, _ := splitOneRule(messages, indexSet(result.KeptIDs, messages))
	c.publishOneRule(s, runID, in, result, reason, prune, messageIDs(removed), handoff)
	return result
}

// PruneShare is the share of the changeable context older results must reach
// before they are stubbed at the line (item 2we); it is not a setting.
const PruneShare = .5

// PrunedPrefix opens the harness line a prune-only crossing leaves in context.
const PrunedPrefix = "Compacted: "

type oneRulePrune struct {
	ids           []string
	tokens, saved int
}

func prunable(messages []events.Message) []events.Message {
	out := []events.Message{}
	for _, message := range messages {
		if message.Role == "tool" && !message.Elided && (message.Category == "files" || message.Category == "results" || message.Category == "fetched") {
			out = append(out, message)
		}
	}
	return out
}

// PruneStub is the one line an older result becomes in model context.
func PruneStub(message events.Message) string {
	name := message.Name
	if name == "" {
		name = "tool"
	}
	return fmt.Sprintf("[%s result removed at compaction, %d tokens; recall reads it]", name, message.Tokens)
}

func pruneOlder(messages []events.Message, keep map[int]bool) ([]events.Message, oneRulePrune) {
	out, prune := append([]events.Message(nil), messages...), oneRulePrune{}
	for index, message := range out {
		if keep[index] || len(prunable([]events.Message{message})) == 0 {
			continue
		}
		stub := PruneStub(message)
		prune.ids, prune.tokens = append(prune.ids, message.ID), prune.tokens+message.Tokens
		out[index].Content, out[index].Tokens, out[index].Estimated, out[index].Elided = stub, estimateTokens(stub), true, true
		prune.saved += message.Tokens - out[index].Tokens
	}
	return out, prune
}

func oneRuleKeep(messages []events.Message, pin string, allowance int) map[int]bool {
	keep := map[int]bool{}
	start := pinIndex(messages, pin)
	for i := start; i < len(messages); i++ {
		if messages[i].Role == "user" {
			keep[i] = true
		}
	}
	type span struct{ begin, end int }
	work := []span{}
	for index := start; index < len(messages); {
		if messages[index].Role == "user" {
			index++
			continue
		}
		begin := index
		index++
		for index < len(messages) && messages[index].Role != "user" && messages[index].Role != "assistant" {
			index++
		}
		work = append(work, span{begin, index})
	}
	spent := 0
	for index := len(work) - 1; index >= 0; index-- {
		part := work[index]
		cost := tokenSum(messages[part.begin:part.end])
		if index == len(work)-1 || spent+cost <= allowance {
			for i := part.begin; i < part.end; i++ {
				keep[i] = true
			}
			spent += cost
		}
	}
	return keep
}

func splitOneRule(messages []events.Message, keep map[int]bool) (older, kept []events.Message) {
	for i, message := range messages {
		if keep[i] {
			kept = append(kept, message)
		} else {
			older = append(older, message)
		}
	}
	return
}

func newestOneRule(messages []events.Message, pin string) []events.Message {
	keep := oneRuleKeep(messages, pin, 0)
	_, kept := splitOneRule(messages, keep)
	return kept
}

func oneRuleSummary(ctx context.Context, request []events.Message, limit int, summarize func([]events.Message, int) (string, error)) (string, string) {
	if summarize == nil {
		return fallbackHandOff("error"), "error"
	}
	text, err := summarize(request, limit)
	if err != nil {
		reason := "error"
		if strings.Contains(strings.ToLower(err.Error()), "refus") {
			reason = "refusal"
		}
		return fallbackHandOff(reason), reason
	}
	if strings.TrimSpace(text) == "" {
		return fallbackHandOff("empty"), "empty"
	}
	return "Fresh-context hand-off:\n" + strings.TrimSpace(text), ""
}

func fallbackHandOff(reason string) string {
	return "Earlier work was put aside without a summary because of " + reason + "; the record can be searched with recall."
}

func oneRuleTooBig(kept []events.Message, fixed, window, used int) string {
	room := window - fixed
	for _, message := range kept {
		if message.Role == "user" && message.Tokens >= room {
			return fmt.Sprintf("task message is %d tokens and does not fit the %d-token window", message.Tokens, window)
		}
	}
	for i := len(kept) - 1; i >= 0; i-- {
		if kept[i].Role == "tool" && kept[i].Tokens >= room {
			return fmt.Sprintf("newest result is %d tokens and does not fit the %d-token window", kept[i].Tokens, window)
		}
	}
	return fmt.Sprintf("newest work is %d tokens and does not fit the %d-token window", used, window)
}

func estimateTokens(text string) int { return (len([]rune(text))*10 + 35) / 36 }
func messageIDs(messages []events.Message) []string {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		if message.ID != "" {
			ids = append(ids, message.ID)
		}
	}
	return ids
}

func indexSet(ids []string, messages []events.Message) map[int]bool {
	wanted, out := map[string]bool{}, map[int]bool{}
	for _, id := range ids {
		wanted[id] = true
	}
	for index, message := range messages {
		out[index] = wanted[message.ID]
	}
	return out
}

func (c *Compactor) publishOneRule(s *session.Session, runID string, in OneRuleInput, result OneRuleResult, reason string, prune oneRulePrune, affected []string, handoff events.Message) {
	if c.bus == nil {
		return
	}
	kind := "fresh"
	if len(prune.ids) > 0 {
		kind = "prune+fresh"
		if strings.HasPrefix(handoff.Content, PrunedPrefix) {
			kind = "prune"
		}
	}
	source := events.CompactionSummaryData{Outcome: "accepted", Trigger: "threshold", EstimatedPromptTokens: result.BeforeChangeable, Estimated: true}
	if in.Source != nil {
		source = *in.Source
	}
	if reason != "" {
		source.Outcome, source.Reason, source.FallbackReason = reason, reason, reason
	}
	if kind != "prune" {
		c.bus.Publish(events.New(events.CompactionSummary, s.ID, runID, source))
	}
	c.bus.Publish(events.New(events.Compaction, s.ID, runID, map[string]any{"kind": kind, "pruned_results": len(prune.ids), "pruned_tokens": prune.tokens, "trigger": source.Trigger, "before": result.BeforeChangeable, "after": result.AfterChangeable, "affected_ids": affected, "summary_message_id": handoff.ID, "role": source.Role, "connection_id": source.ConnectionID, "model": source.Model, "fallback_reason": source.FallbackReason, "usage": source.Usage}))
	c.bus.Publish(events.New(events.MessageAppended, s.ID, runID, map[string]any{"message": handoff}))
}
