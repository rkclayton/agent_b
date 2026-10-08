package projection

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"harness/internal/events"
)

func TestLiveProjectionPerEventCostDoesNotGrowWithStoredEvents2pd(t *testing.T) {
	seed := func(count int, kind string) Snapshot {
		state := Empty("main")
		for i := 0; i < count; i++ {
			state.Chat = append(state.Chat, ChatEntry{Type: "agent", Key: fmt.Sprintf("turn:run:%d", i), RunID: "run", Turn: i})
		}
		state.Chat[len(state.Chat)-1].Key = "turn:active"
		state.Chat[len(state.Chat)-1].Turn = 1
		if kind == events.ToolResult {
			state.Chat[len(state.Chat)-1] = ChatEntry{Type: "tool", Key: "tool:call", CallID: "call", Name: "shell"}
			state.Tools = []Tool{{Name: "shell", Enabled: true}}
		}
		state.Cursor = Cursor{Generation: "main.jsonl", Offset: int64(count)}
		return state
	}
	measure := func(count int, kind string) (float64, time.Duration) {
		state := seed(count, kind)
		store := NewStore()
		data := map[string]any{"turn": 1, "kind": "content", "text": "x"}
		if kind == events.ToolResult {
			data = map[string]any{"call_id": "call", "name": "shell", "preview": "ok"}
		}
		event := events.New(kind, "main", "run", data)
		cursor := events.LogCursor{Generation: "main.jsonl", Offset: int64(count + 1)}
		apply := func() {
			store.states["main"], store.initialized["main"] = state, true
			store.Apply(event, cursor)
		}
		allocations := testing.AllocsPerRun(100, apply)
		started := time.Now()
		for range 500 {
			apply()
		}
		return allocations, time.Since(started)
	}
	for _, kind := range []string{events.ModelDelta, events.ToolResult} {
		one, oneWall := measure(1, kind)
		full, fullWall := measure(200, kind)
		t.Logf("%s wall one=%s full=%s; verdict allocations/event one=%.0f full=%.0f", kind, oneWall, fullWall, one, full)
		// The race runtime shifts this count between identical paths: by one, and
		// on hosted Ubuntu by two (tool.result 48 against 50 on 3b32c742, a
		// test-only commit, and twice on 4816a43f); retained-size growth is
		// materially larger (the planted copy is +600).
		if full > one+4 {
			t.Errorf("%s per-event work grew: one_chat=%.0f allocations/event full_200_chats=%.0f allocations/event", kind, one, full)
		}
	}
	plant := func(count int) float64 {
		state := seed(count, events.ModelDelta)
		return testing.AllocsPerRun(10, func() {
			raw, _ := json.Marshal(state.Chat)
			var copied []ChatEntry
			_ = json.Unmarshal(raw, &copied)
		})
	}
	one, full := plant(1), plant(200)
	if full <= one {
		t.Fatalf("pre-v1.57 full-copy projection was not rejected: one_chat=%.0f allocations/event full_200_chats=%.0f allocations/event", one, full)
	}
	t.Logf("pre-v1.57 full-copy projection rejected: one_chat=%.0f allocations/event full_200_chats=%.0f allocations/event", one, full)
}

func TestSnapshotReadCostDoesNotGrowWithTimeline2pr(t *testing.T) {
	measure := func(count int) float64 {
		state := Empty("main")
		state.Timeline = make([]events.Event, count)
		return testing.AllocsPerRun(100, func() { _ = SnapshotForRead(state) })
	}
	small, large := measure(300), measure(3000)
	if large != small {
		t.Fatalf("timeline read allocations grew: small=%.0f large=%.0f", small, large)
	}
}

func TestNextIsPureAndEmitsVersionedCursorPatch(t *testing.T) {
	previous := seeded(t)
	before, _ := json.Marshal(previous)
	record := Record{
		Cursor: Cursor{Generation: "main-a.jsonl", Offset: 42},
		Event: events.Event{SessionID: "main", Type: events.MessageAppended, Data: map[string]any{
			"message": events.Message{ID: "m1", Role: "user", Content: "hello", Category: "history"},
		}},
	}
	next, patch, err := Next(previous, record)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, _ := json.Marshal(previous)
	if !reflect.DeepEqual(unchanged, before) {
		t.Fatal("Next mutated its input snapshot")
	}
	if len(next.Messages) != 1 || next.Messages[0].ID != "m1" {
		t.Fatalf("messages = %#v", next.Messages)
	}
	if patch.SchemaVersion != SchemaVersion || patch.Cursor != record.Cursor || patch.SessionID != "main" {
		t.Fatalf("patch envelope = %#v", patch)
	}
	if len(patch.Operations) == 0 {
		t.Fatal("message append emitted no projection operation")
	}
}

