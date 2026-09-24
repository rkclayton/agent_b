package credential

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNamedStoreUsesContainedDeterministicPath(t *testing.T) {
	root := t.TempDir()
	store, err := NewNamed(root, "homepc")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, ".agentb-connection-credential-homepc.dpapi")
	if store.Path() != want {
		t.Fatalf("path=%q want=%q", store.Path(), want)
	}
	for _, invalid := range []string{"", "../escape", "HomePC", "a/b"} {
		if _, err := NewNamed(root, invalid); err == nil {
			t.Fatalf("invalid credential name %q accepted", invalid)
		}
	}
}

func TestNamedStoreReadsLegacyCredentialFileAndWritesCurrentName(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("DPAPI is Windows-only")
	}
	store, err := NewNamed(t.TempDir(), "homepc")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Write([]byte("legacy-secret")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(store.path, store.legacyPath); err != nil {
		t.Fatal(err)
	}
	value, err := store.Read()
	if err != nil || !bytes.Equal(value, []byte("legacy-secret")) || !store.Status().Stored {
		t.Fatalf("legacy read value=%q status=%+v err=%v", value, store.Status(), err)
	}
	if err := store.Write([]byte("current-secret")); err != nil {
		t.Fatal(err)
	}
	value, err = store.Read()
	if err != nil || !bytes.Equal(value, []byte("current-secret")) {
		t.Fatalf("current file did not take precedence: value=%q err=%v", value, err)
	}
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.path); !os.IsNotExist(err) {
		t.Fatalf("current credential survived clear: %v", err)
	}
	if _, err := os.Stat(store.legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy credential survived clear: %v", err)
	}
}
