package chatstore

import (
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
