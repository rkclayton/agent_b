package chatstore

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchRescansExplorerMoveByIdentity(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "chats"))
	path, err := store.Create("stable", "Before", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	changes := make(chan []Entry, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = store.Watch(ctx, func(entries []Entry) { changes <- entries }) }()
	select {
	case <-changes:
	case <-time.After(2 * time.Second):
		t.Fatal("initial scan timed out")
	}
	folder := filepath.Join(store.Root(), "Moved")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(folder, "After")
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case entries := <-changes:
			for _, entry := range entries {
				if entry.Metadata.ID == "stable" && entry.Path == moved {
					return
				}
			}
		case <-deadline:
			t.Fatal("move was not rescanned by identity")
		}
	}
}

func TestWatchCoalescesBurstAndStillNoticesOneChange(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "chats"))
	changes := make(chan []Entry, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = store.Watch(ctx, func(entries []Entry) { changes <- entries }) }()
	select {
	case <-changes:
	case <-time.After(2 * time.Second):
		t.Fatal("initial scan timed out")
	}
	started := time.Now()
	var first string
	for index := 0; index < 45; index++ {
		path, err := store.Create(fmt.Sprintf("burst-%02d", index), fmt.Sprintf("Burst %02d", index), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			first = filepath.Join(path, "chat.json")
		}
	}
	if time.Since(started) >= time.Second {
		t.Fatalf("fixture did not produce its burst within one second: %v", time.Since(started))
	}
	select {
	case entries := <-changes:
		if len(entries) != 45 {
			t.Fatalf("burst scan saw %d chats, want 45", len(entries))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("burst scan timed out")
	}
	select {
	case <-changes:
		t.Fatal("45 writes caused more than one rescan")
	case <-time.After(500 * time.Millisecond):
	}
	if err := os.Chtimes(first, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changes:
	case <-time.After(2 * time.Second):
		t.Fatal("one changed file was not noticed within the existing two-second bound")
	}
}

func TestCreateWritesChatMetadataInNamedDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "chats")
	created := time.Date(2026, 9, 29, 21, 17, 0, 0, time.UTC)
	store := New(root)
	first, err := store.Create("s2", "Build: report?", created)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create("s3", "Build: report?", created.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if first != filepath.Join(root, "Build_ report_") || second != filepath.Join(root, "Build_ report_ (2)") {
		t.Fatalf("paths = %q, %q", first, second)
	}
	metadata, err := ReadMetadata(first)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.ID != "s2" || metadata.Label != "Build: report?" || !metadata.Created.Equal(created) {
		t.Fatalf("metadata = %+v", metadata)
	}
	if info, err := os.Stat(filepath.Join(first, "chat.json")); err != nil || info.IsDir() {
		t.Fatalf("chat.json: %v, %+v", err, info)
	}
}

