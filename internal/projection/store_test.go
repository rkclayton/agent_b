package projection

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"harness/internal/events"
)

func TestSnapshotReadViewKeepsBytesAndAtomicLiveEvents2pr(t *testing.T) {
	state := Empty("main")
	state.Cursor = Cursor{Generation: "main.jsonl", Offset: 1}
	state.Chat = []ChatEntry{{Type: "agent", Key: "turn:run:1", RunID: "run", Turn: 1}}
	state.Timeline = []events.Event{events.New(events.ModelRequest, "main", "run", map[string]any{"turn": 1})}

	before, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(SnapshotForRead(state))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("read view changed snapshot bytes")
	}

	store := NewStore()
	store.states["main"], store.initialized["main"] = state, true
	store.sources["main"] = events.LogCursor{Generation: "main.jsonl", Offset: 1}
	const count = 200
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		for index := 0; index < count; index++ {
			event := events.New(events.ModelDelta, "main", "run", map[string]any{"turn": 1, "kind": "content", "text": "x"})
			store.Apply(event, events.LogCursor{Generation: "main.jsonl", Offset: int64(index + 2)})
		}
	}()
	for index := 0; index < count; index++ {
		current := store.CurrentSnapshot()["main"]
		if _, err := json.Marshal(current); err != nil {
			t.Fatal(err)
		}
		if len(current.Chat) != 1 || strings.Trim(current.Chat[0].Text, "x") != "" {
			t.Fatalf("reader saw partial event: %+v", current.Chat)
		}
	}
	group.Wait()
}

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
