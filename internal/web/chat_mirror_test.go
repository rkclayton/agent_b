package web

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/events"
	"harness/internal/projection"
)

func mirrorRequest(t *testing.T, server *Server, id, route string, body any, want int) appResponseUnit {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"v": 1, "kind": "request", "id": id, "route": route, "body": body})
	if err != nil {
		t.Fatal(err)
	}
	response := server.DispatchAppMessage("broker:phone", encoded)
	var unit appResponseUnit
	if err := json.Unmarshal(response, &unit); err != nil {
		t.Fatalf("response %s: %v", response, err)
	}
	if unit.Status != want {
		t.Fatalf("%s = %d %s, want %d", route, unit.Status, unit.Body, want)
	}
	return unit
}

func TestLatestChatActivityCountsOnlyTurns2rv(t *testing.T) {
	snapshot := projection.Snapshot{CreatedAt: "2026-10-07T10:00:00Z", Timeline: []events.Event{
		{Type: events.MessageAppended, TS: "2026-10-07T11:00:00Z", Data: map[string]any{"message": map[string]any{"role": "user"}}},
		{Type: events.SessionRenamed, TS: "2026-10-07T12:00:00Z"},
		{Type: events.MessageAppended, TS: "2026-10-07T13:00:00Z", Data: map[string]any{"message": events.Message{Role: "assistant"}}},
		{Type: events.RunStopped, TS: "2026-10-07T14:00:00Z"},
	}}
	if got := latestChatActivity(snapshot); got != "2026-10-07T13:00:00Z" {
		t.Fatalf("activity=%s", got)
	}
	if got := latestChatActivity(projection.Snapshot{CreatedAt: snapshot.CreatedAt}); got != "" {
		t.Fatalf("empty chat activity=%s", got)
	}
}

func mirroredSeed(t *testing.T, server *Server, chatID string) map[string]any {
	t.Helper()
	cfg := server.ConfigSnapshot()
	return map[string]any{
		"id": chatID, "label": "Phone scan", "agent_id": cfg.DefaultAgentID(),
		"created_at": time.Now().UTC().Format(time.RFC3339Nano), "scratch": true,
		"run":   map[string]any{"status": "idle", "max_turns": cfg.Run.MaxTurns},
		"tools": []any{}, "messages": []any{}, "runnable": true,
	}
}

func mirroredEvent(chatID, kind string, data any) map[string]any {
	return map[string]any{"ts": time.Now().UTC().Format(time.RFC3339Nano), "run_id": "", "type": kind, "data": data}
}

