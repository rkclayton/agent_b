package web

import (
	"fmt"
	"path/filepath"
	"testing"

	"harness/internal/events"
	"harness/internal/projection"
)

// Item 2pv (a): WHAT THE PAGE IS SENT AT THE START, in bytes, against what its first
// screen shows. A measurement, not a gate: one logged row per size. Always-run, about
// twenty-five seconds. What a phone is sent is measured through the public broker by
// TestLivePhoneJoinBytes2pv in internal/broker, which is opt-in.
func TestFirstAnswerBytesPage2pv(t *testing.T) {
	for _, size := range []struct {
		name          string
		chats, events int
	}{{"small", 3, 300}, {"large", 30, 3000}} {
		server := measuredServer(t, size.chats, size.events)
		// The page asks twice at the start: /api/events opens with this snapshot frame,
		// and /api/state answers from the live cut the subscription initialized.
		sessions, _, unsubscribe, err := server.projector.SubscribeSnapshot(server.projector.Sources())
		if err != nil {
			t.Fatal(err)
		}
		unsubscribe()
		events0 := jsonSize(t, events.New(events.Snapshot, "", "", server.snapshotWithSessions(sessions, false)))
		page := jsonSize(t, server.snapshot())
		// The first screen: the chat list (every chat's own fields) and the selected
		// chat's newest screenful. Fifty entries is more than one screen shows.
		firstScreen := make(map[string]projection.Snapshot, len(sessions))
		for id, snapshot := range sessions {
			keep := 0
			if id == "chat-0" {
				keep = 50
			}
			snapshot.Timeline = snapshot.Timeline[len(snapshot.Timeline)-min(keep, len(snapshot.Timeline)):]
			snapshot.Chat = snapshot.Chat[len(snapshot.Chat)-min(keep, len(snapshot.Chat)):]
			snapshot.Messages = snapshot.Messages[len(snapshot.Messages)-min(keep, len(snapshot.Messages)):]
			firstScreen[id] = snapshot
		}
		needed := jsonSize(t, server.snapshotWithSessions(firstScreen, false))
		t.Logf("2pv page %-5s chats=%d chat_entries=%d state_bytes=%d events_first_frame_bytes=%d first_screen_bytes=%d (%.2f%%)", size.name, len(sessions), len(sessions["chat-0"].Chat), page, events0, needed, 100*float64(needed)/float64(page))
	}
}

// measuredServer holds chats of entries messages each, the user and the agent taking
// turns, projected as the instance projects its journals.
func measuredServer(t *testing.T, chats, entries int) *Server {
	t.Helper()
	writers, err := events.NewWriters(filepath.Join(t.TempDir(), "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	store, bus := projection.NewStore(), events.NewBus()
	bus.SetDurableSink(writers.WriteRecord, store.Apply, store.MarkStale)
	for chat := 0; chat < chats; chat++ {
		id := fmt.Sprintf("chat-%d", chat)
		if _, err := writers.OpenSession(id); err != nil {
			t.Fatal(err)
		}
		bus.Publish(events.New(events.SessionCreated, id, "", map[string]any{"session": map[string]any{"id": id, "label": id, "run": map[string]any{"status": "idle"}, "tools": []any{}, "messages": []any{}}}))
		for index := 0; index < entries; index++ {
			bus.Publish(events.New(events.MessageAppended, id, "run", map[string]any{"message": map[string]any{
				"id": fmt.Sprintf("m%d", index), "role": []string{"user", "assistant"}[index%2], "content": "fixture"}}))
		}
	}
	root := t.TempDir()
	cfg := sizedConfig(root, 1)
	server := New(&cfg, filepath.Join(root, "harness.json"), t.TempDir(), RuntimeRoots{Application: t.TempDir(), Data: t.TempDir(), Profile: t.TempDir(), Workspace: root}, bus)
	server.SetProjection(store, writers)
	return server
}
