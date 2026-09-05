package credential

import (
	"path/filepath"
	"testing"
)

func TestNamedStoreUsesContainedDeterministicPath(t *testing.T) {
	root := t.TempDir()
	store, err := NewNamed(root, "acme")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, ".agentb-profile-credential-acme.dpapi")
	if store.Path() != want {
		t.Fatalf("path=%q want=%q", store.Path(), want)
	}
	for _, invalid := range []string{"", "../escape", "acme", "a/b"} {
		if _, err := NewNamed(root, invalid); err == nil {
			t.Fatalf("invalid credential name %q accepted", invalid)
		}
	}
}
