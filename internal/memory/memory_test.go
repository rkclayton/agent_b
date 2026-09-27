package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
)

func testManager(t *testing.T, baseDir, workspace string) *Manager {
	t.Helper()
	cfg := config.Defaults(workspace)
	cfg.Memory.Dir = "memory"
	return New(baseDir, func() config.Config { return cfg }, func(context.Context, string, string) (int, error) {
		return 0, nil
	})
}

func TestCanonicalWorkspaceMemoryKeyAndLegacyDefaultMigration(t *testing.T) {
	baseDir := t.TempDir()
	workspace := filepath.Join(baseDir, "MixedCaseWorkspace")
	manager := testManager(t, baseDir, workspace)
	canonical := manager.Path(workspace)
	if filepath.Separator == '\\' {
		alternate := strings.ToUpper(workspace)
		if got := manager.Path(alternate); !strings.EqualFold(filepath.Base(got), filepath.Base(canonical)) {
			t.Fatalf("case spelling changed key: %q vs %q", got, canonical)
		}
		clean, _ := filepath.Abs(workspace)
		legacySum := sha256.Sum256([]byte(filepath.Clean(clean)))
		legacy := filepath.Join(filepath.Dir(canonical), filepath.Base(clean)+"-"+hex.EncodeToString(legacySum[:4])+".md")
		if legacy != canonical {
			if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(legacy, []byte("- 2026-09-07 existing default memory\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := manager.Path(workspace); got != legacy {
				t.Fatalf("legacy migration path=%q want %q", got, legacy)
			}
			content, err := manager.Read(workspace)
			if err != nil || !strings.Contains(content, "existing default memory") {
				t.Fatalf("content=%q err=%v", content, err)
			}
		}
	}
	if _, _, err := manager.Note(workspace, "clear me"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Clear(workspace); err != nil {
		t.Fatal(err)
	}
	if content, err := manager.Read(workspace); err != nil || content != "" {
		t.Fatalf("after clear=%q err=%v", content, err)
	}
}

func TestReadReturnsNotedEntriesAcrossManagerRestart(t *testing.T) {
	baseDir := t.TempDir()
	workspace := filepath.Join(baseDir, "workspace")
	first := testManager(t, baseDir, workspace)
	if _, duplicate, err := first.Note(workspace, "run the full Go test suite"); err != nil || duplicate {
		t.Fatalf("Note() = duplicate %v, error %v", duplicate, err)
	}

	content, err := first.Read(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "- ") || !strings.Contains(content, " run the full Go test suite") {
		t.Fatalf("Read() = %q, want dated stored entry", content)
	}

	restarted := testManager(t, baseDir, workspace)
	afterRestart, err := restarted.Read(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if afterRestart != content {
		t.Fatalf("Read() after manager restart = %q, want %q", afterRestart, content)
	}
}

func TestReadEmptyStoreReturnsCleanly(t *testing.T) {
	baseDir := t.TempDir()
	workspace := filepath.Join(baseDir, "workspace")
	manager := testManager(t, baseDir, workspace)
	content, err := manager.Read(workspace)
	if err != nil || content != "" {
		t.Fatalf("Read() = %q, %v; want empty success", content, err)
	}
	if err := os.MkdirAll(filepath.Dir(manager.Path(workspace)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.Path(workspace), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	content, err = manager.Read(workspace)
	if err != nil || content != "" {
		t.Fatalf("Read() of empty file = %q, %v; want empty success", content, err)
	}
}

func TestPathAlwaysStaysInConfiguredMemoryDirectory(t *testing.T) {
	baseDir := t.TempDir()
	memoryDir := filepath.Join(baseDir, "memory")
	cfg := config.Defaults(baseDir)
	cfg.Memory.Dir = memoryDir
	manager := New(baseDir, func() config.Config { return cfg }, nil)

	for _, workspace := range []string{baseDir, filepath.Join(baseDir, "..", "outside"), filepath.VolumeName(baseDir) + string(filepath.Separator)} {
		path := manager.Path(workspace)
		relative, err := filepath.Rel(memoryDir, path)
		if err != nil {
			t.Fatal(err)
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			t.Fatalf("Path(%q) escaped memory directory: %q", workspace, path)
		}
	}
}

// Item 2mw (d): TWO CORRECTIONS DIFFERING ONLY IN CASE AND WHITESPACE ARE ONE NOTE.
// The duplicate check used to compare the remainder of the stored line against the
// incoming note, so either difference wrote a second copy of the same belief.
func TestACorrectionDifferingOnlyInCaseOrSpacingIsNotWrittenTwice(t *testing.T) {
	baseDir := t.TempDir()
	manager := testManager(t, baseDir, baseDir)

	first := "The installer refuses a workspace whose leaf it does not know " + ReflectionSuffix(ReflectionUnconfirmed, "r1")
	if _, duplicate, err := manager.NoteAgent("coder", first); err != nil || duplicate {
		t.Fatalf("the first write was refused: duplicate=%v err=%v", duplicate, err)
	}
	for _, variant := range []string{
		"the installer refuses a workspace whose leaf it does not know " + ReflectionSuffix(ReflectionUnconfirmed, "r1"),
		"The  installer   refuses a workspace  whose leaf it does not know " + ReflectionSuffix(ReflectionUnconfirmed, "r1"),
		// And a different run saying the same thing is still the same thing.
		"The installer refuses a workspace whose leaf it does not know " + ReflectionSuffix(ReflectionUnconfirmed, "r9"),
	} {
		_, duplicate, err := manager.NoteAgent("coder", variant)
		if err != nil {
			t.Fatal(err)
		}
		if !duplicate {
			t.Errorf("a second copy was written for %q", variant)
		}
	}
	notes, err := manager.Notes(manager.AgentPath("coder"))
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("the layer holds %d notes: %v", len(notes), notes)
	}
	// (b): the provenance is provenance, not part of the note's words.
	if notes[0].Reflection != ReflectionUnconfirmed || notes[0].Run != "r1" {
		t.Errorf("provenance was not parsed: %+v", notes[0])
	}
	if strings.Contains(notes[0].Text, "reflection") || strings.Contains(notes[0].Text, "[") {
		t.Errorf("the marker is inside the note's words: %q", notes[0].Text)
	}
}

// Item 2mw (a): CONFIRMING REMOVES THE UNCONFIRMED MARKER AND DATES THE AGREEMENT,
// and what the next request loads no longer carries the word.
func TestConfirmingAReflectionNoteTakesTheWordUnconfirmedOutOfThePrompt(t *testing.T) {
	baseDir := t.TempDir()
	manager := testManager(t, baseDir, baseDir)
	note := "PowerShell 7 is the shell here " + ReflectionSuffix(ReflectionUnconfirmed, "r4")
	if _, _, err := manager.NoteAgent("coder", note); err != nil {
		t.Fatal(err)
	}
	before, _, err := manager.LoadAgent(context.Background(), "coder", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(before, ReflectionUnconfirmed) {
		t.Fatalf("the unconfirmed note did not reach the prompt: %q", before)
	}

	confirmed, err := manager.ConfirmAgent("coder", "PowerShell 7 is the shell here", "")
	if err != nil || !confirmed {
		t.Fatalf("confirm reported %v, %v", confirmed, err)
	}
	after, _, err := manager.LoadAgent(context.Background(), "coder", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(after, ReflectionUnconfirmed) {
		t.Errorf("the prompt still carries the word unconfirmed: %q", after)
	}
	if !strings.Contains(after, "PowerShell 7 is the shell here") {
		t.Errorf("confirming lost the note: %q", after)
	}
	notes, err := manager.Notes(manager.AgentPath("coder"))
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || !strings.HasPrefix(notes[0].Reflection, ReflectionConfirmed) {
		t.Fatalf("the note is not confirmed: %+v", notes)
	}
	// The run survives confirmation: it is a fact about when the machine noticed.
	if notes[0].Run != "r4" {
		t.Errorf("confirming lost the run: %+v", notes[0])
	}

	// Editing is the same operation with the operator's words.
	if edited, err := manager.ConfirmAgent("coder", "PowerShell 7 is the shell here", "PowerShell 7 is the shell, and 5.1 is rewritten"); err != nil || !edited {
		t.Fatalf("edit reported %v, %v", edited, err)
	}
	notes, _ = manager.Notes(manager.AgentPath("coder"))
	if len(notes) != 1 || notes[0].Text != "PowerShell 7 is the shell, and 5.1 is rewritten" {
		t.Fatalf("the edit did not take: %+v", notes)
	}
}

// Item 2mw (c): A DELETED NOTE IS RESTORABLE, for a stated period, from the same page
// that removed it. The removal itself is unchanged - it still goes through the exact
// note route - and the undo reads the tombstone beside the layer.
func TestADeletedNoteIsRestorableWithinTheStatedPeriod(t *testing.T) {
	baseDir := t.TempDir()
	manager := testManager(t, baseDir, baseDir)
	note := "the exchange folder is read flat " + ReflectionSuffix(ReflectionUnconfirmed, "r7")
	if _, _, err := manager.NoteAgent("coder", note); err != nil {
		t.Fatal(err)
	}
	removed, err := manager.RemoveAgent("coder", note)
	if err != nil || !removed {
		t.Fatalf("remove reported %v, %v", removed, err)
	}
	if body, _ := manager.ReadAgent("coder"); strings.Contains(body, "read flat") {
		t.Fatalf("the note was not removed: %q", body)
	}

	restorable, err := manager.RemovedAgent("coder")
	if err != nil {
		t.Fatal(err)
	}
	if len(restorable) != 1 || !strings.Contains(restorable[0].Text, "read flat") {
		t.Fatalf("the removed note is not restorable: %+v", restorable)
	}
	if restored, err := manager.RestoreAgent("coder", "the exchange folder is read flat"); err != nil || !restored {
		t.Fatalf("restore reported %v, %v", restored, err)
	}
	if body, _ := manager.ReadAgent("coder"); !strings.Contains(body, "read flat") {
		t.Fatalf("the note did not come back: %q", body)
	}
	// It leaves the tombstone, so it cannot be restored into the layer twice.
	if again, err := manager.RemovedAgent("coder"); err != nil || len(again) != 0 {
		t.Fatalf("the tombstone still holds the restored note: %+v %v", again, err)
	}

	// And a tombstone entry older than the stated period is gone. The date is the
	// day of the removal, which is what the retention is measured from.
	tomb := tombstonePath(manager.AgentPath("coder"))
	stale := time.Now().UTC().Add(-TombstoneRetention - 48*time.Hour).Format("2006-01-02")
	if err := os.WriteFile(tomb, []byte("- "+stale+" a belief from long ago\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if expired, err := manager.RemovedAgent("coder"); err != nil || len(expired) != 0 {
		t.Fatalf("an entry past the retention is still offered: %+v %v", expired, err)
	}
}

// Item 2mw (e): THE MACHINE LAYER is a third file loaded like the other two, and it
// reaches the prompt under its own heading rather than being mixed into the agent's.
func TestTheMachineLayerIsItsOwnGroupAndLoadsLikeTheOthers(t *testing.T) {
	baseDir := t.TempDir()
	manager := testManager(t, baseDir, baseDir)
	if _, _, err := manager.NoteMachine("the interpreters live under Program Files " + ReflectionSuffix(ReflectionUnconfirmed, "r2")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.NoteAgent("coder", "the operator prefers short reports"); err != nil {
		t.Fatal(err)
	}
	block, path, err := manager.LoadMachine(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if path != manager.MachinePath() {
		t.Errorf("the machine layer loaded from %q", path)
	}
	if !strings.Contains(block, "this machine") || !strings.Contains(block, "Program Files") {
		t.Errorf("the machine layer has no heading of its own: %q", block)
	}
	// The two layers are separate files: writing one never touches the other.
	if agent, _ := manager.ReadAgent("coder"); strings.Contains(agent, "Program Files") {
		t.Errorf("a machine fact landed in the agent layer: %q", agent)
	}
	if machine, _ := manager.ReadMachine(); strings.Contains(machine, "short reports") {
		t.Errorf("an agent note landed in the machine layer: %q", machine)
	}
	if notes, err := manager.MachineNotes(); err != nil || len(notes) != 1 || notes[0].Run != "r2" {
		t.Fatalf("the machine layer does not list with its provenance: %+v %v", notes, err)
	}
}

// Item 2mw (a): DELETE BY THE NOTE'S WORDS, which is all a row has. The stored line
// also carries its provenance and the removal matches byte-exactly, so passing the
// words straight through matched nothing and the control silently did nothing. Found
// by driving the route end to end rather than by a unit test, which is why this one
// exists.
func TestANoteIsDeletedByItsWordsEvenThoughTheLineCarriesProvenance(t *testing.T) {
	baseDir := t.TempDir()
	manager := testManager(t, baseDir, baseDir)
	path := manager.MachinePath()
	if _, _, err := manager.NoteMachine("PowerShell 7 is the shell here " + ReflectionSuffix(ReflectionUnconfirmed, "r14")); err != nil {
		t.Fatal(err)
	}
	// The words alone, exactly as a row's dataset carries them.
	removed, err := manager.RemoveNote(path, "PowerShell 7 is the shell here")
	if err != nil || !removed {
		t.Fatalf("delete by words reported %v, %v", removed, err)
	}
	if notes, _ := manager.MachineNotes(); len(notes) != 0 {
		t.Fatalf("the note is still there: %+v", notes)
	}
	// And it is restorable by its words too, with the provenance intact.
	restorable, err := manager.RemovedNotes(path)
	if err != nil || len(restorable) != 1 {
		t.Fatalf("the removed note is not restorable: %+v %v", restorable, err)
	}
	if restorable[0].Reflection != ReflectionUnconfirmed || restorable[0].Run != "r14" {
		t.Errorf("the tombstone lost the provenance: %+v", restorable[0])
	}
	if restored, err := manager.RestoreNote(path, "PowerShell 7 is the shell here"); err != nil || !restored {
		t.Fatalf("restore by words reported %v, %v", restored, err)
	}
	notes, _ := manager.MachineNotes()
	if len(notes) != 1 || notes[0].Run != "r14" {
		t.Fatalf("the note did not come back whole: %+v", notes)
	}
	// A note that is not there is not an error, because another window may have acted.
	if removed, err := manager.RemoveNote(path, "something nobody ever wrote"); err != nil || removed {
		t.Errorf("removing an absent note reported %v, %v", removed, err)
	}
}

// Item 2mw (c): A LAYER WHOSE LAST NOTE WAS DELETED STILL OFFERS THE UNDO. Removing
// the last note deletes the layer file, so a listing that only looked at live files
// lost the tombstone with it and the restore control disappeared along with the note
// it would have brought back. Found by driving the route, not by a unit test.
func TestALayerThatOnlyHasATombstoneIsStillListed(t *testing.T) {
	baseDir := t.TempDir()
	manager := testManager(t, baseDir, baseDir)
	path := manager.MachinePath()
	if _, _, err := manager.NoteMachine("the only fact here " + ReflectionSuffix(ReflectionUnconfirmed, "r1")); err != nil {
		t.Fatal(err)
	}
	if removed, err := manager.RemoveNote(path, "the only fact here"); err != nil || !removed {
		t.Fatalf("remove reported %v, %v", removed, err)
	}
	// The layer file is gone, which is the pre-existing behaviour and stays.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the emptied layer file is still there: %v", err)
	}
	layers, err := manager.Layers()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, layer := range layers {
		if layer.Kind == "machine" {
			found = true
			if layer.File != "machine.md" {
				t.Errorf("the layer is named %q", layer.File)
			}
		}
		// A tombstone is never itself a layer.
		if strings.HasSuffix(layer.File, "-removed.md") {
			t.Errorf("a tombstone was listed as a layer: %q", layer.File)
		}
	}
	if !found {
		t.Fatalf("the layer with only a tombstone is not listed: %+v", layers)
	}
	if restorable, err := manager.RemovedNotes(path); err != nil || len(restorable) != 1 {
		t.Fatalf("the undo is not offered: %+v %v", restorable, err)
	}
}

// LayerPath is the only way a caller outside this package names a layer, so it refuses
// anything that is not a file directly inside the memory directory.
func TestALayerFileNameOutsideTheMemoryDirectoryIsRefused(t *testing.T) {
	baseDir := t.TempDir()
	manager := testManager(t, baseDir, baseDir)
	for _, bad := range []string{
		"", "   ", "harness.json", "machine", "sub/machine.md", `sub\machine.md`,
		"../harness.json", `..\harness.json`, `C:\Windows\win.ini`, "/etc/passwd",
		// A tombstone is not addressable: it is reached through its layer.
		"machine-removed.md",
	} {
		if _, err := manager.LayerPath(bad); err == nil {
			t.Errorf("LayerPath accepted %q", bad)
		}
	}
	got, err := manager.LayerPath("machine.md")
	if err != nil || got != manager.MachinePath() {
		t.Errorf("LayerPath(machine.md) = %q, %v", got, err)
	}
}
