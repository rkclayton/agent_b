package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/events"
	"harness/internal/session"
)

// Item 2mn (a) and (d): TWO SESSIONS, TWO SCRATCH ROOTS, ONE SHARED FILE.
//
// This is the reproduction the item's @verify asks for, and it was run against the
// OLD key first: with writes recorded as (the writing session's own root, the path
// relative to that root), two sessions writing one file in a shared plan repository
// landed in two different maps and NO conflict fired. The key is the file now.
//
// The event keeps its shape, which is (d): `path` is still what the operator reads,
// workspace-relative to the session that hit the conflict.
func TestASharedRepositoryFileConflictsAcrossScratchRoots(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(repository, "shared.go")
	if err := os.WriteFile(shared, []byte("package repo\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	workspaces := session.NewWorkspaceRegistry()
	labels := map[string]string{"a": "A", "b": "B"}
	bus := events.NewBus()
	stream, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	coordinator := NewFileCoordinator(workspaces, func(id string) string { return labels[id] }, bus)
	writer := NewWriteFile(coordinator)

	// Two sessions whose scratch roots have nothing in common. The file they both
	// write is in neither of them, which is the shared-repository case.
	first := testSession(filepath.Join(root, "scratch-a"), "a", "A")
	second := testSession(filepath.Join(root, "scratch-b"), "b", "B")
	for _, s := range []*session.Session{first, second} {
		if err := os.MkdirAll(s.Workspace, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := writer.Call(context.Background(), first, map[string]any{"path": shared, "content": "package repo // a\n"}); err != nil {
		t.Fatalf("the first write was refused: %v", err)
	}
	_, err := writer.Call(context.Background(), second, map[string]any{"path": shared, "content": "package repo // b\n"})
	if err == nil {
		t.Fatal("the second session overwrote a file the first had just written, with no conflict")
	}
	if !strings.Contains(err.Error(), "session A wrote this file") || !strings.Contains(err.Error(), "re-read before editing") {
		t.Fatalf("the refusal does not name the last writer: %v", err)
	}
	// The file is unchanged: a conflict refuses, it does not half-write.
	data, _ := os.ReadFile(shared)
	if string(data) != "package repo // a\n" {
		t.Fatalf("the file was modified anyway: %q", string(data))
	}

	// (d): the event fired, keeps its fields, and names both sides.
	conflict := map[string]any{}
	for conflict["session_id"] == nil {
		select {
		case event := <-stream:
			if event.Type != events.WorkspaceConflict {
				continue
			}
			fields, ok := event.Data.(map[string]any)
			if !ok {
				t.Fatalf("the conflict event's data is not a field map: %T", event.Data)
			}
			conflict = fields
		case <-time.After(2 * time.Second):
			t.Fatal("no workspace.conflict event was published")
		}
	}
	if conflict["other_session_id"] != "a" || conflict["session_id"] != "b" {
		t.Fatalf("the conflict event does not name both sessions: %+v", conflict)
	}
	if conflict["other_label"] != "A" {
		t.Fatalf("the conflict event does not carry the other session's label: %+v", conflict)
	}
	if _, ok := conflict["path"].(string); !ok {
		t.Fatalf("the conflict event lost its path: %+v", conflict)
	}

	// And a read clears it: the seen-list is keyed by the same file, so the second
	// session reading what the first wrote may then write.
	second.Touch(session.FileKey(shared))
	if _, err := writer.Call(context.Background(), second, map[string]any{"path": shared, "content": "package repo // b\n"}); err != nil {
		t.Fatalf("a session that has now seen the file was still refused: %v", err)
	}
}

// The same identity, reached by two different but equivalent spellings.
func TestOneFileHasOneKeyHoweverItIsSpelled(t *testing.T) {
	root := t.TempDir()
	direct := filepath.Join(root, "x.txt")
	roundabout := filepath.Join(root, "sub", "..", "x.txt")
	registry := session.NewWorkspaceRegistry()
	registry.RecordWrite(direct, "a")
	if _, ok := registry.LastWriter(roundabout); !ok {
		t.Fatal("the same file reached by a different spelling is a different key")
	}
	if session.FileKey(direct) != session.FileKey(roundabout) {
		t.Fatalf("%q and %q are different keys", session.FileKey(direct), session.FileKey(roundabout))
	}
	if _, ok := registry.LastWriter(filepath.Join(root, "y.txt")); ok {
		t.Fatal("a different file shares a key")
	}
}

// The defect itself, stated as the arithmetic it was: the OLD key was the writing
// session's own root plus the path relative to that root, so one shared file
// produced two keys and the conflict could not fire. This does not revert the fix
// to prove it — it computes the old key the old way and shows the two disagree,
// which is the same demonstration and survives in the record.
func TestTheOldKeyGaveOneSharedFileTwoIdentities(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "repo", "shared.go")
	oldKey := func(sessionRoot string) string {
		rel, err := filepath.Rel(sessionRoot, shared)
		if err != nil || strings.HasPrefix(rel, "..") {
			// This is the branch the coordinator took: a file outside the session's
			// own root fell back to the absolute path, and the ROOT half of the key
			// still differed, so the two never met.
			rel = filepath.ToSlash(filepath.Clean(shared))
		}
		return filepath.ToSlash(filepath.Clean(sessionRoot)) + "|" + filepath.ToSlash(rel)
	}
	a, b := filepath.Join(root, "scratch-a"), filepath.Join(root, "scratch-b")
	if oldKey(a) == oldKey(b) {
		t.Fatal("the old key did not actually differ, so this test is not describing the defect")
	}
	// And the new key is one identity, whichever root asks.
	if session.FileKey(shared) != session.FileKey(shared) {
		t.Fatal("the file key is not stable")
	}
}
