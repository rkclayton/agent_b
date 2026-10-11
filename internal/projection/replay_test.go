package projection

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"harness/internal/events"
	"harness/internal/tools"
)

func encodeEvents(values ...events.Event) (body []byte) {
	for _, value := range values {
		line, _ := json.Marshal(value)
		body = append(body, append(line, '\n')...)
	}
	return
}

func TestLoadReplayUsesProjectionAndKeepsHistoricalStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.jsonl")
	values := []events.Event{
		{TS: "2026-09-05T00:00:00Z", SessionID: "main", Type: events.SessionCreated, Data: map[string]any{"session": map[string]any{"id": "main", "label": "main", "run": map[string]any{"status": "idle"}, "tools": []any{}, "messages": []any{}}}},
		{TS: "2026-09-05T00:00:01Z", SessionID: "main", RunID: "r1", Type: events.RunStarted, Data: map[string]any{"run_id": "r1"}},
		{TS: "2026-09-05T00:00:02Z", SessionID: "main", RunID: "r1", Type: events.RunStopped, Data: map[string]any{"reason": "done"}},
		{TS: "2026-09-05T00:00:03Z", SessionID: "main", Type: "todos.updated", Data: map[string]any{"ignored": true}},
	}
	encoded := encodeEvents(values...)
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
	encoded := encodeEvents(values...)
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

func TestSlimJournalKeepsProjectionAndInterruptedTurn2td(t *testing.T) {
	root := t.TempDir()
	event := func(seq int64, run, kind string, data any) events.Event {
		return events.Event{Seq: seq, TS: "2026-10-11T00:00:00Z", SessionID: "main", RunID: run, Type: kind, Data: data}
	}
	write := func(name string, body []byte) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	request := event(1, "r1", events.ModelRequest, map[string]any{"turn": 1})
	request.Body = strings.Repeat("request", 500)
	response := event(4, "r1", events.ModelResponse, map[string]any{"turn": 1, "content": "done"})
	response.Raw = strings.Repeat("wire", 4000)
	base := encodeEvents(request,
		event(2, "r1", events.ModelDelta, map[string]any{"turn": 1, "kind": "reasoning", "text": "thought"}),
		event(3, "r1", events.ModelDelta, map[string]any{"turn": 1, "kind": "content", "text": "done"}), response,
		event(5, "r1", events.RunStopped, map[string]any{"reason": "done"}),
		event(6, "r2", events.ModelRequest, map[string]any{"turn": 2}))
	cutoff := encodeEvents(event(7, "r2", events.ModelDelta, map[string]any{"turn": 2, "kind": "content", "text": "partial"}))
	path, pending := write("main.jsonl", base), write("pending.jsonl", cutoff)
	before, _ := ProjectFileState(write("combined.jsonl", append(append([]byte{}, base...), cutoff...)), 0)
	if changed, err := SlimJournal(path, pending); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	after, _ := ProjectFileState(path, 0)
	before.Cursor, after.Cursor = Cursor{}, Cursor{}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("projection changed")
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), `"body"`) || strings.Contains(string(body), `"raw"`) || strings.Count(string(body), `"type":"model.delta"`) != 1 || len(body)*5 > len(base)+len(cutoff) {
		t.Fatal("slim journal contents")
	}
	t.Logf("full=%d slim=%d", len(base)+len(cutoff), len(body))
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatal("pending remained")
	}
	if changed, err := SlimJournal(path, pending); err != nil || changed {
		t.Fatal("second start changed")
	}
	unchanged := func(name string, original []byte, run func(string) error) {
		t.Run(name, func(t *testing.T) {
			candidate := write(name+".jsonl", original)
			if err := run(candidate); err == nil {
				t.Fatal("rewrite passed")
			}
			got, _ := os.ReadFile(candidate)
			if !bytes.Equal(original, got) {
				t.Fatal("journal changed")
			}
		})
	}
	unchanged("damaged", append(append([]byte{}, base...), []byte("{damaged")...), func(path string) error { _, err := SlimJournal(path, ""); return err })
	unchanged("projection differs", base, func(path string) error {
		_, err := slimJournal(path, "", func(records []Record) { records[len(records)-1].Event.Type = "planted.different" }, tools.AtomicReplace)
		return err
	})
	unchanged("killed before replace", base, func(path string) error {
		_, err := slimJournal(path, "", nil, func(string, string) error { return errors.New("planted kill") })
		return err
	})
}
