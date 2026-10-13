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
		{name: "policy revoked", kind: events.PolicyRevoked, want: "4b675345314bcb58f3a0bd43b5bcba6571aa327fde4eb39734187c694e1d77dc"},
		{name: "policy denied", kind: events.PolicyDenied, want: "b195b468f43bb21db0c58ef17a34d7f83a03a349e33a9adcf5c43f8eef353cdf"},
		{name: "memory cleared", kind: events.MemoryCleared, data: map[string]any{"dir": "workspace"}, want: "90bede9e3c6e7c0fb8c7aa7d07fe5c251840b8a20747fddfc6c181e018746fb5"},
		{name: "session reopened", kind: events.SessionReopened, want: "19b82c13d833fbeb89880378c5ae735a6d85b15fb2a0c10f7130acd2302607ec"},
		{name: "run queued", kind: events.RunQueued, data: map[string]any{"run_id": "queued-run", "position": 3, "behind": "agent-c"}, want: "0a46f77ce8364a325f0cdd41986bfee2ee2fdaaaff8bc83ea55330aea8ce26c2"},
		{name: "run resumed", kind: events.RunResumed, want: "e2745062e91017cca1021d9df3c937fc431156e5993c95a8a0d8e9984c98e5c2"},
		{name: "model progress", kind: events.ModelProgress, data: map[string]any{"turn": 2, "phase": "thinking"}, want: "0d074096d33693ed0d85cf3b264f69fcfe12b0cb96da9638c0182321138cdc5d"},
		{name: "tool toggled", kind: events.ToolToggled, data: map[string]any{"name": "shell", "enabled": false}, want: "58f5b36ee01e69aac1ff3203578c389f19a954d147cdfe56c795de491e387248"},
		{name: "message updated", kind: events.MessageUpdated, data: map[string]any{"id": "message-one", "patch": map[string]any{"content": "revised", "tokens": 9, "elided": true, "tool_calls": []any{}}}, want: "4180746917788bf1a8a3bda569b7cffa3e449b321d9741c458303f872b31cead"},
		{name: "messages reminted", kind: events.MessagesReminted, data: map[string]any{"reminted": []any{map[string]any{"index": 1, "from": "message-one", "to": "message-two"}}}, want: "0eaa8b77081540f366fd0ad62299eaa33ed3ff83f08b825ecf89bc4b4b9fd7ab"},
		{name: "cycle detected", kind: events.CycleDetected, want: "6d8b5ab3374b3a30bbfc7dee4ae76bd512c19e4f556a4edff10ceaa8e640c004"},
		{name: "workspace conflict", kind: events.WorkspaceConflict, want: "f40d748d32c1dedfb303570caaa6319d9b277c2885fe80ffa061f59a74a846ee"},
		{name: "compaction summary", kind: events.CompactionSummary, data: map[string]any{"dispatched": true, "usage": map[string]any{"prompt_tokens": 21, "completion_tokens": 8}}, want: "650438a7c90841726ab6a2ba23caaefb85ad43079b37b86818513c72ddd57ec9"},
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