func TestMirrorAppliesFiveEventsFilesAndGapRecovery2oo(t *testing.T) {
	server, registry, writers, _, _, _ := consoleServer(t)
	defer writers.Close()
	server.bus.SetSink(nil)
	server.bus.SetDurableSink(writers.WriteRecord, server.projector.Apply, server.projector.MarkStale)
	chatID := "phone-chat"
	sidecar := []byte("the photographed serial is ALPHA-17")
	digest := sha256.Sum256(sidecar)
	imageDigest := sha256.Sum256([]byte("jpg"))
	imageHash := hex.EncodeToString(imageDigest[:])
	appends := []map[string]any{
		{"chat_id": chatID, "origin": "phone", "owner": "phone", "seq": 1, "event": mirroredEvent(chatID, events.SessionCreated, map[string]any{"session": mirroredSeed(t, server, chatID)})},
		{"chat_id": chatID, "origin": "phone", "owner": "phone", "seq": 2, "event": mirroredEvent(chatID, events.MessageAppended, map[string]any{"message": map[string]any{"id": "m1", "role": "user", "category": "history", "content": "Read this photo", "attachments": []map[string]any{{"path": "attachments/photo.jpg", "bytes": 3, "sha256": imageHash}}}}), "files": []map[string]any{{"path": "attachments/photo.jpg", "bytes": 3, "sha256": imageHash, "content": base64.RawURLEncoding.EncodeToString([]byte("jpg"))}, {"path": "attachments/photo.jpg.ocr.txt", "bytes": len(sidecar), "sha256": hex.EncodeToString(digest[:]), "content": base64.RawURLEncoding.EncodeToString(sidecar)}}},
		{"chat_id": chatID, "origin": "phone", "owner": "phone", "seq": 3, "event": mirroredEvent(chatID, events.RunStarted, map[string]any{"run_id": "r1"})},
		{"chat_id": chatID, "origin": "phone", "owner": "phone", "seq": 4, "event": mirroredEvent(chatID, events.MessageAppended, map[string]any{"message": map[string]any{"id": "m2", "role": "assistant", "category": "history", "content": "I can read it."}})},
		{"chat_id": chatID, "origin": "phone", "owner": "phone", "seq": 5, "event": mirroredEvent(chatID, events.RunStopped, map[string]any{"reason": "done"})},
	}
	for index := 0; index < 3; index++ {
		mirrorRequest(t, server, string(rune('a'+index)), "chat.mirror", appends[index], http.StatusOK)
	}
	// The disconnected owner's gap is refused; since names the durable recovery cursor.
	mirrorRequest(t, server, "gap", "chat.mirror", appends[4], http.StatusConflict)
	since := mirrorRequest(t, server, "since", "chat.mirror.since", map[string]any{"chat_id": chatID}, http.StatusOK)
	if !strings.Contains(string(since.Body), `"last_seq":3`) {
		t.Fatalf("since = %s", since.Body)
	}
	for index := 3; index < 5; index++ {
		mirrorRequest(t, server, string(rune('d'+index)), "chat.mirror", appends[index], http.StatusOK)
	}
	// A byte-identical retry is idempotent and does not create a sixth record.
	mirrorRequest(t, server, "retry", "chat.mirror", appends[4], http.StatusOK)
	item, ok := registry.Get(chatID)
	if !ok {
		t.Fatal("mirrored chat was not registered")
	}
	if item.Snapshot().Owner != "phone" || item.Snapshot().Origin != "phone" {
		t.Fatalf("mirror fields = %+v", item.Snapshot())
	}
	read, err := os.ReadFile(filepath.Join(item.Snapshot().WorkspaceDir, "attachments", "photo.jpg.ocr.txt"))
	if err != nil || string(read) != string(sidecar) {
		t.Fatalf("sidecar = %q, %v", read, err)
	}
	if err := server.takeMirror(chatID, 5, "pc"); err != nil {
		t.Fatal(err)
	}
	device := &recordingDevice{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { server.streamToDevice(ctx, device); close(done) }()
	device.waitFor(t, "the mirrored chat snapshot", func(unit map[string]any) bool { return unit["kind"] == "snapshot" && unit["session_id"] == chatID })
	server.bus.Publish(events.New(events.MessageAppended, chatID, "r2", map[string]any{"message": map[string]any{"id": "m3", "role": "assistant", "category": "history", "content": "PC answer"}}))
	device.waitFor(t, "the PC owner's mirrored answer", func(unit map[string]any) bool { return unit["kind"] == "request" && unit["route"] == "chat.mirror" })
	cancel()
	<-done
}

func TestMirrorRefusesNonOwnerAndTakeDuringTurn2oo(t *testing.T) {
	server, _, writers, _, _, _ := consoleServer(t)
	defer writers.Close()
	server.bus.SetSink(nil)
	server.bus.SetDurableSink(writers.WriteRecord, server.projector.Apply, server.projector.MarkStale)
	chatID := "phone-busy"
	seed := map[string]any{"chat_id": chatID, "origin": "phone", "owner": "phone", "seq": 1, "event": mirroredEvent(chatID, events.SessionCreated, map[string]any{"session": mirroredSeed(t, server, chatID)})}
	mirrorRequest(t, server, "seed", "chat.mirror", seed, http.StatusOK)
	nonOwner := map[string]any{"chat_id": chatID, "origin": "phone", "owner": "pc", "seq": 2, "event": mirroredEvent(chatID, events.MessageAppended, map[string]any{})}
	mirrorRequest(t, server, "wrong", "chat.mirror", nonOwner, http.StatusConflict)
	started := map[string]any{"chat_id": chatID, "origin": "phone", "owner": "phone", "seq": 2, "event": mirroredEvent(chatID, events.RunStarted, map[string]any{"run_id": "r1"})}
	mirrorRequest(t, server, "run", "chat.mirror", started, http.StatusOK)
	if err := server.takeMirror(chatID, 2, "pc"); err == nil || err.Error() != "the phone is mid-turn" {
		t.Fatalf("take = %v", err)
	}
}
