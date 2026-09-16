package projection

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"harness/internal/events"
)

func TestLoadReplayUsesProjectionAndKeepsHistoricalStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.jsonl")
	values := []events.Event{
		{TS: "2026-09-05T00:00:00Z", SessionID: "main", Type: events.SessionCreated, Data: map[string]any{"session": map[string]any{"id": "main", "label": "main", "run": map[string]any{"status": "idle"}, "tools": []any{}, "messages": []any{}}}},
		{TS: "2026-09-05T00:00:01Z", SessionID: "main", RunID: "r1", Type: events.RunStarted, Data: map[string]any{"run_id": "r1"}},
		{TS: "2026-09-05T00:00:02Z", SessionID: "main", RunID: "r1", Type: events.RunStopped, Data: map[string]any{"reason": "done"}},
		{TS: "2026-09-05T00:00:03Z", SessionID: "main", Type: "todos.updated", Data: map[string]any{"ignored": true}},
	}
	var encoded []byte
	for _, value := range values {
		line, _ := json.Marshal(value)
		encoded = append(encoded, line...)
		encoded = append(encoded, '\n')
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	replay, err := LoadReplay([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	state := replay.Sessions["main"]
	if state.Run.Status != "idle" || state.Run.RunID != "" || state.Run.Turn != 0 || state.Run.LastStopReason != "done" {
		t.Fatalf("historical run projection = %#v", state.Run)
	}
	if len(state.Timeline) != 4 || len(replay.Patches) != 4 {
		t.Fatalf("timeline=%d patches=%d", len(state.Timeline), len(replay.Patches))
	}
}

func TestResetGenerationFollowsExplicitPredecessor(t *testing.T) {
	dir := t.TempDir()
	previousPath := filepath.Join(dir, "main-old.jsonl")
	created := events.Event{TS: "2026-09-05T00:00:00Z", SessionID: "main", Type: events.SessionCreated, Data: map[string]any{"session": map[string]any{"id": "main", "label": "kept", "run": map[string]any{"status": "idle", "max_turns": 40}, "tools": []any{}, "messages": []any{}}}}
	line, _ := json.Marshal(created)
	line = append(line, '\n')
	if err := os.WriteFile(previousPath, line, 0o600); err != nil {
		t.Fatal(err)
	}
	currentPath := filepath.Join(dir, "main-new.jsonl")
	reset := events.Event{TS: "2026-09-05T00:01:00Z", SessionID: "main", Type: events.SessionReset, Data: map[string]any{"predecessor": map[string]any{"generation": filepath.Base(previousPath), "offset": len(line)}}}
	current, _ := json.Marshal(reset)
	current = append(current, '\n')
	if err := os.WriteFile(currentPath, current, 0o600); err != nil {
		t.Fatal(err)
	}
	state, _, err := ProjectFile(currentPath, int64(len(current)))
	if err != nil {
		t.Fatal(err)
	}
	if !state.Complete || state.Label != "kept" || state.Cursor.Generation != filepath.Base(currentPath) {
		t.Fatalf("state=%#v", state)
	}
}

func TestReplayArchiveStaysOutsideProjectedMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.jsonl")
	ok := true
	values := []events.Event{
		{TS: "2026-09-05T00:00:00Z", SessionID: "main", Type: events.SessionCreated, Data: map[string]any{"session": map[string]any{"id": "main", "run": map[string]any{"status": "idle"}, "tools": []any{}, "messages": []any{}}}},
		{TS: "2026-09-05T00:00:01Z", SessionID: "main", Type: events.MessageAppended, Data: map[string]any{"message": events.Message{ID: "m1", Role: "tool", Content: "full body", OK: &ok}}},
		{TS: "2026-09-05T00:00:02Z", SessionID: "main", Type: events.Compaction, Data: map[string]any{"kind": "summarize", "summary_message_id": "s1", "affected_ids": []string{"m1"}}},
	}
	var encoded []byte
	for _, value := range values {
		line, _ := json.Marshal(value)
		encoded = append(encoded, line...)
		encoded = append(encoded, '\n')
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	replay, err := LoadReplay([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Sessions["main"].Messages) != 0 {
		t.Fatal("archive body leaked into projected messages")
	}
	lookup, err := replay.Archives["main"].Resolve("m1")
	if err != nil || lookup.Message.Content != "full body" {
		t.Fatalf("lookup=%+v err=%v", lookup, err)
	}
}