func TestFindUsesChatJSONIdentityRatherThanDirectoryName(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "chats"))
	path, err := store.Create("stable-id", "Before", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(store.Root(), "Folder", "After")
	if err := os.MkdirAll(filepath.Dir(moved), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	entry, found, err := store.Find("stable-id")
	if err != nil || !found || entry.Path != moved || entry.Metadata.ID != "stable-id" {
		t.Fatalf("Find = %+v, %v, %v", entry, found, err)
	}
}

func TestMigrateMovesLegacyBytesOnceAndWritesIdentity(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "scratch", "s7")
	if err := os.MkdirAll(filepath.Join(legacy, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("byte-for-byte migration proof\x00\xff")
	file := filepath.Join(legacy, "nested", "proof.bin")
	if err := os.WriteFile(file, original, 0o600); err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256(original)
	created := time.Date(2026, 9, 29, 21, 17, 0, 0, time.UTC)
	store := New(filepath.Join(root, "chats"))
	destination, migrated, err := store.Migrate(legacy, "s7", "Existing chat", created)
	if err != nil || !migrated {
		t.Fatalf("Migrate = %q, %v, %v", destination, migrated, err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy remains: %v", err)
	}
	moved, err := os.ReadFile(filepath.Join(destination, "nested", "proof.bin"))
	if err != nil || sha256.Sum256(moved) != wantHash {
		t.Fatalf("moved bytes changed: %v", err)
	}
	metadata, err := ReadMetadata(destination)
	if err != nil || metadata.ID != "s7" || metadata.Label != "Existing chat" || !metadata.Created.Equal(created) {
		t.Fatalf("metadata = %+v, %v", metadata, err)
	}
	again, migrated, err := store.Migrate(legacy, "s7", "Existing chat", created)
	if err != nil || migrated || again != destination {
		t.Fatalf("second Migrate = %q, %v, %v", again, migrated, err)
	}
}

func TestRestoreChoosesTheOldestDuplicateChatIdentity2sc(t *testing.T) {
	root := t.TempDir()
	store := New(filepath.Join(root, "chats"))
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	first, err := store.Create("same", "first", older)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Create("same", "second", older.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	path, migrated, err := store.Migrate(filepath.Join(root, "missing"), "same", "ignored", older)
	if err != nil || migrated || path != first {
		t.Fatalf("path=%q first=%q migrated=%v err=%v", path, first, migrated, err)
	}
}

func TestCleanupRemovesOnlyAppOwnedLeftoversAndClearsArchiveMark2ta(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "chats"))
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	makeChat := func(id, label string, created time.Time) string {
		t.Helper()
		path, err := store.Create(id, label, created)
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	onlyMetadata := makeChat("empty", "empty", now)
	appOwned := makeChat("app", "app", now)
	if err := os.MkdirAll(filepath.Join(appOwned, "attachments", ".agentb-index"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appOwned, "attachments", "upload.txt"), []byte("upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	unknown := makeChat("held", "held", now)
	unknownFile := filepath.Join(unknown, "work.txt")
	if err := os.WriteFile(unknownFile, []byte("exact bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	canonical := makeChat("same", "first", now.Add(-time.Hour))
	spare := makeChat("same", "second", now)
	archived := makeChat("archived", "kept name", now)
	meta, _ := ReadMetadata(archived)
	meta.ArchivedAt = now
	meta.Pinned = true
	if err := WriteMetadata(archived, meta); err != nil {
		t.Fatal(err)
	}
	unmarked := makeChat("ordinary", "ordinary", now)
	unmarkedBytes, _ := os.ReadFile(filepath.Join(unmarked, MetadataFile))
	markedEmpty := makeChat("marked-empty", "marked-empty", now)
	meta, _ = ReadMetadata(markedEmpty)
	meta.ArchivedAt = now
	if err := WriteMetadata(markedEmpty, meta); err != nil {
		t.Fatal(err)
	}

	result, err := store.Cleanup(map[string]bool{"same": true, "archived": true, "ordinary": true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 4 || len(result.Kept) != 1 || result.Kept[0] != unknown {
		t.Fatalf("cleanup = %+v", result)
	}
	for _, path := range []string{onlyMetadata, appOwned, spare, markedEmpty} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("leftover remains at %s: %v", path, err)
		}
	}
	for _, path := range []string{canonical, archived, unmarked, unknown} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("kept path %s: %v", path, err)
		}
	}
	if got, _ := os.ReadFile(unknownFile); string(got) != "exact bytes" {
		t.Fatalf("unknown bytes = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(unmarked, MetadataFile)); string(got) != string(unmarkedBytes) {
		t.Fatal("unmarked metadata changed")
	}
	meta, err = ReadMetadata(archived)
	if err != nil || !meta.ArchivedAt.IsZero() || !meta.Pinned || meta.Label != "kept name" {
		t.Fatalf("restored metadata = %+v, %v", meta, err)
	}
}

func TestPinSurvivesRestartAndMove2qz(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	if _, err := store.Create("pinned", "Pinned", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPinned("pinned", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddFolder("", "Work"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Move("pinned", "Work"); err != nil {
		t.Fatal(err)
	}
	entry, found, err := New(root).Find("pinned")
	if err != nil || !found || !entry.Metadata.Pinned {
		t.Fatalf("restart after move: found=%v metadata=%+v err=%v", found, entry.Metadata, err)
	}
}

func TestOldChatIsNeverMarkedArchived2ta(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "chats"))
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		id   string
		age  int
		live bool
	}{{"old", 15, false}, {"recent", 13, false}, {"live", 15, true}} {
		path, err := store.Create(item.id, item.id, now.AddDate(0, 0, -item.age))
		if err != nil {
			t.Fatal(err)
		}
		meta, err := ReadMetadata(path)
		if err != nil {
			t.Fatal(err)
		}
		meta.LastActivity = now.AddDate(0, 0, -item.age)
		meta.Live = item.live
		if err := WriteMetadata(path, meta); err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range mustScan(t, store) {
		if !entry.Metadata.ArchivedAt.IsZero() {
			t.Fatalf("chat %s was archived", entry.Metadata.ID)
		}
	}
}

func mustScan(t *testing.T, store *Store) []Entry {
	t.Helper()
	entries, err := store.Scan()
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestNoticeModeAndLastUserMessageSurviveMoveAndRestart2re(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	if _, err := store.Create("chat", "one", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddFolder("", "Work"); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	if err := store.SetNotice("chat", "off", when); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Move("chat", "Work"); err != nil {
		t.Fatal(err)
	}
	states, err := New(root).NoticeStates()
	if err != nil || states["chat"].Notify != "off" || !states["chat"].LastUserMessage.Equal(when) {
		t.Fatalf("states=%+v err=%v", states, err)
	}
}
