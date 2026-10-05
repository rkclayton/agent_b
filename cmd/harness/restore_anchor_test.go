package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/projection"
	"harness/internal/session"
)

func TestSixtyFourChatStartupReadsSmallStateWithinTwoSeconds2qc(t *testing.T) {
	root := t.TempDir()
	chats := filepath.Join(root, "chats")
	if err := os.MkdirAll(chats, 0o700); err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, 64)
	for index := 0; index < 64; index++ {
		id := fmt.Sprintf("s%d", index+1)
		path := filepath.Join(chats, id+".jsonl")
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		writer := bufio.NewWriterSize(file, 1<<20)
		seed, _ := json.Marshal(events.New(events.SessionCreated, id, "", map[string]any{"session": map[string]any{
			"id": id, "label": id, "created_at": time.Unix(int64(index+1), 0).UTC().Format(time.RFC3339Nano),
			"run": map[string]any{"status": "idle"}, "tools": []any{}, "messages": []any{},
		}}))
		if _, err := writer.Write(append(seed, '\n')); err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			large, _ := json.Marshal(events.New(events.Stage, id, "r1", map[string]any{"stage": "assemble", "state": "exit", "turn": 1, "padding": strings.Repeat("x", 8192)}))
			line := append(large, '\n')
			for written := int64(len(seed) + 1); written < 100<<20; written += int64(len(line)) {
				if _, err := writer.Write(line); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := writer.Flush(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	writers, err := events.NewWriters(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	defer writers.Close()
	if _, _, _, err := restoreProjections(writers, paths); err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 3; run++ {
		started := time.Now()
		sessions, _, hits, err := restoreProjections(writers, paths)
		elapsed := time.Since(started)
		t.Logf("2qc startup run %d: chats=%d cache_hits=%d listen_ready_ms=%d", run, len(sessions), hits, elapsed.Milliseconds())
		if err != nil {
			t.Fatal(err)
		}
		if len(sessions) != 64 || hits != 64 {
			t.Fatalf("run %d: chats=%d hits=%d", run, len(sessions), hits)
		}
		if elapsed > 2*time.Second {
			t.Fatalf("run %d took %v, want <= 2s", run, elapsed)
		}
	}
}

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
		// Item 2m5: the id floor is taken from the projections the restore already
		// has, so there is no second pass to seed it with; retainedIDFloor is gone.
		restored, floor, err := restoreRetainedChats(next, nextRegistry, nextBus, 0)
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

// Item 2m5 (b) and (f): A CHAT AT THE OPERATOR'S SCALE IS PROJECTED ONCE, AND THE
// LAUNCH AFTER THAT READS ITS PROJECTION.
//
// His `chats/` is 215 MB across 34 files, two of them 56 MB and 48 MB, and startup
// was spending 6,797 ms of 8,513 ms projecting all of it — measured on a copy at
// 6.244s, against 45ms to decode the 6.7 MB of state those journals project to.
// This builds one journal at that shape — many turns, large tool bodies — restores
// it twice, and requires the second restore to read the cache and to produce the
// same transcript, byte for byte, as the first.
func TestASecondLaunchReadsTheProjectionInsteadOfTheJournal2m5(t *testing.T) {
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
	item, err := registry.Create("scale", cfg.DefaultAgentID(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	// A journal shaped like HIS: the bulk is streamed model deltas, thousands of
	// them per chat — s2 alone holds 3,483 — each a record in the journal and none
	// of them in the projection, which keeps one agent entry per turn. That is why
	// his 56 MB chat projects to 1.79 MB, and why reading the projection is the fix.
	for turn := 1; turn <= 40; turn++ {
		user := events.Message{ID: fmt.Sprintf("m-%d", turn*2), Role: "user", Category: "history", Turn: turn, Content: fmt.Sprintf("turn %d", turn)}
		item.Append(user)
		bus.Publish(events.New(events.MessageAppended, item.ID, "", map[string]any{"message": user}))
		runID := fmt.Sprintf("r%d", turn)
		bus.Publish(events.New(events.RunStarted, item.ID, runID, map[string]any{"run_id": runID, "user_message_id": user.ID}))
		for index := 0; index < 400; index++ {
			bus.Publish(events.New(events.ModelDelta, item.ID, runID, map[string]any{"turn": turn, "kind": "content", "index": 0, "text": strings.Repeat("word ", 20)}))
		}
		answer := events.Message{ID: fmt.Sprintf("m-%d", turn*2+1), Role: "assistant", Category: "history", Turn: turn, Content: strings.Repeat("word ", 40)}
		item.Append(answer)
		bus.Publish(events.New(events.MessageAppended, item.ID, "", map[string]any{"message": answer}))
		bus.Publish(events.New(events.RunStopped, item.ID, runID, map[string]any{"run_id": runID, "reason": "done", "turns": 1}))
	}
	if err := writers.Close(); err != nil {
		t.Fatal(err)
	}

	restore := func() (*session.Session, string) {
		t.Helper()
		next, err := events.NewWriters(logs)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = next.Close() }()
		nextBus := events.NewBus()
		nextBus.SetDurableSink(next.WriteRecord, nil, nil)
		nextRegistry := session.NewRegistry(nextBus, next, connections, 40, func() config.Config { return cfg })
		restored, _, err := restoreRetainedChats(next, nextRegistry, nextBus, 0)
		if err != nil || len(restored) != 1 {
			t.Fatalf("restore: %d chats, err %v", len(restored), err)
		}
		return restored[0], next.ProjectionCacheDir()
	}

	first, cacheDir := restore()
	firstMessages := first.MessagesCopy()
	entry := projection.CacheEntryPath(cacheDir, filepath.Join(root, "chats", first.ID+".jsonl"))
	if _, err := os.Stat(entry); err != nil {
		t.Fatalf("the first restore wrote no projection for %s: %v", first.ID, err)
	}
	journal, err := os.Stat(filepath.Join(root, "chats", first.ID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	cached, err := os.Stat(entry)
	if err != nil {
		t.Fatal(err)
	}
	if cached.Size() >= journal.Size() {
		t.Fatalf("the projection (%d bytes) is not smaller than the journal (%d bytes), which is the whole reason to read it", cached.Size(), journal.Size())
	}

	// (c): nothing under chats/ was rewritten by any of this.
	before, err := os.ReadFile(filepath.Join(root, "chats", first.ID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	second, _ := restore()
	after, err := os.ReadFile(filepath.Join(root, "chats", first.ID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// (c): NOTHING IS REWRITTEN, SPLIT OR DELETED. A restore does APPEND its own
	// record to the durable journal — it always has, and that is why the cache
	// records the offset it projected through rather than the file's size alone —
	// so what this asserts is that every byte that was there is still there, in
	// place, and that the file only grew.
	if len(after) < len(before) || !bytes.Equal(after[:len(before)], before) {
		t.Fatal("the journal was rewritten or truncated across a restore; item 2m5 (c) forbids it")
	}
	secondMessages := second.MessagesCopy()
	if len(secondMessages) != len(firstMessages) {
		t.Fatalf("the cached restore produced %d messages, the projected one %d", len(secondMessages), len(firstMessages))
	}
	for index := range firstMessages {
		if firstMessages[index].ID != secondMessages[index].ID || firstMessages[index].Content != secondMessages[index].Content {
			t.Fatalf("message %d differs between the projected and the cached restore", index)
		}
	}

	// A journal that has changed is projected again: the cache may never show an
	// operator a chat that is missing its newest turns.
	stale, err := os.OpenFile(filepath.Join(root, "chats", first.ID+".jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	appended := events.New(events.MessageAppended, first.ID, "", map[string]any{"message": events.Message{ID: "m-999", Role: "user", Category: "history", Turn: 41, Content: "the newest turn"}})
	appended.SessionID = first.ID
	line, err := json.Marshal(map[string]any{"seq": 99999, "ts": appended.TS, "session_id": first.ID, "run_id": "", "type": appended.Type, "data": appended.Data})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stale.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	third, _ := restore()
	if got := third.MessagesCopy(); len(got) != len(firstMessages)+1 || got[len(got)-1].Content != "the newest turn" {
		t.Fatalf("a journal that grew was served from a stale projection: %d messages, last %q", len(got), got[len(got)-1].Content)
	}
}
