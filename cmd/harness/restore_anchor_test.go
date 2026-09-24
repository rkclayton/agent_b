package main

import (
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
)

// Item 2fd rules 3 and 7, through a real journal: restore anchors on the newest
// user message the journal names even when that message was folded away, and a
// repeated id is re-minted once, journaled, and not re-minted on the next start.
func TestRestoreAnchorsOnTheJournalAndRemintsDuplicatesOnce(t *testing.T) {
	root, workspace := t.TempDir(), t.TempDir()
	logs := filepath.Join(root, "logs")
	cfg := config.Defaults(workspace)
	connection := &cfg.Connections[0]
	connections := func(id string) (*config.Connection, bool) { return connection, id == connection.ID }

	writers, err := events.NewWriters(logs)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	bus.SetDurableSink(writers.WriteRecord, nil, nil)
	registry := session.NewRegistry(bus, writers, connections, 40, func() config.Config { return cfg })
	item, err := registry.Create("anchor", cfg.DefaultAgentID(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	ok := true
	messages := []events.Message{
		{ID: "m-1", Role: "user", Category: "history", Content: "start"},
		{ID: "m-5", Role: "assistant", Category: "summary", Content: "note covering m-2 … m-8"},
		{ID: "m-6", Role: "assistant", Category: "history", ToolCalls: []events.ToolCall{{ID: "c1", Name: "read_file", Arguments: `{"path":"old.txt"}`}}},
		{ID: "m-7", Role: "tool", Category: "files", Name: "read_file", ToolCallID: "c1", OK: &ok, Content: "OLD BODY " + strings.Repeat("o", 400), Tokens: 120},
		{ID: "m-9", Role: "assistant", Category: "history", ToolCalls: []events.ToolCall{{ID: "c2", Name: "read_file", Arguments: `{"path":"new.txt"}`}}},
		{ID: "m-10", Role: "tool", Category: "files", Name: "read_file", ToolCallID: "c2", OK: &ok, Content: "NEW BODY " + strings.Repeat("n", 400), Tokens: 120},
		{ID: "m-11", Role: "assistant", Category: "history", Content: "done"},
		{ID: "m-5", Role: "user", Category: "history", Content: "a message that reused an id before 2es"},
	}
	for _, message := range messages {
		item.Append(message)
		bus.Publish(events.New(events.MessageAppended, item.ID, "", map[string]any{"message": message}))
	}
	// m-8 started the newest run and was later folded into the note.
	bus.Publish(events.New(events.RunStarted, item.ID, "r4", map[string]any{"run_id": "r4", "user_message_id": "m-8"}))
	if err := writers.Close(); err != nil {
		t.Fatal(err)
	}

	restart := func() ([]*session.Session, int64, []events.Event, func()) {
		t.Helper()
		next, err := events.NewWriters(logs)
		if err != nil {
			t.Fatal(err)
		}
		nextBus := events.NewBus()
		published := []events.Event{}
		nextBus.SetDurableSink(func(record events.Event) (events.LogCursor, error) {
			published = append(published, record)
			return next.WriteRecord(record)
		}, nil, nil)
		nextRegistry := session.NewRegistry(nextBus, next, connections, 40, func() config.Config { return cfg })
		restored, floor, err := restoreRetainedChats(next, nextRegistry, nextBus, retainedIDFloor(next))
		if err != nil {
			t.Fatal(err)
		}
		return restored, floor, published, func() { _ = next.Close() }
	}

	restored, floor, published, done := restart()
	if len(restored) != 1 {
		t.Fatalf("restored %d chats", len(restored))
	}
	after := restored[0].MessagesCopy()
	byIndex := func(index int) events.Message { return after[index] }
	if !byIndex(3).Elided || !strings.HasPrefix(byIndex(3).Content, "[elided: read_file") {
		t.Fatalf("m-7 is older than the journal's newest user message m-8 and must come back as a stub: %q", byIndex(3).Content)
	}
	if byIndex(5).Elided || !strings.HasPrefix(byIndex(5).Content, "NEW BODY") {
		t.Fatalf("m-10 belongs to the newest run and must stay verbatim: %q", byIndex(5).Content)
	}
	if byIndex(1).ID != "m-5" || byIndex(7).ID != "m-12" || floor != 12 {
		t.Fatalf("the later m-5 must be re-minted above the floor: first=%s later=%s floor=%d", byIndex(1).ID, byIndex(7).ID, floor)
	}
	notes := 0
	for _, event := range published {
		if event.Type == events.MessagesReminted {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("the re-mint must be journaled once, got %d notes", notes)
	}
	done()

	restored, _, published, done = restart()
	defer done()
	again := restored[0].MessagesCopy()
	if again[7].ID != "m-12" || again[1].ID != "m-5" {
		t.Fatalf("the journal must carry the re-mint: %s %s", again[1].ID, again[7].ID)
	}
	for _, event := range published {
		if event.Type == events.MessagesReminted {
			t.Fatal("a second start re-minted again")
		}
	}
}
