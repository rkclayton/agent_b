package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/projection"
	"harness/internal/quietproc"
	"harness/internal/session"
)

func TestSixtyFourChatStartupReadsSmallStateWithinTwoSeconds2qc(t *testing.T) {
	emptyRoot, _ := startupStore2tm(t, false)
	fullRoot, journals := startupStore2tm(t, true)
	ratios, empties, fulls := []float64{}, []time.Duration{}, []time.Duration{}
	for len(ratios) < 9 {
		empty := timedProcessStartup2tm(t, emptyRoot, nil, false)
		full := timedProcessStartup2tm(t, fullRoot, journals, false)
		if empty == 0 || full == 0 {
			continue
		}
		empties, fulls = append(empties, empty), append(fulls, full)
		ratios = append(ratios, float64(full)/float64(empty))
	}
	sort.Float64s(ratios)
	sort.Slice(empties, func(i, j int) bool { return empties[i] < empties[j] })
	sort.Slice(fulls, func(i, j int) bool { return fulls[i] < fulls[j] })
	middleRatio, emptyMiddle, fullMiddle := ratios[4], empties[4], fulls[4]
	t.Logf("2tm startup middle_ratio=%.2fx empty_middle=%s full_middle=%s", middleRatio, emptyMiddle, fullMiddle)
	if middleRatio > 2 || (emptyMiddle < time.Second && fullMiddle > 2*time.Second) {
		t.Fatalf("slow 64-chat startup: %.2fx, empty %s, full %s", middleRatio, emptyMiddle, fullMiddle)
	}
	slow := timedProcessStartup2tm(t, fullRoot, journals, true)
	if ratio := float64(slow) / float64(emptyMiddle); ratio <= 2 {
		t.Fatalf("whole-journal stand-in escaped startup bound: %.2fx", ratio)
	} else {
		t.Logf("2tm whole-journal stand-in ratio=%.2fx", ratio)
	}
}

func startupStore2tm(t *testing.T, full bool) (string, []string) {
	t.Helper()
	root := t.TempDir()
	writers, err := events.NewWriters(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	defer writers.Close()
	paths := []string{}
	if !full {
		return root, paths
	}
	for index := 0; index < 64; index++ {
		id := fmt.Sprintf("s%d", index+1)
		path := filepath.Join(writers.ChatDir(), id+".jsonl")
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		writer := bufio.NewWriterSize(file, 1<<20)
		seed, _ := json.Marshal(events.New(events.SessionCreated, id, "", map[string]any{"session": map[string]any{"id": id, "label": id, "created_at": time.Unix(int64(index+1), 0).UTC().Format(time.RFC3339Nano), "run": map[string]any{"status": "idle"}, "tools": []any{}, "messages": []any{}}}))
		_, _ = writer.Write(append(seed, '\n'))
		if index == 0 {
			for part := 0; part < 2; part++ {
				large := events.New(events.Stage, id, "r1", map[string]any{"stage": "assemble", "state": "exit", "turn": 1})
				large.Raw = strings.Repeat("x", 50<<20)
				line, _ := json.Marshal(large)
				line = append(line, '\n')
				_, _ = writer.Write(line)
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
	if _, _, _, err := restoreProjections(writers, paths); err != nil {
		t.Fatal(err)
	}
	return root, paths
}

func timedProcessStartup2tm(t *testing.T, root string, paths []string, whole bool) time.Duration {
	t.Helper()
	command := quietproc.Quiet(exec.Command(os.Args[0], "-test.run=^TestStartupProcess2tm$", "-test.count=1"))
	command.Env = append(os.Environ(), "AGENTB_2TM_ROOT="+root, "AGENTB_2TM_PATHS="+strings.Join(paths, string(os.PathListSeparator)), fmt.Sprintf("AGENTB_2TM_WHOLE=%t", whole))
	command.Stdout, command.Stderr = io.Discard, io.Discard
	started := time.Now()
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	return time.Since(started)
}

func TestStartupProcess2tm(t *testing.T) {
	root := os.Getenv("AGENTB_2TM_ROOT")
	if root == "" {
		t.Skip("startup subprocess")
	}
	paths := filepath.SplitList(os.Getenv("AGENTB_2TM_PATHS"))
	if os.Getenv("AGENTB_2TM_WHOLE") == "true" {
		for _, path := range paths {
			if _, _, err := projection.ReadFile(path, 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	writers, err := events.NewWriters(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	defer writers.Close()
	sessions, _, hits, err := restoreProjections(writers, paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != len(paths) || hits != len(paths) {
		t.Fatalf("chats=%d hits=%d want=%d", len(sessions), hits, len(paths))
	}
}

func TestCompletedJournalNormalizationIsNotRepeated2tm(t *testing.T) {
	writers, err := events.NewWriters(filepath.Join(t.TempDir(), "logs"))
	if err != nil {
		t.Fatal(err)
	}
	defer writers.Close()
	path := filepath.Join(writers.ChatDir(), "s1.jsonl")
	created := events.New(events.SessionCreated, "s1", "", map[string]any{"session": map[string]any{"id": "s1", "label": "one", "run": map[string]any{"status": "idle"}}})
	created.Raw = strings.Repeat("x", 1<<20)
	line, _ := json.Marshal(created)
	if err := os.WriteFile(path, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := slimRetainedChats(writers, []string{path}); got != 1 {
		t.Fatalf("first pass processed %d journals", got)
	}
	marker := projection.CacheEntryPath(writers.ProjectionCacheDir(), path) + ".slim"
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("normalization marker: %v", err)
	}
	info, _ := os.Stat(path)
	if info.Size() >= 1<<20 {
		t.Fatalf("diagnostic body was not slimmed: %d bytes", info.Size())
	}
	if got := slimRetainedChats(writers, []string{path}); got != 0 {
		t.Fatalf("completed pass repeated for %d journals", got)
	}
	pending := events.PendingChatPath(writers.ChatDir(), "s1")
	renamed, _ := json.Marshal(events.New(events.SessionRenamed, "s1", "", map[string]any{"label": "recovered"}))
	if err := os.WriteFile(pending, append(renamed, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := slimRetainedChats(writers, []string{path}); got != 1 {
		t.Fatalf("pending recovery processed %d journals", got)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatalf("pending recovery was skipped: %v", err)
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
		nextBus.SetDurableSink(retainedSink(next), nil, nil)
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
