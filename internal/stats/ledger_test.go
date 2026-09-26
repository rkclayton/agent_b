package stats

import (
	"path/filepath"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
)

func TestLedgerAggregatesAtRunEndAndSurvivesSessionDeletion(t *testing.T) {
	root := t.TempDir()
	bus := events.NewBus()
	writers, err := events.NewWriters(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	defer writers.Close()
	cfg := config.Defaults(root)
	connection := cfg.Connections[0]
	connection.BaseURL, connection.Model, connection.Context.NCtx = "http://127.0.0.1:1", "model", 32768
	connection.Capabilities.Streaming, connection.Capabilities.ToolCalls, connection.Capabilities.OverflowBehavior = true, true, "error"
	cfg.Connections[0] = connection
	registry := session.NewRegistry(bus, writers, func(string) (*config.Connection, bool) { return &connection, true }, 40, func() config.Config { return cfg })
	manager := New(root, registry, bus)
	item, err := registry.Create("main", cfg.DefaultAgentID(), root)
	if err != nil {
		t.Fatal(err)
	}
	runID := "r1"
	bus.Publish(events.New(events.RunStarted, item.ID, runID, map[string]any{"run_id": runID}))
	bus.Publish(events.New(events.ModelRequest, item.ID, runID, map[string]any{"turn": 1}))
	bus.Publish(events.New(events.ModelResponse, item.ID, runID, map[string]any{"content": "done", "duration_ms": 12, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 20, "cached_tokens": 40}}))
	bus.Publish(events.New(events.DelegatedUsage, item.ID, runID, map[string]any{"usage": map[string]any{"prompt_tokens": 30, "completion_tokens": 7, "cached_tokens": 5}, "tool_calls": []any{map[string]any{"name": "read_file"}}}))
	bus.Publish(events.New(events.ToolResult, item.ID, runID, map[string]any{"name": "read_file", "ok": false}))
	bus.Publish(events.New(events.ApprovalRequired, item.ID, runID, map[string]any{"call_id": "c1"}))
	bus.Publish(events.New(events.RunStopped, item.ID, runID, map[string]any{"reason": "done"}))
	deadline := time.Now().Add(time.Second)
	var ledger Ledger
	for time.Now().Before(deadline) {
		ledger = manager.Snapshot(cfg.DefaultAgentID())
		if ledger.Agent.Runs == 1 && ledger.Agent.PromptTokens == 100 && ledger.Agent.Reliability.Completed == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if ledger.Version != Version || ledger.Agent.Chats != 1 || ledger.Agent.Runs != 1 || ledger.Agent.Turns != 1 || ledger.Agent.Tools["read_file"].Failures != 1 || ledger.Agent.Reliability.Completed != 1 || ledger.Agent.Reliability.Interventions != 1 {
		t.Fatalf("ledger=%+v", ledger)
	}
	if ledger.Agent.PromptTokens != 130 || ledger.Agent.CompletionTokens != 27 || ledger.Agent.CachedTokens != 45 || ledger.Agent.DelegatedPromptTokens != 30 || ledger.Agent.DelegatedToolCalls != 1 || len(ledger.Connections["local"].ModelResponseMS) != 1 {
		t.Fatalf("tokens/connection=%+v", ledger)
	}
	before := manager.Snapshot(cfg.DefaultAgentID())
	if err := registry.Close(item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Delete(item.ID); err != nil {
		t.Fatal(err)
	}
	after := manager.Snapshot(cfg.DefaultAgentID())
	if before.Agent.Runs != after.Agent.Runs || before.Agent.PromptTokens != after.Agent.PromptTokens {
		t.Fatalf("delete altered ledger before=%+v after=%+v", before, after)
	}
}

func TestReliabilityAttributionCountsKnownFailuresWithoutCallingStopsFailures(t *testing.T) {
	counters := Counters{Tools: map[string]Tool{}}
	add(&counters, events.New(events.ApprovalDecided, "main", "r1", map[string]any{"decision": "session"}), nil)
	for _, reason := range []string{"model_error", "length", "tool_errors", "connection_not_runnable", "context_ceiling", "turn_ceiling", "safe", "emergency", "user_stop"} {
		add(&counters, events.New(events.RunStopped, "main", "r1", map[string]any{"reason": reason}), nil)
	}
	if counters.ApprovalsApproved != 1 {
		t.Fatalf("approved=%d", counters.ApprovalsApproved)
	}
	got := counters.Reliability
	if got.ModelFailures != 2 || got.HarnessFailures != 3 || got.BriefFailures != 1 {
		t.Fatalf("attribution=%+v", got)
	}
}

// Item 2ji (c): the per-connection row's figures come from the run.stopped event
// the tally enriched, through the wiring the binary itself installs. If this
// drifts from the chat line, the two surfaces disagree about the same run, which
// is the thing the item exists to prevent.
func TestActivityRowCarriesTheRunsOwnFigures2ji(t *testing.T) {
	root := t.TempDir()
	bus := events.NewBus()
	writers, err := events.NewWriters(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	defer writers.Close()
	cfg := config.Defaults(root)
	connection := cfg.Connections[0]
	connection.BaseURL, connection.Model, connection.Context.NCtx = "http://127.0.0.1:1", "model", 32768
	connection.Capabilities.Streaming, connection.Capabilities.ToolCalls, connection.Capabilities.OverflowBehavior = true, true, "error"
	cfg.Connections[0] = connection
	registry := session.NewRegistry(bus, writers, func(string) (*config.Connection, bool) { return &connection, true }, 40, func() config.Config { return cfg })
	// The same wiring main() installs, not a copy of it.
	InstallRunTally(bus)
	manager := New(root, registry, bus)
	item, err := registry.Create("main", cfg.DefaultAgentID(), root)
	if err != nil {
		t.Fatal(err)
	}

	at := func(seconds int) string { return time.Date(2026, 9, 26, 12, 0, seconds, 0, time.UTC).Format(time.RFC3339) }
	emit := func(kind, runID, ts string, data map[string]any) {
		event := events.New(kind, item.ID, runID, data)
		event.TS = ts
		bus.Publish(event)
	}
	emit(events.RunStarted, "r1", at(0), map[string]any{"run_id": "r1"})
	emit(events.ModelResponse, "r1", at(2), map[string]any{"content": "", "tool_calls": []any{map[string]any{"id": "1"}}, "duration_ms": 2000,
		"timings": map[string]any{"prompt_ms": 500, "predicted_ms": 1500}})
	emit(events.ToolCallEvent, "r1", at(3), map[string]any{"name": "read_file", "args": map[string]any{"path": "NOTES.md"}})
	emit(events.ToolResult, "r1", at(4), map[string]any{"name": "read_file", "ok": true, "ms": 300})
	emit(events.ToolCallEvent, "r1", at(5), map[string]any{"name": "read_file", "args": map[string]any{"path": "NOTES.md"}})
	emit(events.ToolResult, "r1", at(6), map[string]any{"name": "read_file", "ok": false, "ms": 100})
	emit(events.ApprovalRequired, "r1", at(7), map[string]any{"call_id": "c1"})
	emit(events.ApprovalDecided, "r1", at(11), map[string]any{"call_id": "c1", "decision": "approve"})
	emit(events.ModelResponse, "r1", at(12), map[string]any{"content": "", "duration_ms": 1000})
	emit(events.RunStopped, "r1", at(20), map[string]any{"reason": "done"})

	deadline := time.Now().Add(2 * time.Second)
	var row Counters
	for time.Now().Before(deadline) {
		row = manager.Snapshot(cfg.DefaultAgentID()).Connections["local"]
		if len(row.RecentRuns) == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(row.RecentRuns) != 1 {
		t.Fatalf("the run never reached the connection row: %+v", row)
	}
	record := row.RecentRuns[0]
	if record.TotalMS != 20_000 {
		t.Errorf("total %d ms, want 20000", record.TotalMS)
	}
	if record.ModelMS != 3_000 {
		t.Errorf("model %d ms, want 3000", record.ModelMS)
	}
	if record.ToolMS != 400 {
		t.Errorf("tools %d ms, want 400", record.ToolMS)
	}
	if record.WaitingMS != 4_000 {
		t.Errorf("waiting %d ms, want 4000 (the card, required to decided)", record.WaitingMS)
	}
	// The tool counts are tallied once, not once per counter set. They were
	// doubled when they lived in add(), which runs for the agent AND the
	// connection.
	if record.ToolCalls != 2 || record.ToolFailures != 1 {
		t.Errorf("tool calls %d failures %d, want 2 and 1", record.ToolCalls, record.ToolFailures)
	}
	if record.EmptyReplies != 1 {
		t.Errorf("empty replies %d, want 1 (the second response said and called nothing)", record.EmptyReplies)
	}
	if record.RepeatedCalls != 1 {
		t.Errorf("repeated calls %d, want 1 (read_file on NOTES.md twice)", record.RepeatedCalls)
	}
	// And the lifetime sums match the one run that produced them.
	if row.RunModelMS != 3_000 || row.RunToolMS != 400 || row.RunWaitingMS != 4_000 || len(row.RunTimeMS) != 1 || row.RunTimeMS[0] != 20_000 {
		t.Errorf("lifetime sums disagree with the only run: %+v", row)
	}

	// A run whose event carries no time at all -- every run journalled before this
	// item -- adds nothing, rather than a row of zeros that would drag the median
	// and every rate towards nothing.
	before := manager.Snapshot(cfg.DefaultAgentID()).Connections["local"]
	bus.Publish(events.New(events.RunStopped, item.ID, "legacy", map[string]any{"reason": "done"}))
	deadline = time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	after := manager.Snapshot(cfg.DefaultAgentID()).Connections["local"]
	if len(after.RecentRuns) != len(before.RecentRuns) || len(after.RunTimeMS) != len(before.RunTimeMS) {
		t.Errorf("a run with no reported time was recorded anyway: before=%d/%d after=%d/%d",
			len(before.RecentRuns), len(before.RunTimeMS), len(after.RecentRuns), len(after.RunTimeMS))
	}
}

// The recent window is the last RecentRunWindow runs and no more, so a long-lived
// ledger does not grow a record per run forever on that side.
func TestTheRecentWindowKeepsTheLastTwenty2ji(t *testing.T) {
	counters := Counters{}
	for index := 1; index <= RecentRunWindow+5; index++ {
		recordRun(&counters, map[string]any{
			"time": map[string]any{"total_ms": index * 1000, "model_ms": index * 100},
		}, nil, "")
	}
	if len(counters.RecentRuns) != RecentRunWindow {
		t.Fatalf("recent window holds %d, want %d", len(counters.RecentRuns), RecentRunWindow)
	}
	if counters.RecentRuns[0].TotalMS != 6000 {
		t.Errorf("the window kept the wrong end: first is %d ms, want 6000", counters.RecentRuns[0].TotalMS)
	}
	if last := counters.RecentRuns[RecentRunWindow-1].TotalMS; last != int64((RecentRunWindow+5)*1000) {
		t.Errorf("the window kept the wrong end: last is %d ms", last)
	}
	// The lifetime side keeps every run, which is what makes lifetime and recent
	// different figures worth showing side by side.
	if len(counters.RunTimeMS) != RecentRunWindow+5 {
		t.Errorf("lifetime kept %d run times, want %d", len(counters.RunTimeMS), RecentRunWindow+5)
	}
}
