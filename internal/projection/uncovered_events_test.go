package projection

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"harness/internal/events"
)

func TestPreviouslyUncoveredEventsPinWholeProjectionAndPatch2ue(t *testing.T) {
	type pin struct {
		name string
		kind string
		data map[string]any
		want string
	}
	pins := []pin{
		{name: "policy revoked", kind: events.PolicyRevoked, want: "e4f4b656e16c801ebb3035f6074ef070ecc9c5879aff45e28e1497ec7b53e4ae"},
		{name: "policy denied", kind: events.PolicyDenied, want: "4fa7fc4075276a6f765c18ff44dadbc2b47cdd1f2438af82295836af1f30b7c1"},
		{name: "memory cleared", kind: events.MemoryCleared, data: map[string]any{"dir": "workspace"}, want: "3375487b897a0d539a5018a84e50aba168aedd6508d516fa1d4296cbd220bfdd"},
		{name: "session reopened", kind: events.SessionReopened, want: "8b9d54d19e90cc5f97b9f8c225069e44710a048e11f10f9833d6238a931dfafb"},
		{name: "run queued", kind: events.RunQueued, data: map[string]any{"run_id": "queued-run", "position": 3, "behind": "agent-c"}, want: "f6c3af7d7fca9d95c8de074489bc738833d7d52248f05a000d69672152e1d944"},
		{name: "run resumed", kind: events.RunResumed, want: "d415bd9198e9c1741678f021c0bf4177f1dd07f533b80ba94bb6a5bb8d9da930"},
		{name: "model progress", kind: events.ModelProgress, data: map[string]any{"turn": 2, "phase": "thinking"}, want: "1a93e1d08787f3606cdf0bb3ee8c27fcbeed022dbbb829686d486dc4582cbce9"},
		{name: "tool toggled", kind: events.ToolToggled, data: map[string]any{"name": "shell", "enabled": false}, want: "58ee6a5fa6967cb07aaa3753e5d323dae92f297d11321118b9e2e47d22b19afa"},
		{name: "message updated", kind: events.MessageUpdated, data: map[string]any{"id": "message-one", "patch": map[string]any{"content": "revised", "tokens": 9, "elided": true, "tool_calls": []any{}}}, want: "3da9083a40136f3a4471370b811442373aa31b8a193f9267ff5a76f5076df25d"},
		{name: "messages reminted", kind: events.MessagesReminted, data: map[string]any{"reminted": []any{map[string]any{"index": 1, "from": "message-one", "to": "message-two"}}}, want: "168963defd522f214964bcf27c4c558304152cf5e5430efbc160aca258126482"},
		{name: "cycle detected", kind: events.CycleDetected, want: "13f0b55e2bbf2db255473e03615da4fddc47f1ae9f6878a6d022687d3a184c49"},
		{name: "workspace conflict", kind: events.WorkspaceConflict, want: "827aa9e575f074f521ae6c27da1dd915df329cef668d50031113714287048201"},
		{name: "compaction summary", kind: events.CompactionSummary, data: map[string]any{"dispatched": true, "usage": map[string]any{"prompt_tokens": 21, "completion_tokens": 8}}, want: "621901a94b56ac5cfc5bb6f15e523be3a3442c8cbb5746b44fced986ffbb82c8"},
	}

	for index, test := range pins {
		t.Run(test.name, func(t *testing.T) {
			state := uncoveredEventPinState(t)
			record := Record{
				Cursor: Cursor{Generation: "pin.events", Offset: int64(100 + index)},
				Event: events.Event{SessionID: "main", RunID: "run-one", Seq: int64(200 + index), Type: test.kind, Data: test.data},
			}
			next, patch, err := Next(state, record)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(struct {
				Snapshot Snapshot `json:"snapshot"`
				Patch    Patch    `json:"patch"`
			}{next, patch})
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(encoded)
			got := hex.EncodeToString(digest[:])
			if got != test.want {
				t.Fatalf("whole snapshot and patch digest = %s, want %s", got, test.want)
			}
		})
	}
}

func uncoveredEventPinState(t *testing.T) Snapshot {
	t.Helper()
	state := seeded(t)
	state.WorkspaceDir = "workspace"
	state.MemoryContent = "remembered"
	state.Closed = true
	state.RunAsYou = true
	state.Run = Run{Status: "paused", RunID: "run-one", QueuePosition: 2, WaitingBehind: "agent-b"}
	state.Activity.ActiveTool = "shell"
	state.Tools = []Tool{{Name: "shell", Enabled: true, Calls: 4}}
	state.Messages = []events.Message{
		{ID: "message-one", Role: "assistant", Content: "original", Turn: 2},
		{ID: "message-one", Role: "tool", Content: "tool result", ToolCallID: "call-one"},
	}
	state.Chat = []ChatEntry{{Type: "agent", Key: "turn:run-one:2", RunID: "run-one", Turn: 2, Text: "original", ToolCallIDs: []string{"call-one"}}}
	state, _, err := Next(state, Record{Cursor: Cursor{Generation: "pin.events", Offset: 50}, Event: events.Event{
		SessionID: "main", Type: events.PolicyApproved, Data: map[string]any{
			"root": "workspace", "hash": "policy-hash",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// Item 2wg: a steer is a chat notice and raises no alarm; the cycle decision still does.
func TestLoopSteerIsANoticeWithoutTheAlarm2wg(t *testing.T) {
	state := uncoveredEventPinState(t)
	next, _, err := Next(state, Record{Cursor: Cursor{Generation: "pin.events", Offset: 300}, Event: events.Event{SessionID: "main", RunID: "run-one", Seq: 300, Type: events.CycleDetected, Data: map[string]any{"stage": "steer", "name": "list_dir", "line": "list_dir on chat has given the same result 3 times"}}})
	if last := next.Chat[len(next.Chat)-1]; err != nil || next.Activity.DispatchAlarm || last.Type != "notice" || last.Event == nil || last.Event.Type != events.CycleDetected {
		t.Fatalf("alarm=%v last=%+v err=%v", next.Activity.DispatchAlarm, last, err)
	}
	cycle, _, err := Next(state, Record{Cursor: Cursor{Generation: "pin.events", Offset: 301}, Event: events.Event{SessionID: "main", RunID: "run-one", Seq: 301, Type: events.CycleDetected}})
	if err != nil || !cycle.Activity.DispatchAlarm || len(cycle.Chat) != len(state.Chat) {
		t.Fatalf("cycle alarm=%v chat=%d err=%v", cycle.Activity.DispatchAlarm, len(cycle.Chat), err)
	}
}
