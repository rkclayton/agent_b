package agent

import (
	"sort"
	"strings"
	"time"

	contextmgr "harness/internal/context"
	"harness/internal/events"
	"harness/internal/llm"
)

func fixedContextTokens(budget events.Budget) int {
	fixed := 0
	for _, name := range []string{"system", "project", "skills", "workspace_memory", "agent_memory", "tools"} {
		fixed += budget.Categories[name]
	}
	return fixed
}

func (x *runState) compactBeforeRequest() contextmgr.OneRuleResult {
	fixed := fixedContextTokens(x.budget)
	source := events.CompactionSummaryData{Role: "b", ConnectionID: x.connection.ID, Model: x.connection.Model, Outcome: "accepted", Dispatched: true, EstimatedPromptTokens: guardedPromptTokens(x.budget), Estimated: x.budget.Estimated, NCtx: x.connection.Context.NCtx, Trigger: "summary_pct"}
	window := x.budget.Ceiling
	if window <= 0 {
		window = x.budget.NCtx
	}
	input := contextmgr.OneRuleInput{
		Used: guardedPromptTokens(x.budget), Window: window, Fixed: fixed,
		SummaryPct: x.r.cfg().Context.SummaryPct, Pin: x.s.RunPin(),
		HandOffID: x.r.id("m"), Source: &source,
	}
	input.Summarize = func(records []events.Message, maxTokens int) (string, error) {
		x.recheckSummary = true
		messages := make([]llm.Message, 0, len(records))
		for _, record := range records {
			messages = append(messages, requestMessageAt(x.connection, x.s, record, false))
		}
		connection := summaryConnection(x.connection)
		started := time.Now()
		var response llm.Response
		var err error
		x.r.stage(x.s, x.runID, x.turn, "compact", func() {
			response, err = llm.New(&connection).Chat(x.ctx, llm.Request{Messages: messages, MaxTokens: maxTokens})
		})
		source.DurationMS = time.Since(started).Milliseconds()
		if err != nil {
			source.Outcome, source.Reason = "error", err.Error()
			return "", err
		}
		source.Usage = events.ModelUsage{PromptTokens: response.Usage.PromptTokens, CompletionTokens: response.Usage.CompletionTokens, CachedTokens: nullableInt(response.Usage.CachedTokens)}
		x.s.RecordCompactionModel(response.Usage.PromptTokens, response.Usage.CompletionTokens)
		return strings.TrimSpace(response.Content), nil
	}
	return x.r.compact.Compact(x.ctx, x.s, x.runID, input)
}

func (x *runState) trimAfterSummary() bool {
	window, fixed := x.budget.Ceiling, fixedContextTokens(x.budget)
	line := fixed + int(x.r.cfg().Context.SummaryPct*float64(max(0, window-fixed)))
	required := guardedPromptTokens(x.budget) - line + 1
	if window <= 0 || required <= 0 {
		return false
	}
	messages := x.s.MessagesCopy()
	candidates := make([]int, 0)
	for i, message := range messages {
		if message.Role == "tool" && !message.Elided && message.Tokens > 0 {
			candidates = append(candidates, i)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return messages[candidates[i]].Tokens > messages[candidates[j]].Tokens })
	ids := make([]string, 0, len(candidates))
	before, original, freed := 0, 0, 0
	for _, message := range messages {
		before += message.Tokens
	}
	for _, i := range candidates {
		message := &messages[i]
		stub := contextmgr.PruneStub(*message)
		tokens := estimatedTokenCount(stub)
		if tokens >= message.Tokens {
			continue
		}
		original += message.Tokens
		freed += message.Tokens - tokens
		message.Content, message.Tokens, message.Estimated, message.Elided = stub, tokens, true, true
		ids = append(ids, message.ID)
		if freed >= required {
			break
		}
	}
	if freed == 0 {
		return false
	}
	x.s.ReplaceMessages(messages)
	x.s.RecordCompaction(-freed)
	x.r.bus.Publish(events.New(events.Compaction, x.s.ID, x.runID, map[string]any{
		"kind": "prune", "trigger": "summary_pct", "before": before, "after": before - freed,
		"affected_ids": ids, "pruned_results": len(ids), "pruned_tokens": original,
	}))
	return true
}
