package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/events"
	"harness/internal/projection"
)

func TestLargeStoreFirstAnswersAreBounded2qc(t *testing.T) {
	server := measuredServer(t, 30, 3000)
	state := httptest.NewRecorder()
	server.state(state, httptest.NewRequest(http.MethodGet, "/api/state?session=chat-0", nil))
	if got := state.Body.Len(); got > 1<<20 {
		t.Fatalf("/api/state = %d bytes, want <= %d", got, 1<<20)
	}
	opening := jsonSize(t, events.New(events.Snapshot, "", "", server.openingSnapshot(server.projector.CurrentSnapshot(), false)))
	if opening > 64<<10 {
		t.Fatalf("opening events frame = %d bytes, want <= %d", opening, 64<<10)
	}
}

func TestLargeStorePhoneJoinAndReconnectAreBounded2qc(t *testing.T) {
	server := measuredServer(t, 30, 3000)
	resume := map[string]projection.Snapshot{}
	join, joinKinds := streamInitialUnits(t, server, 30, resume)
	if join > 1<<20 {
		t.Fatalf("phone join = %d bytes, want <= %d", join, 1<<20)
	}
	if strings.Join(joinKinds, ",") != strings.Repeat("snapshot,", 29)+"snapshot" {
		t.Fatalf("join kinds = %v", joinKinds)
	}
	for index := 0; index < 10; index++ {
		server.bus.Publish(events.New(events.MessageAppended, "chat-0", "run", map[string]any{"message": map[string]any{
			"id": fmt.Sprintf("new-%d", index), "role": "assistant", "content": "new entry",
		}}))
	}
	reconnect, reconnectKinds := streamInitialUnits(t, server, 10, resume)
	if reconnect > 64<<10 {
		t.Fatalf("phone reconnect = %d bytes, want <= %d", reconnect, 64<<10)
	}
	for _, kind := range reconnectKinds {
		if kind != "patch" {
			t.Fatalf("reconnect resent %q instead of changes only", kind)
		}
	}
}

func TestLargeStorePageHealthStaysWithinBudget2qc(t *testing.T) {
	server := measuredServer(t, 30, 3000)
	state := httptest.NewRecorder()
	server.state(state, httptest.NewRequest(http.MethodGet, "/api/state?session=chat-0", nil))
	health := httptest.NewRecorder()
	server.pageHealth(health, httptest.NewRequest(http.MethodPost, "/api/page-health", strings.NewReader(`{"freeze_ms":499,"settings_pages":{}}`)))
	if health.Code != http.StatusNoContent {
		t.Fatalf("page health status = %d", health.Code)
	}
	got := map[string]map[string]any{}
	server.app.SetSink(func(kind string, data map[string]any) { got[kind] = data })
	server.app.SendNow()
	page := got[events.PageHealth]
	if page["state_bytes"].(int64) > 1<<20 || page["longest_freeze_ms"].(int) > 500 {
		t.Fatalf("page.health = %#v", page)
	}
}

func TestThreeThousandEntryChatPagesBackToItsFirstEntry2qc(t *testing.T) {
	server := measuredServer(t, 1, 3000)
	total := len(server.projector.CurrentSnapshot()["chat-0"].Chat)
	before, seen := total, 0
	for before > 0 {
		response := httptest.NewRecorder()
		server.session(response, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/sessions/chat-0/history?before=%d", before), nil))
		if response.Code != http.StatusOK {
			t.Fatalf("history at %d: %d %s", before, response.Code, response.Body.String())
		}
		var page struct {
			Start, Before, Total int
			Chat                 []projection.ChatEntry
		}
		if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
			t.Fatal(err)
		}
		if len(page.Chat) != min(50, before) || page.Before != before || page.Total != total {
			t.Fatalf("page at %d = start %d before %d total %d rows %d", before, page.Start, page.Before, page.Total, len(page.Chat))
		}
		seen += len(page.Chat)
		before = page.Start
	}
	if seen != total || total != 1500 {
		t.Fatalf("paged entries = %d, projected total = %d, want 1500 rows from 3000 message events", seen, total)
	}
}

func streamInitialUnits(t *testing.T, server *Server, want int, resume map[string]projection.Snapshot) (int, []string) {
	t.Helper()
	device := &recordingDevice{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { server.streamUnitsToDeviceResuming(ctx, device, resume); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		device.mu.Lock()
		count := len(device.units)
		device.mu.Unlock()
		if count >= want {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	device.mu.Lock()
	defer device.mu.Unlock()
	if len(device.units) < want {
		t.Fatalf("phone got %d initial units, want at least %d", len(device.units), want)
	}
	total, kinds := 0, make([]string, 0, len(device.units))
	for _, unit := range device.units {
		encoded, err := appCanonical(unit)
		if err != nil {
			t.Fatal(err)
		}
		total += len(encoded)
		kinds = append(kinds, fmt.Sprint(unit["kind"]))
	}
	return total, kinds
}

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
	if _, err := store.Snapshot(writers.SessionCursors()); err != nil {
		t.Fatal(err)
	}
	return server
}
