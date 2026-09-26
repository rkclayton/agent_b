package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/config"
)

// Item 2jf: the file is where the rules live, because the file is what survives.
func writer(t *testing.T) (*Manager, string) {
	t.Helper()
	root := t.TempDir()
	manager := New(root, func() config.Config { return config.Config{Memory: config.Memory{Enabled: true, MaxTokens: 1500}} }, nil)
	return manager, filepath.Join(root, "layer.md")
}

// (e): the provenance is in the FILE, so a note that arrived beside external
// content is marked wherever the file is read from.
func TestANoteCarriesItsProvenance2jf(t *testing.T) {
	manager, path := writer(t)
	if duplicate, err := manager.WriteNote(path, Write{
		Note: "the operator prefers focused tests", Scope: "user",
		Run: "r7", Turn: 3, UntrustedInTurn: true,
	}); err != nil || duplicate {
		t.Fatalf("write: %v duplicate=%v", err, duplicate)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(body))
	for _, want := range []string{"the operator prefers focused tests", "scope: user", "run: r7", "turn: 3", "untrusted-in-turn: yes"} {
		if !strings.Contains(line, want) {
			t.Errorf("the file does not record %q:\n%s", want, line)
		}
	}
	// And it reads back as structure, which is what (f)'s view needs.
	notes, err := manager.Notes(path)
	if err != nil || len(notes) != 1 {
		t.Fatalf("notes=%v err=%v", notes, err)
	}
	note := notes[0]
	if note.Text != "the operator prefers focused tests" || note.Scope != "user" || note.Run != "r7" || note.Turn != 3 || !note.UntrustedInTurn {
		t.Fatalf("parsed back wrong: %+v", note)
	}
	if note.Date == "" {
		t.Error("the note lost its date")
	}
	// A note with no provenance — every note written before this item — still
	// reads, and does not claim a scope it never had.
	old := filepath.Join(filepath.Dir(path), "old.md")
	if err := os.WriteFile(old, []byte("- 2026-01-01 an older note with no provenance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	olds, err := manager.Notes(old)
	if err != nil || len(olds) != 1 {
		t.Fatalf("old notes=%v err=%v", olds, err)
	}
	if olds[0].Text != "an older note with no provenance" || olds[0].Scope != "" || olds[0].UntrustedInTurn {
		t.Fatalf("an old note was misread: %+v", olds[0])
	}
}

// (b): the replace happens in the SAME write, so superseding a belief cannot
// leave the stale one behind.
func TestAReplaceRemovesTheOldNoteInTheSameWrite2jf(t *testing.T) {
	manager, path := writer(t)
	for _, note := range []string{"the build uses make", "the mascot is a heron"} {
		if _, err := manager.WriteNote(path, Write{Note: note, Scope: "repository"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := manager.WriteNote(path, Write{
		Note: "the build uses just", Scope: "repository", Replaces: "the build uses make",
	}); err != nil {
		t.Fatal(err)
	}
	notes, err := manager.Notes(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 {
		t.Fatalf("expected 2 notes after a replace, got %d: %+v", len(notes), notes)
	}
	texts := []string{notes[0].Text, notes[1].Text}
	for _, gone := range []string{"the build uses make"} {
		for _, text := range texts {
			if text == gone {
				t.Errorf("the replaced note survived: %q", gone)
			}
		}
	}
	found := false
	for _, text := range texts {
		if text == "the build uses just" {
			found = true
		}
	}
	if !found {
		t.Errorf("the new note is missing: %v", texts)
	}
	// A replaces naming nothing is a mistake worth reporting, not a silent
	// append — otherwise a typo quietly doubles the layer.
	_, err = manager.WriteNote(path, Write{Note: "something else", Scope: "repository", Replaces: "a note that was never written"})
	if !errors.Is(err, ErrNoteNotFound) {
		t.Fatalf("a replaces naming nothing gave %v", err)
	}
	// And nothing was written by the refused call.
	if after, _ := manager.Notes(path); len(after) != 2 {
		t.Fatalf("the refused write changed the file: %d notes", len(after))
	}
}

// (d): the budget is on the FILE and a write that would exceed it is REFUSED
// with what to do. Item 2eb trimmed on the way in, so the file grew forever and
// the operator never met the limit; the harness never trims now.
func TestAFullLayerRefusesTheWriteAndSaysWhatToDo2jf(t *testing.T) {
	manager, path := writer(t)
	// A budget of 40 tokens is about 160 characters.
	long := strings.Repeat("a durable fact ", 8)
	if _, err := manager.WriteNote(path, Write{Note: long, Scope: "user", Budget: 40}); err != nil {
		t.Fatalf("the first note did not fit a 40-token budget: %v", err)
	}
	_, err := manager.WriteNote(path, Write{Note: long + " and another", Scope: "user", Budget: 40})
	if !errors.Is(err, ErrMemoryFull) {
		t.Fatalf("the second note was not refused: %v", err)
	}
	// The message has to be actionable, or the model cannot do anything about it.
	for _, want := range []string{"replace a note you name", "of 40 tokens"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	// THE HARNESS NEVER TRIMS: the file is exactly what it was.
	notes, _ := manager.Notes(path)
	if len(notes) != 1 || notes[0].Text != strings.TrimSpace(long) {
		t.Fatalf("the refused write disturbed the file: %+v", notes)
	}
	// And a REPLACE gets in where an append could not, which is the whole point
	// of refusing rather than trimming.
	if _, err := manager.WriteNote(path, Write{
		Note: "a shorter fact", Scope: "user", Replaces: strings.TrimSpace(long), Budget: 40,
	}); err != nil {
		t.Fatalf("a replace was refused by the budget it made room in: %v", err)
	}
	if notes, _ := manager.Notes(path); len(notes) != 1 || notes[0].Text != "a shorter fact" {
		t.Fatalf("the replace did not land: %+v", notes)
	}
}

func TestADuplicateIsStillADuplicate2jf(t *testing.T) {
	manager, path := writer(t)
	if _, err := manager.WriteNote(path, Write{Note: "one fact", Scope: "user"}); err != nil {
		t.Fatal(err)
	}
	duplicate, err := manager.WriteNote(path, Write{Note: "ONE FACT", Scope: "user"})
	if err != nil || !duplicate {
		t.Fatalf("a case-different repeat was not a duplicate: %v %v", duplicate, err)
	}
	if notes, _ := manager.Notes(path); len(notes) != 1 {
		t.Fatalf("the duplicate was written anyway: %d notes", len(notes))
	}
}

func TestANoteStaysWithinThreeHundredCharacters2jf(t *testing.T) {
	manager, path := writer(t)
	if _, err := manager.WriteNote(path, Write{Note: strings.Repeat("x", 301), Scope: "user"}); err == nil {
		t.Fatal("a 301-character note was accepted")
	}
	if _, err := manager.WriteNote(path, Write{Note: strings.Repeat("x", 300), Scope: "user"}); err != nil {
		t.Fatalf("a 300-character note was refused: %v", err)
	}
}
