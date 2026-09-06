package projection

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"harness/internal/events"
)

func TestGoldenOutputBoundaryIsDeterministicAndClockFree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clock.events")
	records := []events.Event{
		{Seq: 1, TS: "2026-09-05T12:34:56.789Z", SessionID: "main", Type: events.SessionCreated, Data: map[string]any{"session": map[string]any{"id": "main", "label": "main", "run": map[string]any{"status": "idle"}, "tools": []any{}, "messages": []any{}, "budget": map[string]any{}, "runnable": true}}},
		{Seq: 2, TS: "2026-09-05T12:34:57.999Z", SessionID: "main", RunID: "r1", Type: events.Stage, Data: map[string]any{"stage": "assemble", "state": "exit", "turn": 1, "ms": 1210, "timings": map[string]any{"prompt_ms": 1000}}},
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	first, err := GoldenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := GoldenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	left, _ := MarshalGolden(first)
	right, _ := MarshalGolden(second)
	if !bytes.Equal(left, right) {
		t.Fatal("same records produced different golden bytes")
	}
	for _, forbidden := range [][]byte{[]byte("2026-09-05"), []byte("prompt_ms")} {
		if bytes.Contains(left, forbidden) {
			t.Fatalf("golden contains runtime clock value %q", forbidden)
		}
	}
	var decoded any
	if err := json.Unmarshal(left, &decoded); err != nil {
		t.Fatal(err)
	}
	if key := findClockKey(decoded); key != "" {
		t.Fatalf("golden contains runtime clock field %q", key)
	}
	if len(first.Records) != 2 || first.Records[0].EventType != events.SessionCreated || first.Records[1].EventType != events.Stage {
		t.Fatalf("record order changed: %#v", first.Records)
	}
}

func findClockKey(value any) string {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			if goldenClockKey(key) {
				return key
			}
			if found := findClockKey(child); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range current {
			if found := findClockKey(child); found != "" {
				return found
			}
		}
	}
	return ""
}
