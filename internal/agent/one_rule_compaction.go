package agent

import (
	"strings"
	"time"

	contextmgr "harness/internal/context"
	"harness/internal/events"
	"harness/internal/llm"
)

func (x *runState) compactBeforeRequest() contextmgr.OneRuleResult {
	fixed := 0
	for _, name := range []string{"system", "project", "skills", "workspace_memory", "agent_memory", "tools"} {
		fixed += x.budget.Categories[name]
	}
	source := events.CompactionSummaryData{Role: "b", ConnectionID: x.connection.ID, Model: x.connection.Model, Outcome: "accepted", Dispatched: true, EstimatedPromptTokens: guardedPromptTokens(x.budget), Estimated: x.budget.Estimated, NCtx: x.connection.Context.NCtx, Trigger: "summary_pct"}
	if x.refusalSentence != "" {
		source.Trigger = "server_refusal"
	}
	window := x.budget.Ceiling
	if window <= 0 {
		window = x.budget.NCtx
	}
	input := contextmgr.OneRuleInput{
		Used: guardedPromptTokens(x.budget), Window: window, Fixed: fixed,
		SummaryPct: x.r.cfg().Context.SummaryPct, Pin: x.s.RunPin(),
		HandOffID:       x.r.id("m"),
		RefusalSentence: x.refusalSentence, RefusalAttempt: x.refusalAttempt, Source: &source,
	}
	input.Summarize = func(records []events.Message, maxTokens int) (string, error) {
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
