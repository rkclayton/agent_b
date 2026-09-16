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

// c-w2's sequence, reproduced: an event is appended to the session's JSONL, a
// plain /api/state snapshot is taken before the store folds that append, and
// then the fold arrives. Every connected client must still receive the patch.
func TestPolledSnapshotBetweenAppendAndFoldDoesNotSilenceThePatch(t *testing.T) {
	writers, err := events.NewWriters(filepath.Join(t.TempDir(), "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	if _, err := writers.OpenSession("main"); err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	created := events.New(events.SessionCreated, "main", "", map[string]any{"session": map[string]any{"id": "main", "label": "main", "run": map[string]any{"status": "idle"}, "tools": []any{}, "messages": []any{}}})
	created.Seq = 1
	cursor, err := writers.WriteRecord(created)
	if err != nil {
		t.Fatal(err)
	}
	store.Apply(created, cursor)
	_, patches, unsubscribe, err := store.SubscribeSnapshot(writers.SessionCursors())
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()

	queued := events.New(events.MessageQueued, "main", "r1", map[string]any{"position": 1})
	queued.Seq = 2
	appended, err := writers.WriteRecord(queued)
	if err != nil {
		t.Fatal(err)
	}
	// The poll lands between the append and its fold.
	polled, err := store.Snapshot(writers.SessionCursors())
	if err != nil {
		t.Fatal(err)
	}
	if polled["main"].Cursor.Offset != appended.Offset {
		t.Fatalf("the poll did not see the appended record: %+v vs %+v", polled["main"].Cursor, appended)
	}
	store.Apply(queued, appended)
	select {
	case patch := <-patches:
		if patch.Cursor.Offset != appended.Offset {
			t.Fatalf("patch cursor = %+v, want %+v", patch.Cursor, appended)
		}
	case <-time.After(time.Second):
		t.Fatal("the polled snapshot silenced the patch: no client received message.queued")
	}
}

// A run observed while a client keeps polling /api/state: every event the run
// appends reaches the subscriber as a patch, in order, with no gap in cursors.
func TestEveryPatchArrivesWhileAClientPollsDuringARun(t *testing.T) {
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

	stop := make(chan struct{})
	polled := make(chan int, 1)
	go func() {
		count := 0
		for {
			select {
			case <-stop:
				polled <- count
				return
			default:
				if _, err := store.Snapshot(writers.SessionCursors()); err == nil {
					count++
				}
			}
		}
	}()
	const published = 200
	go func() {
		for index := 0; index < published; index++ {
			bus.Publish(events.New(events.MessageQueued, "main", "r1", map[string]any{"position": index + 1}))
		}
	}()
	previous := snapshot["main"].Cursor
	for received := 0; received < published; received++ {
		select {
		case patch, ok := <-patches:
			if !ok {
				t.Fatalf("subscriber dropped after %d patches", received)
			}
			if patch.PreviousCursor != previous {
				t.Fatalf("patch %d skipped a record: previous=%+v want %+v", received, patch.PreviousCursor, previous)
			}
			previous = patch.Cursor
		case <-time.After(5 * time.Second):
			close(stop)
			t.Fatalf("only %d of %d patches arrived while polling (%d polls)", received, published, <-polled)
		}
	}
	close(stop)
	if count := <-polled; count == 0 {
		t.Fatal("the poller never took a snapshot during the run")
	}
}
