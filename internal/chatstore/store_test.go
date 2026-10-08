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

func TestArchiveRestoreKeepsFolderAndOrdersNewestFirst(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "chats"))
	path, err := store.Create("s1", "First", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(store.Root(), "work")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(folder, filepath.Base(path))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Archive("s1", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	active, archived, err := store.List()
	if err != nil || len(active) != 0 || len(archived) != 1 || archived[0].Metadata.ID != "s1" {
		t.Fatalf("list = %#v %#v %v", active, archived, err)
	}
	if _, err := store.Restore("s1"); err != nil {
		t.Fatal(err)
	}
	active, archived, err = store.List()
	if err != nil || len(active) != 1 || len(archived) != 0 || filepath.Dir(active[0].Path) != folder {
		t.Fatalf("restored = %#v %#v %v", active, archived, err)
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

func TestAutoArchiveUsesIdleClockAndProtectsLiveChat(t *testing.T) {
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
	changed, err := store.AutoArchive(now)
	if err != nil || len(changed) != 1 || changed[0] != "old" {
		t.Fatalf("AutoArchive = %#v, %v", changed, err)
	}
}
