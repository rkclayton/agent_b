package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceFileChangesIncludesCreatedAndModifiedFiles(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "keep.txt")
	changed := filepath.Join(root, "changed.txt")
	if err := os.WriteFile(keep, []byte("same"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(changed, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := workspaceFileSnapshot(root)
	if err := os.WriteFile(changed, []byte("new and longer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "created.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := workspaceFileChanges(root, before)
	if len(got) != 2 || got["changed.txt"].Path != "changed.txt" || got["created.txt"].Path != "created.txt" {
		t.Fatalf("changes=%#v", got)
	}
}

func TestMessageLimitErrorExtractsServerSentence(t *testing.T) {
	limit, sentence, ok := messageLimitError(assertError(`chat stream HTTP 400: {"error":{"message":"conversation too long: 61 messages (limit 60)"}}`))
	if !ok || limit != 60 || sentence != "conversation too long: 61 messages (limit 60)" {
		t.Fatalf("limit=%d sentence=%q ok=%t", limit, sentence, ok)
	}
}

type assertError string

func (e assertError) Error() string { return string(e) }
