package projection

import (
	"path/filepath"
	"testing"
	"time"

	"harness/internal/events"
)

func TestStoreCutsSnapshotBeforeLaterProjectionPatches(t *testing.T) {
	writers, err := events.NewWriters(filepath.Join(t.TempDir(), "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	if _, err := writers.OpenSession("main"); err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	bus := events.NewBus()
	bus.SetDurableSink(writers.WriteRecord, store.Apply, store.MarkStale)
	bus.Publish(events.New(events.SessionCreated, "main", "", map[string]any{"session": map[string]any{"id": "main", "label": "main", "run": map[string]any{"status": "idle"}, "tools": []any{}, "messages": []any{}}}))
	snapshot, patches, unsubscribe, err := store.SubscribeSnapshot(writers.SessionCursors())
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	cut := snapshot["main"].Cursor
	bus.Publish(events.New(events.MessageQueued, "main", "r1", map[string]any{"position": 1}))
	select {
	case patch := <-patches:
		if patch.PreviousCursor != cut || patch.Cursor.Offset <= cut.Offset {
			t.Fatalf("cut=%+v patch=%+v", cut, patch)
		}
	case <-time.After(time.Second):
		t.Fatal("projection patch missing")
	}
}

func TestStoreDoesNotAdvanceAcrossAppendFailure(t *testing.T) {
	store := NewStore()
	store.states["main"] = Empty("main")
	store.initialized["main"] = true
	store.MarkStale(events.New(events.ToolResult, "main", "r1", nil), assertError("disk full"))
	if store.states["main"].Cursor != (Cursor{}) {
		t.Fatal("cursor advanced across failure")
	}
}

type assertError string

func (e assertError) Error() string { return string(e) }
