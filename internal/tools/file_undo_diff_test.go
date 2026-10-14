package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileUndoListDiffAndOneFileRestore2sk(t *testing.T) {
	root := t.TempDir()
	store := NewFileUndoStore(filepath.Join(root, "data"))
	chat, run := "chat", "run"
	edited := filepath.Join(root, "edited.txt")
	created := filepath.Join(root, "created.txt")
	removed := filepath.Join(root, "removed.txt")
	before := "one\ntwo\nthree\nfour\nfive\nsix\nseven"
	if err := os.WriteFile(edited, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(removed, []byte("gone"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ path, display string }{{edited, "edited.txt"}, {created, "created.txt"}, {removed, "removed.txt"}} {
		if err := store.Prepare(chat, run, item.path, item.display); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(edited, []byte(strings.Replace(before, "four", "FOUR", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(created, []byte("new one\nnew two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{edited, created, removed} {
		if err := store.Commit(chat, run, path); err != nil {
			t.Fatal(err)
		}
	}

	files, held, err := store.Files(chat, run)
	if err != nil || !held || len(files) != 3 {
		t.Fatalf("held=%v files=%+v err=%v", held, files, err)
	}
	kinds := map[string]string{}
	ids := map[string]string{}
	for _, file := range files {
		kinds[file.Path], ids[file.Path] = file.Kind, file.ID
	}
	if kinds["edited.txt"] != "changed" || kinds["created.txt"] != "created" || kinds["removed.txt"] != "removed" {
		t.Fatalf("kinds=%v", kinds)
	}
	diff, err := store.Diff(chat, run, ids["edited.txt"])
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Lines) != 8 || diff.Lines[0].Text != "one" || diff.Lines[3].Kind != "removed" || diff.Lines[3].OldLine != 4 || diff.Lines[4].Kind != "added" || diff.Lines[4].NewLine != 4 || diff.Lines[7].Text != "seven" {
		t.Fatalf("diff=%+v", diff)
	}
	createdDiff, _ := store.Diff(chat, run, ids["created.txt"])
	removedDiff, _ := store.Diff(chat, run, ids["removed.txt"])
	for _, line := range createdDiff.Lines {
		if line.Kind != "added" {
			t.Fatalf("created=%+v", createdDiff)
		}
	}
	for _, line := range removedDiff.Lines {
		if line.Kind != "removed" {
			t.Fatalf("removed=%+v", removedDiff)
		}
	}
	result, err := store.UndoFile(chat, run, ids["edited.txt"])
	if err != nil || len(result.Restored) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if got, _ := os.ReadFile(edited); string(got) != before {
		t.Fatalf("edited=%q", got)
	}
	if got, _ := os.ReadFile(created); string(got) != "new one\nnew two" {
		t.Fatalf("created changed=%q", got)
	}
	if _, err := os.Stat(removed); !os.IsNotExist(err) {
		t.Fatalf("removed restored: %v", err)
	}
	files, _, _ = store.Files(chat, run)
	if len(files) != 2 {
		t.Fatalf("remaining=%+v", files)
	}
}

func TestFileUndoDiffChangedSinceAndBounds2sk(t *testing.T) {
	root := t.TempDir()
	store := NewFileUndoStore(filepath.Join(root, "data"))
	chat, run := "chat", "run"
	path := filepath.Join(root, "large.txt")
	before := strings.Repeat("before\n", 5000)
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Prepare(chat, run, path, "large.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("after\n", 5000)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(chat, run, path); err != nil {
		t.Fatal(err)
	}
	files, _, _ := store.Files(chat, run)
	id := files[0].ID
	started := time.Now()
	diff, err := store.Diff(chat, run, id)
	elapsed := time.Since(started)
	if err != nil || len(diff.Lines) != 2000 || diff.More != 8000 {
		t.Fatalf("lines=%d more=%d err=%v", len(diff.Lines), diff.More, err)
	}
	if err := os.WriteFile(path, []byte("changed by hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diff, _ = store.Diff(chat, run, id)
	if !diff.File.ChangedSince {
		t.Fatal("later change not identified")
	}
	result, err := store.UndoFile(chat, run, id)
	if err != nil || len(result.Skipped) != 1 || !strings.Contains(result.Skipped[0], "large.txt") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("5000-line diff took %s", elapsed)
	}
	t.Logf("5,000-line bounded diff took %s", elapsed)
}

func TestFileUndoDiffRefusesBinaryAndOversized2sk(t *testing.T) {
	root := t.TempDir()
	store := NewFileUndoStore(filepath.Join(root, "data"))
	for _, fixture := range []struct {
		run, name string
		before    []byte
		want      string
	}{
		{"binary", "binary.dat", []byte{'a', 0, 'b'}, "Binary file; no diff."},
		{"large", "large.txt", []byte(strings.Repeat("x", (512<<10)+1)), "File is over 512 KiB; no diff."},
	} {
		path := filepath.Join(root, fixture.name)
		if err := os.WriteFile(path, fixture.before, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := store.Prepare("chat", fixture.run, path, fixture.name); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append([]byte(nil), fixture.before...), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := store.Commit("chat", fixture.run, path); err != nil {
			t.Fatal(err)
		}
		files, _, _ := store.Files("chat", fixture.run)
		diff, err := store.Diff("chat", fixture.run, files[0].ID)
		if err != nil || diff.Message != fixture.want || len(diff.Lines) != 0 {
			t.Fatalf("%s diff=%+v err=%v", fixture.name, diff, err)
		}
	}
}