func TestPlanBindingUpdateDoesNotEraseUnmentionedSessionState(t *testing.T) {
	state := seeded(t)
	state.ConnectionID, state.Runnable, state.MemoryPath, state.MemoryContent = "planner", true, "memory.md", "remembered"
	next, _, err := Next(state, Record{Cursor: Cursor{Generation: "plan.events", Offset: 90}, Event: events.Event{
		SessionID: "main", Type: events.SessionUpdated,
		Data: map[string]any{"role": "d", "plan_id": "stable", "plan_name": "Stable"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if next.Role != "d" || next.PlanID != "stable" || next.ConnectionID != "planner" || !next.Runnable || next.MemoryPath != "memory.md" || next.MemoryContent != "remembered" {
		t.Fatalf("plan update erased session state: %+v", next)
	}
}

func TestLegacyConnectionAliasesReplayThroughOneTable(t *testing.T) {
	legacyID := "ser" + "ver_id"
	legacyLabel := "b_" + "pro" + "file"
	created := Record{Cursor: Cursor{Generation: "legacy.events", Offset: 1}, Event: events.Event{
		SessionID: "legacy", Type: events.SessionCreated,
		Data: map[string]any{"session": map[string]any{
			"id": "legacy", legacyID: "old-endpoint", legacyLabel: "Old endpoint",
			"created_at": "2026-09-01T00:00:00Z", "run": map[string]any{"status": "idle"},
		}},
	}}
	state, err := NextState(Empty("legacy"), created)
	if err != nil {
		t.Fatal(err)
	}
	if state.ConnectionID != "old-endpoint" || state.BConnection != "Old endpoint" {
		t.Fatalf("legacy seed aliases were not projected: %+v", state)
	}

	updated, err := NextState(state, Record{Cursor: Cursor{Generation: "legacy.events", Offset: 2}, Event: events.Event{
		SessionID: "legacy", Type: events.SessionUpdated,
		Data: map[string]any{legacyID: "replacement", "main_" + "pro" + "file": "Replacement"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ConnectionID != "replacement" || updated.BConnection != "Replacement" {
		t.Fatalf("legacy update aliases were not projected: %+v", updated)
	}
	encoded, _ := json.Marshal(updated)
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &topLevel); err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []string{legacyID, legacyLabel, "main_" + "pro" + "file"} {
		if _, present := topLevel[legacy]; present {
			t.Fatalf("projection emitted top-level legacy field %q: %s", legacy, encoded)
		}
	}
}

func TestS13LegacyCreationRecordRestoresServerBinding(t *testing.T) {
	legacyLabel := "b_" + "profile"
	record := Record{Cursor: Cursor{Generation: "s13-20260924T132321.jsonl", Offset: 1}, Event: events.Event{
		SessionID: "s13", Type: events.SessionCreated,
		Data: map[string]any{"session": map[string]any{
			"id": "s13", "agent_id": "agent-b", "server_id": "server", legacyLabel: "Acmeholding",
			"created_at": "2026-09-23T21:21:00.033258Z", "run": map[string]any{"status": "idle"}, "runnable": true,
		}},
	}}
	state, err := NextState(Empty("s13"), record)
	if err != nil {
		t.Fatal(err)
	}
	if state.ConnectionID != "server" || state.BConnection != "Acmeholding" || !state.Runnable {
		t.Fatalf("s13 replay = %+v", state)
	}
}

func TestMessageAttachmentProjectsIntoChatEntry(t *testing.T) {
	state := seeded(t)
	attachment := events.Attachment{Path: "attachments/spec.txt", Bytes: 12, SHA256: strings.Repeat("a", 64)}
	next, _, err := Next(state, Record{Cursor: Cursor{Generation: "attachment.events", Offset: 200}, Event: events.Event{
		SessionID: "main", Type: events.MessageAppended,
		Data: map[string]any{"message": events.Message{ID: "m-attachment", Role: "user", Content: "review", Category: "history", Attachments: []events.Attachment{attachment}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Chat) != 1 || len(next.Chat[0].Attachments) != 1 || next.Chat[0].Attachments[0] != attachment {
		t.Fatalf("chat=%+v", next.Chat)
	}
}

func TestCompactionSummaryProjectsAsSummaryWithoutMachineryMarkers(t *testing.T) {
	state := seeded(t)
	content := "Progress note (auto-summary of earlier turns):\nKept the operator task.\n\n[BEGIN COMPACTION EVIDENCE]\n{\"excerpt\":\"internal\"}\n[END COMPACTION EVIDENCE]"
	next, _, err := Next(state, Record{Cursor: Cursor{Generation: "summary.events", Offset: 200}, Event: events.Event{
		SessionID: "main", Type: events.MessageAppended,
		Data: map[string]any{"message": events.Message{ID: "m-summary", Role: "system", Content: content, Category: "summary"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Chat) != 1 || next.Chat[0].Type != "summary" || next.Chat[0].Text != "Kept the operator task." {
		t.Fatalf("chat=%+v", next.Chat)
	}
	if len(next.Messages) != 1 || next.Messages[0].Role != "system" || next.Messages[0].Content != content {
		t.Fatalf("messages=%+v", next.Messages)
	}
}

func TestFreshContextKeepsVisibleTranscriptAndHidesHandOff2qh(t *testing.T) {
	state := seeded(t)
	state.Messages = []events.Message{{ID: "old", Role: "assistant", Category: "history"}, {ID: "task", Role: "user", Content: "current task", Category: "history"}}
	state.Chat = []ChatEntry{{Type: "user", Key: "message:old-user", Text: "earlier question"}, {Type: "agent", Key: "turn:old", Text: "earlier answer", Done: true}}
	next, _, err := Next(state, Record{Cursor: Cursor{Generation: "fresh.events", Offset: 200}, Event: events.Event{
		Seq: 7, SessionID: "main", RunID: "run", Type: events.Compaction,
		Data: map[string]any{"kind": "fresh", "before": 1000, "after": 100, "affected_ids": []string{"old"}, "summary_message_id": "handoff"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	next, _, err = Next(next, Record{Cursor: Cursor{Generation: "fresh.events", Offset: 300}, Event: events.Event{
		Seq: 8, SessionID: "main", RunID: "run", Type: events.MessageAppended,
		Data: map[string]any{"message": events.Message{ID: "handoff", Role: "harness", Category: "summary", Content: "Fresh-context hand-off:\nDONE: earlier work"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Chat) != 3 || next.Chat[0].Text != "earlier question" || next.Chat[1].Text != "earlier answer" || next.Chat[2].Event == nil || next.Chat[2].Event.Type != events.Compaction {
		t.Fatalf("visible transcript=%+v", next.Chat)
	}
	if len(next.Messages) != 2 || next.Messages[0].ID != "task" || next.Messages[1].ID != "handoff" {
		t.Fatalf("model context=%+v", next.Messages)
	}
}

func TestEmptyToolArgumentsRemainAnObjectInProjectedJSON(t *testing.T) {
	state := seeded(t)
	next, _, err := Next(state, Record{Cursor: Cursor{Generation: "empty-args.events", Offset: 200}, Event: events.Event{
		SessionID: "main", RunID: "r1", Type: events.ToolCallEvent,
		Data: map[string]any{"call_id": "call-empty", "name": "recall", "args": map[string]any{}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Chat) != 1 || next.Chat[0].Args == nil || len(*next.Chat[0].Args) != 0 {
		t.Fatalf("chat=%+v", next.Chat)
	}
	encoded, err := json.Marshal(next.Chat[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"args":{}`) {
		t.Fatalf("empty arguments were not preserved as an object: %s", encoded)
	}
	encodedAgent, err := json.Marshal(ChatEntry{Type: "agent", Key: "turn:1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedAgent), `"args"`) {
		t.Fatalf("non-tool entry gained an args field: %s", encodedAgent)
	}
}

func TestAbortAndRetryEventsAreVisibleHarnessNotices(t *testing.T) {
	state := seeded(t)
	for index, eventType := range []string{events.ModelRetry, events.RunAborted} {
		var err error
		state, _, err = Next(state, Record{Cursor: Cursor{Generation: "main.events", Offset: int64(index + 1)}, Event: events.Event{Seq: int64(index + 1), SessionID: "main", RunID: "r1", Type: eventType, Data: map[string]any{"reason": "test"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(state.Chat) != 2 || state.Chat[0].Type != "notice" || state.Chat[0].Event.Type != events.ModelRetry || state.Chat[1].Event.Type != events.RunAborted {
		t.Fatalf("chat=%+v", state.Chat)
	}
}

func TestEachByteLimitTrimKeepsItsPointInTheChat2su(t *testing.T) {
	state := seeded(t)
	for index := 0; index < 10; index++ {
		var err error
		state, _, err = Next(state, Record{Cursor: Cursor{Generation: "trim.events", Offset: int64(index + 1)}, Event: events.Event{Seq: int64(index + 1), SessionID: "main", RunID: "r1", Type: events.Compaction, Data: map[string]any{"trigger": "byte_limit_trim", "trimmed_results": 1, "before": 100, "after": 100}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(state.Chat) != 10 || state.Chat[0].Event == nil {
		t.Fatalf("chat=%+v", state.Chat)
	}
	for _, row := range state.Chat {
		if intValue(eventMap(row.Event.Data)["trimmed_results"]) != 1 {
			t.Fatalf("trim notice=%+v", row)
		}
	}
}

func TestToolProgressProjectsOnlyTheLatestBoundedLine2qf(t *testing.T) {
	state := seeded(t)
	state.Run.Status = "running"
	next, _, err := Next(state, Record{Cursor: Cursor{Generation: "live", Offset: 2}, Event: events.Event{TS: "2026-10-05T18:00:00Z", SessionID: "main", RunID: "r1", Type: events.ToolProgress, Data: map[string]any{"name": "shell", "line": "Passed 41 tests"}}})
	if err != nil {
		t.Fatal(err)
	}
	if next.Activity.ToolLastLine != "Passed 41 tests" {
		t.Fatalf("activity=%+v", next.Activity)
	}
}

func TestDelegateLiveStepProjectsOntoParent2qf(t *testing.T) {
	state := seeded(t)
	state.Run.Status = "running"
	state.Activity.ActiveTool = "delegate"
	next, _, err := Next(state, Record{Cursor: Cursor{Generation: "live", Offset: 2}, Event: events.Event{SessionID: "main", RunID: "r1", Type: events.DelegatedUsage, Data: map[string]any{"child_id": "child", "status": "running", "tool": "read_file", "target": "src/main.go", "turn": 3, "live": true}}})
	if err != nil {
		t.Fatal(err)
	}
	if next.Activity.Delegate == nil || next.Activity.Delegate.Tool != "read_file" || next.Activity.Delegate.Target != "src/main.go" || next.Activity.Delegate.Turn != 3 {
		t.Fatalf("delegate=%+v", next.Activity.Delegate)
	}
	if len(next.Timeline) != len(state.Timeline) {
		t.Fatalf("transient delegate progress entered durable timeline")
	}
}

func TestResetOnlyGenerationIsExplicitlyIncomplete(t *testing.T) {
	state := Empty("main")
	next, _, err := Next(state, Record{
		Cursor: Cursor{Generation: "main-reset.jsonl", Offset: 101},
		Event:  events.Event{SessionID: "main", Type: events.SessionReset, Data: map[string]any{"log_path": "next.jsonl"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Complete {
		t.Fatal("reset-only generation was presented as a complete snapshot")
	}
	if next.Cursor.Offset != 101 || next.LogPath != "next.jsonl" {
		t.Fatalf("snapshot = %#v", next)
	}
}

func TestStreamingProjectionUsesIncrementalTextOperations(t *testing.T) {
	state := seeded(t)
	state, _, _ = Next(state, Record{Cursor: Cursor{Generation: "main-a.jsonl", Offset: 30}, Event: events.Event{TS: "2026-09-05T00:00:00Z", SessionID: "main", RunID: "r1", Type: events.ModelRequest, Data: map[string]any{"turn": 1}}})
	state, _, _ = Next(state, Record{Cursor: Cursor{Generation: "main-a.jsonl", Offset: 40}, Event: events.Event{TS: "2026-09-05T00:00:01Z", SessionID: "main", RunID: "r1", Type: events.ModelDelta, Data: map[string]any{"turn": 1, "kind": "reasoning", "text": "first"}}})
	_, patch, err := Next(state, Record{Cursor: Cursor{Generation: "main-a.jsonl", Offset: 50}, Event: events.Event{TS: "2026-09-05T00:00:02Z", SessionID: "main", RunID: "r1", Type: events.ModelDelta, Data: map[string]any{"turn": 1, "kind": "reasoning", "text": " second"}}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, operation := range patch.Operations {
		if operation.Op == "append" && strings.HasSuffix(operation.Path, "/reasoning") && string(operation.Value) == `" second"` {
			found = true
		}
	}
	if !found {
		t.Fatalf("operations=%+v", patch.Operations)
	}
}

func TestBudgetReplacementDoesNotRetainOmittedFields(t *testing.T) {
	state := seeded(t)
	state.Budget.ToolMarginalTokens = map[string]int{"removed_tool": 145}
	next, _, err := Next(state, Record{
		Cursor: Cursor{Generation: "main-a.jsonl", Offset: 80},
		Event: events.Event{SessionID: "main", Type: events.BudgetEvent, Data: map[string]any{
			"n_ctx": 32768, "categories": map[string]int{"tools": 1221},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Budget.ToolMarginalTokens != nil {
		t.Fatalf("omitted marginal map survived replacement: %#v", next.Budget.ToolMarginalTokens)
	}
}

func TestMessageRemovalDeletesProjectedMessageAndChatEntry(t *testing.T) {
	state := seeded(t)
	appended := Record{Cursor: Cursor{Generation: "main-a.jsonl", Offset: 30}, Event: events.Event{
		SessionID: "main", Type: events.MessageAppended,
		Data: map[string]any{"message": events.Message{ID: "m1", Role: "user", Content: "remove me", Category: "history"}},
	}}
	state, _, _ = Next(state, appended)
	removed := Record{Cursor: Cursor{Generation: "main-a.jsonl", Offset: 50}, Event: events.Event{
		SessionID: "main", Type: events.MessageRemoved, Data: map[string]any{"id": "m1", "reason": "operator_repair"},
	}}
	next, patch, err := Next(state, removed)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Messages) != 0 || len(next.Chat) != 0 {
		t.Fatalf("messages=%#v chat=%#v", next.Messages, next.Chat)
	}
	if len(patch.Operations) == 0 {
		t.Fatal("removal emitted no patch")
	}
}

func TestFilesDeliveredProjectsAsDurableChatNotice(t *testing.T) {
	state := seeded(t)
	record := Record{Cursor: Cursor{Generation: "main-a.jsonl", Offset: 30}, Event: events.Event{
		Seq: 9, SessionID: "main", RunID: "r1", Type: events.FilesDelivered,
		Data: map[string]any{"mode": "both", "items": []any{map[string]any{"source_path": "done.txt", "delivered_path": `C:\\Users\\operator\\Agent_b\\done.txt`, "status": "copied"}}},
	}}
	next, _, err := Next(state, record)
	if err != nil {
		t.Fatal(err)
	}
	entry := next.Chat[len(next.Chat)-1]
	if entry.Type != "notice" || entry.RunID != "r1" || entry.Event == nil || entry.Event.Type != events.FilesDelivered {
		t.Fatalf("chat entry=%+v", entry)
	}
}

func TestShellGrantAndLapseProjectAsReplayableNotices(t *testing.T) {
	state := seeded(t)
	grant := Record{Cursor: Cursor{Generation: "main-a.jsonl", Offset: 30}, Event: events.Event{
		Seq: 9, SessionID: "main", RunID: "r1", Type: events.ShellGrant,
		Data: map[string]any{"run_id": "r1", "scope": "run", "rule": "operator_command", "identity": "operator", "executable": `C:\Program Files\Git\cmd\git.exe`},
	}}
	next, _, err := Next(state, grant)
	if err != nil {
		t.Fatal(err)
	}
	lapse := Record{Cursor: Cursor{Generation: "main-a.jsonl", Offset: 60}, Event: events.Event{
		Seq: 10, SessionID: "main", RunID: "r1", Type: events.ShellGrantLapsed,
		Data: map[string]any{"run_id": "r1", "scope": "run", "rule": "operator_command", "identity": "operator", "executable": `C:\Program Files\Git\cmd\git.exe`, "reason": "run ended"},
	}}
	next, _, err = Next(next, lapse)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Chat) < 2 || next.Chat[len(next.Chat)-2].Event.Type != events.ShellGrant || next.Chat[len(next.Chat)-1].Event.Type != events.ShellGrantLapsed {
		t.Fatalf("chat=%+v", next.Chat)
	}
}

func TestModelAvailabilityAndRunAsYouReconstructFromEvents(t *testing.T) {
	state := Empty("main")
	records := []events.Event{
		events.New(events.ModelUnreachable, "main", "r1", map[string]any{"host": "model.example:8000", "detail": "dial timeout"}),
		events.New(events.ModelBusy, "main", "r1", map[string]any{"host": "model.example:8000", "detail": "connected; waiting"}),
		events.New(events.ShellGrant, "main", "r1", map[string]any{"scope": "session", "identity": "operator", "rule": "shell_boundary"}),
		events.New(events.ModelReachable, "main", "", map[string]any{"connection_id": "main"}),
		events.New(events.ShellGrantLapsed, "main", "r1", map[string]any{"scope": "session", "identity": "operator", "reason": "revoked by operator"}),
	}
	for index, event := range records {
		var err error
		state, _, err = Next(state, Record{Cursor: Cursor{Generation: "availability.events", Offset: int64(index + 1)}, Event: event})
		if err != nil {
			t.Fatal(err)
		}
	}
	if state.ModelUnreachable != nil || state.ModelBusy != nil || state.RunAsYou {
		t.Fatalf("replayed unreachable=%+v busy=%+v run_as_you=%t", state.ModelUnreachable, state.ModelBusy, state.RunAsYou)
	}
}

func TestModelBusyEndsWithTheRequestOrRun(t *testing.T) {
	for _, terminal := range []events.Event{
		events.New(events.ModelResponse, "main", "r1", map[string]any{"turn": 1, "content": "done"}),
		events.New(events.RunStopped, "main", "r1", map[string]any{"reason": "done"}),
	} {
		state := Empty("main")
		var err error
		state, _, err = Next(state, Record{Cursor: Cursor{Generation: "busy.events", Offset: 1}, Event: events.New(events.ModelBusy, "main", "r1", map[string]any{"host": "model.example:8000", "detail": "waiting"})})
		if err != nil {
			t.Fatal(err)
		}
		state, _, err = Next(state, Record{Cursor: Cursor{Generation: "busy.events", Offset: 2}, Event: terminal})
		if err != nil {
			t.Fatal(err)
		}
		if state.ModelBusy != nil {
			t.Fatalf("%s left model busy projected: %+v", terminal.Type, state.ModelBusy)
		}
	}
}

func TestStreamingToolTelemetryCarriesOnlyCumulativeMeasures(t *testing.T) {
	state := Empty("main")
	records := []events.Event{
		events.New(events.ModelRequest, "main", "r1", map[string]any{"turn": 1}),
		events.New(events.ModelDelta, "main", "r1", map[string]any{"turn": 1, "kind": "tool_call", "index": 0, "call_id": "call-1", "name": "run_script", "argument_bytes": 12, "argument_tokens": 4, "text": ""}),
		events.New(events.ModelDelta, "main", "r1", map[string]any{"turn": 1, "kind": "tool_call", "index": 0, "call_id": "call-1", "name": "run_script", "argument_bytes": 2400, "argument_tokens": 667, "text": ""}),
	}
	for index, event := range records {
		event.TS = fmt.Sprintf("2026-09-22T16:47:%02dZ", index+1)
		var err error
		state, _, err = Next(state, Record{Cursor: Cursor{Generation: "stream.events", Offset: int64(index + 1)}, Event: event})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(state.Activity.Stream.ToolCalls) != 1 {
		t.Fatalf("tool telemetry=%+v", state.Activity.Stream)
	}
	call := state.Activity.Stream.ToolCalls[0]
	if call.Name != "run_script" || call.CallID != "call-1" || call.ArgumentBytes != 2400 || call.ArgumentTokens != 667 || call.StartedAt == 0 || call.LastChunkAt <= call.StartedAt {
		t.Fatalf("tool call=%+v", call)
	}
	encoded, _ := json.Marshal(call)
	if strings.Contains(string(encoded), "arguments") {
		t.Fatalf("stream telemetry exposed arguments: %s", encoded)
	}
}

func TestModelUnreachableChatNoticeIsOneRowPerCondition(t *testing.T) {
	state := Empty("main")
	records := []events.Event{
		events.New(events.ModelUnreachable, "main", "r1", map[string]any{"host": "model.example:8000", "detail": "dial timeout"}),
		events.New(events.RunStopped, "main", "r1", map[string]any{"reason": "model_unreachable", "detail": "dial timeout"}),
		events.New(events.ModelUnreachable, "main", "r2", map[string]any{"host": "model.example:8000", "detail": "still offline"}),
		events.New(events.RunStopped, "main", "r2", map[string]any{"reason": "model_unreachable", "detail": "still offline"}),
		events.New(events.ModelReachable, "main", "", map[string]any{"connection_id": "main"}),
		events.New(events.ModelUnreachable, "main", "r3", map[string]any{"host": "model.example:8000", "detail": "offline again"}),
		events.New(events.RunStopped, "main", "r3", map[string]any{"reason": "model_unreachable", "detail": "offline again"}),
		events.New(events.ModelUnreachable, "main", "r4", map[string]any{"host": "other.example:9000", "detail": "refused"}),
		events.New(events.RunStopped, "main", "r4", map[string]any{"reason": "model_unreachable", "detail": "refused"}),
	}
	for index, event := range records {
		event.Seq = int64(index + 1)
		var err error
		state, _, err = Next(state, Record{Cursor: Cursor{Generation: "unreachable.events", Offset: int64(index + 1)}, Event: event})
		if err != nil {
			t.Fatal(err)
		}
	}
	var notices []ChatEntry
	for _, entry := range state.Chat {
		if entry.Event != nil && entry.Event.Type == events.RunStopped && stringValue(eventMap(entry.Event.Data)["reason"]) == "model_unreachable" {
			notices = append(notices, entry)
		}
	}
	if len(notices) != 3 {
		t.Fatalf("unreachable notices=%+v", notices)
	}
	if notices[0].RunID != "r2" || notices[0].Text != "model.example:8000" || notices[0].Decision != "resolved" {
		t.Fatalf("updated and resolved first condition=%+v", notices[0])
	}
	if notices[1].RunID != "r3" || notices[1].Text != "model.example:8000" || notices[1].Decision != "" {
		t.Fatalf("new condition after recovery=%+v", notices[1])
	}
	if notices[2].RunID != "r4" || notices[2].Text != "other.example:9000" {
		t.Fatalf("different host condition=%+v", notices[2])
	}
}

func TestSessionRenameReplaysAuthorAndUserPin(t *testing.T) {
	state := seeded(t)
	aux, _, err := Next(state, Record{Cursor: Cursor{Generation: "main-a.jsonl", Offset: 30}, Event: events.Event{
		Seq: 9, SessionID: "main", Type: events.SessionRenamed, Data: map[string]any{"label": "Aux title", "by": "aux"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if aux.Label != "Aux title" || aux.NamePinned {
		t.Fatalf("aux rename=%+v", aux)
	}
	user, _, err := Next(aux, Record{Cursor: Cursor{Generation: "main-a.jsonl", Offset: 60}, Event: events.Event{
		Seq: 10, SessionID: "main", Type: events.SessionRenamed, Data: map[string]any{"label": "Pinned title", "by": "user"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if user.Label != "Pinned title" || !user.NamePinned {
		t.Fatalf("user rename=%+v", user)
	}
}

func TestRetainedWorkspaceBoundEventReconstructs(t *testing.T) {
	state := seeded(t)
	dir := `acmebound`
	state, _, err := Next(state, Record{Cursor: Cursor{Generation: "bind.events", Offset: 2}, Event: events.New(events.WorkspaceBound, "main", "", map[string]any{"workspace_dir": dir, "project_content": "instructions"})})
	if err != nil || state.WorkspaceDir != dir || state.ProjectContent != "instructions" {
		t.Fatalf("bound state=%+v err=%v", state, err)
	}
}

func TestRunStoppingAndHeldQueueReplay(t *testing.T) {
	state := Empty("main")
	state.Run = Run{Status: "running", RunID: "r1", MaxTurns: 40}
	stopping, _, err := Next(state, Record{Cursor: Cursor{Generation: "main.events", Offset: 10}, Event: events.Event{Seq: 1, SessionID: "main", RunID: "r1", Type: events.RunStopping, Data: map[string]any{"reason": "safe"}}})
	if err != nil || stopping.Run.Status != "stopping" {
		t.Fatalf("stopping=%+v err=%v", stopping.Run, err)
	}
	stopping.QueuedMessages = 2
	held, _, err := Next(stopping, Record{Cursor: Cursor{Generation: "main.events", Offset: 20}, Event: events.Event{Seq: 2, SessionID: "main", RunID: "r1", Type: events.RunStopped, Data: map[string]any{"reason": "safe", "queue_held": true}}})
	if err != nil || held.Run.Status != "held" || held.Run.LastStopReason != "safe" || held.Run.QueuePosition != 2 {
		t.Fatalf("held=%+v err=%v", held.Run, err)
	}
}

func TestRunResultLabelReplay(t *testing.T) {
	state := Empty("main")
	state.Run = Run{Status: "idle", LastStopReason: "done", LastRunID: "r1", ArmedDetectors: []string{"novel_action"}}
	next, _, err := Next(state, Record{Cursor: Cursor{Generation: "main.events", Offset: 10}, Event: events.New(events.RunLabeled, "main", "r1", map[string]any{"label": "mixed"})})
	if err != nil || next.Run.ResultLabel != "mixed" || next.Run.LastStopReason != "done" || len(next.Run.ArmedDetectors) != 1 {
		t.Fatalf("run=%+v err=%v", next.Run, err)
	}
	stale, _, err := Next(next, Record{Cursor: Cursor{Generation: "main.events", Offset: 20}, Event: events.New(events.RunLabeled, "main", "older", map[string]any{"label": "stuck"})})
	if err != nil || stale.Run.ResultLabel != "mixed" {
		t.Fatalf("stale run=%+v err=%v", stale.Run, err)
	}
}

func TestReadFileUsesDurableByteBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main-a.jsonl")
	one := events.Event{Seq: 1, TS: "2026-09-05T00:00:00Z", SessionID: "main", Type: events.SessionReset, Data: map[string]any{}}
	two := events.Event{Seq: 2, TS: "2026-09-05T00:00:01Z", SessionID: "main", Type: events.SessionRenamed, Data: map[string]any{"label": "next"}}
	first, _ := json.Marshal(one)
	second, _ := json.Marshal(two)
	content := append(append(append([]byte{}, first...), '\n'), second...)
	content = append(content, '\n')
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	boundary := int64(len(first) + 1)
	records, offset, err := ReadFile(path, boundary)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || offset != boundary || records[0].Cursor.Offset != boundary {
		t.Fatalf("records=%d offset=%d cursor=%#v", len(records), offset, records[0].Cursor)
	}
	if _, _, err := ReadFile(path, boundary-1); err == nil {
		t.Fatal("non-record byte offset was accepted")
	}
}

func TestCacheIsKeyedByDurableOffsetAndDiscardable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main-a.jsonl")
	created := events.Event{Seq: 1, TS: "2026-09-05T00:00:00Z", SessionID: "main", Type: events.SessionCreated, Data: map[string]any{
		"session": map[string]any{"id": "main", "label": "main", "run": map[string]any{"status": "idle"}, "tools": []any{}, "messages": []any{}},
	}}
	encoded, _ := json.Marshal(created)
	encoded = append(encoded, '\n')
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	cache := NewCache()
	first, err := cache.ProjectFile(path, int64(len(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	first.Label = "mutated caller copy"
	cached, err := cache.ProjectFile(path, int64(len(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	if cached.Label != "main" {
		t.Fatalf("caller mutation changed cached snapshot: %#v", cached)
	}
	cache.Clear()
	second, err := cache.ProjectFile(path, int64(len(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cached, second) || !second.Complete {
		t.Fatalf("rebuilt snapshot differs: cached=%#v second=%#v", cached, second)
	}
}

func seeded(t *testing.T) Snapshot {
	t.Helper()
	next, _, err := Next(Empty("main"), Record{
		Cursor: Cursor{Generation: "main-a.jsonl", Offset: 20},
		Event: events.Event{SessionID: "main", Type: events.SessionCreated, Data: map[string]any{
			"session": map[string]any{
				"id": "main", "label": "main", "connection_id": "acme", "workspace": "workspace",
				"run":   map[string]any{"status": "idle", "max_turns": 40},
				"tools": []any{}, "messages": []any{}, "runnable": true,
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func cloneSnapshot(t *testing.T, value Snapshot) Snapshot {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result Snapshot
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMemoryNotedUpdatesStateWithoutSecondChatNotice2kt(t *testing.T) {
	if chatNotice(events.MemoryNoted) {
		t.Fatal("memory.noted still renders beside the one harness memory line")
	}
}
