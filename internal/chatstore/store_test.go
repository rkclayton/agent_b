package chatstore

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
