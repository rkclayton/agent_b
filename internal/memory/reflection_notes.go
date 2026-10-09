package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"harness/internal/events"
)

// Item 2mw: REFLECTION'S NOTES BECOME THE OPERATOR'S.
//
// The mechanism already ran. Reflection recorded one summary per run and wrote one
// memory note per stated correction, and there it stopped: nothing confirmed a note,
// so every correction it ever found stayed "unconfirmed" forever and carried that word
// into every prompt that loaded it. There was no undo, no date, and no record of which
// run produced it.
//
// (b) PROVENANCE ON THE NOTE ITSELF, IN THE FORM THE LOADER ALREADY PARSES. Reflection
// wrote the marker as a PREFIX. parseNote's provenance pattern is anchored to the END
// of the line, so it never saw that marker: the bracket was part of the note's own
// words. It travelled into every prompt, it defeated the duplicate check (two notes
// differing only in provenance looked different), and it named no run. The marker now
// joins the trailing form the parser already reads:
//
//	- 2026-09-27 the installer rejects a workspace whose leaf it does not know [reflection: unconfirmed, run: r12]
//
// The date is not invented here: notePath has always written the leading day.

// The two states a reflection note can be in. Confirming does not delete and rewrite
// from scratch; it changes this one word, so the note keeps its date and its run.
const (
	ReflectionUnconfirmed = "unconfirmed"
	ReflectionConfirmed   = "confirmed"
)

// TombstoneRetention is (c)'s STATED PERIOD. A note the operator deletes, or confirms
// and then regrets, is kept this long and can be restored from the page that removed
// it. Thirty days matches the operator-files log retention already in the
// configuration, so there is one answer to how long this installation keeps things.
const TombstoneRetention = 30 * 24 * time.Hour

// ReflectionSuffix is the provenance a reflection note carries. An empty run id is
// omitted rather than written with no value, because an empty field reads as a fault
// when it is only an older note.
func ReflectionSuffix(state, runID string) string {
	fields := []string{"reflection: " + state}
	if strings.TrimSpace(runID) != "" {
		fields = append(fields, "run: "+strings.TrimSpace(runID))
	}
	return "[" + strings.Join(fields, ", ") + "]"
}

// ParseNoteLine reads one stored line into its parts. It exists so a caller outside
// this package can check what the LOADER will see, rather than re-implementing the
// line format and drifting from it.
func ParseNoteLine(line string) Note { return parseNote(line) }

// normalizedNote is (d): the comparison that decides whether a correction is one the
// layer already holds. Case and run-length of whitespace are not meaning, so two
// corrections differing only in those are one note rather than two.
func normalizedNote(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}

// SameNote reports whether two note texts say the same thing under (d)'s rule. Either
// side may arrive as a whole line or as bare words.
func SameNote(left, right string) bool {
	return normalizedNote(noteTextOf("- "+strings.TrimPrefix(strings.TrimSpace(left), "- "))) ==
		normalizedNote(noteTextOf("- "+strings.TrimPrefix(strings.TrimSpace(right), "- ")))
}

// MachinePath is (e): A THIRD LAYER, for facts about this box rather than about a
// folder or about the agent, such as the interpreter paths, the shell dialect and
// whether the service split is in place. It sits beside the other two in the same
// directory and is loaded the same way, so nothing has to be migrated: a layer is a
// file.
func (m *Manager) MachinePath() string { return filepath.Join(m.Dir(), "machine.md") }

// LoadMachine loads the machine layer for a prompt, exactly as the agent layer is
// loaded and under the same budget.
func (m *Manager) LoadMachine(ctx context.Context, connectionID string) (string, string, error) {
	return m.load(ctx, m.MachinePath(), connectionID, "Facts about this machine, gathered from earlier sessions:")
}

// ReadMachine reads it back verbatim.
func (m *Manager) ReadMachine() (string, error) { return m.readPath(m.MachinePath()) }

// NoteMachine writes one. (e) says this layer is written ONLY by reflection, which
// holds because the remember tool writes by scope and this is not one of its scopes.
func (m *Manager) NoteMachine(note string) (string, bool, error) {
	return m.notePath(m.MachinePath(), note)
}

// MachineNotes lists the layer.
func (m *Manager) MachineNotes() ([]Note, error) { return m.Notes(m.MachinePath()) }

// tombstonePath is where a removed note waits out TombstoneRetention. It sits beside
// the layer it came from, so restoring never has to guess which layer that was.
func tombstonePath(path string) string {
	extension := filepath.Ext(path)
	return strings.TrimSuffix(path, extension) + "-removed" + extension
}

// rememberRemoved appends the lines a removal dropped, each stamped with the day it
// was removed so the retention can be applied without a second store. Expired entries
// are pruned on the way through, since the file is read and written here anyway.
func (m *Manager) rememberRemoved(path string, lines []string) error {
	if len(lines) == 0 {
		return nil
	}
	tomb := tombstonePath(path)
	kept := livingTombstones(tomb)
	stamp := time.Now().UTC().Format("2006-01-02")
	for _, line := range lines {
		text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if text == "" {
			continue
		}
		// The stamp REPLACES the note's own date rather than preceding it. Two dates
		// on one line would leave the second inside the note's words, and a restore
		// would then bring back the date as part of the note and drop the trailing
		// provenance with it.
		if date := parseNote(line).Date; date != "" {
			text = strings.TrimSpace(strings.TrimPrefix(text, date))
		}
		kept = append(kept, "- "+stamp+" "+text)
	}
	if err := os.MkdirAll(filepath.Dir(tomb), 0o755); err != nil {
		return err
	}
	return os.WriteFile(tomb, []byte(strings.Join(kept, "\n")+"\n"), 0o600)
}

// livingTombstones are the removed notes still inside the retention window.
func livingTombstones(tomb string) []string {
	data, err := os.ReadFile(tomb)
	if err != nil {
		return nil
	}
	cutoff := time.Now().UTC().Add(-TombstoneRetention)
	kept := []string{}
	for _, line := range strings.Split(normalize(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		removedAt, err := time.Parse("2006-01-02", parseNote(line).Date)
		// A line with no readable date is KEPT: losing it is the one outcome the
		// tombstone exists to prevent.
		if err == nil && removedAt.Before(cutoff) {
			continue
		}
		kept = append(kept, line)
	}
	return kept
}

// RemovedNotes lists what is restorable for one layer, newest first, with the day each
// was removed as its date.
func (m *Manager) RemovedNotes(path string) ([]Note, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lines := livingTombstones(tombstonePath(path))
	out := make([]Note, 0, len(lines))
	for _, line := range lines {
		out = append(out, parseNote(line))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date > out[j].Date })
	return out, nil
}

// RestoreNote is (c)'s undo: the note goes back into the layer it came from and
// leaves the tombstone. Its original date is gone, because the tombstone records the
// day of the removal and not of the writing, so it returns dated today rather than
// carrying an invented older date.
func (m *Manager) RestoreNote(path, text string) (bool, error) {
	tomb := tombstonePath(path)
	found, err := m.takeTombstone(tomb, text)
	if err != nil || found == "" {
		return false, err
	}
	// Written through the ordinary path, so the note comes back subject to the same
	// duplicate rule as any other. Already present by another route is a success.
	if _, _, err := m.notePath(path, found); err != nil {
		return false, err
	}
	return true, nil
}

// takeTombstone removes one entry from a tombstone and returns its words.
func (m *Manager) takeTombstone(tomb, text string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lines := livingTombstones(tomb)
	kept := make([]string, 0, len(lines))
	found := ""
	for _, line := range lines {
		if found == "" && SameNote(line, text) {
			// The whole stored note, provenance included: a restore returns what was
			// removed, not a reconstruction of its words.
			found = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
			if date := parseNote(line).Date; date != "" {
				found = strings.TrimSpace(strings.TrimPrefix(found, date))
			}
			continue
		}
		kept = append(kept, line)
	}
	if found == "" {
		return "", nil
	}
	if len(kept) == 0 {
		if err := os.Remove(tomb); err != nil && !os.IsNotExist(err) {
			return "", err
		}
		return found, nil
	}
	if err := os.WriteFile(tomb, []byte(strings.Join(kept, "\n")+"\n"), 0o600); err != nil {
		return "", err
	}
	return found, nil
}

// ConfirmNote is (a): confirming REWRITES the note without the unconfirmed marker and
// records the day it was confirmed. An editReplacement replaces the note's words with
// the operator's, which is the same operation with one more change. The line keeps its
// original date and its run id, because those are facts about when the machine noticed
// the thing, not about when the operator agreed with it.
func (m *Manager) ConfirmNote(path, text, editReplacement string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	lines := strings.Split(strings.TrimRight(normalize(string(data)), "\n"), "\n")
	changed := false
	for index, line := range lines {
		if changed || strings.TrimSpace(line) == "" {
			continue
		}
		note := parseNote(line)
		if !SameNote(note.Text, text) {
			continue
		}
		words := note.Text
		if strings.TrimSpace(editReplacement) != "" {
			words = strings.TrimSpace(editReplacement)
		}
		fields := []string{"reflection: " + ReflectionConfirmed + " " + time.Now().UTC().Format("2006-01-02")}
		if note.Run != "" {
			fields = append(fields, "run: "+note.Run)
		}
		date := note.Date
		if date == "" {
			date = time.Now().UTC().Format("2006-01-02")
		}
		lines[index] = fmt.Sprintf("- %s %s [%s]", date, words, strings.Join(fields, ", "))
		changed = true
	}
	if !changed {
		return false, nil
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return false, err
	}
	return true, os.Rename(temporary, path)
}

// The controls act on a LAYER BY PATH, because reflection writes its notes to the
// FOLDER layer (one file per workspace) while the pre-existing removal route is the
// agent layer's. Keying on the path means one implementation serves every layer, and
// these three wrappers are what the agent layer's own tests exercise.
func (m *Manager) RemovedAgent(agentID string) ([]Note, error) {
	return m.RemovedNotes(m.TargetPath(agentID))
}
func (m *Manager) RestoreAgent(agentID, text string) (bool, error) {
	return m.RestoreNote(m.TargetPath(agentID), text)
}
func (m *Manager) ConfirmAgent(agentID, text, editReplacement string) (bool, error) {
	return m.ConfirmNote(m.TargetPath(agentID), text, editReplacement)
}

// RemoveNote is exact-note removal for any layer, named by its path. The agent layer
// already had this; the folder layer, which is where reflection actually writes, did
// not. Both go through DropSessionWrites, so both leave a tombstone.
func (m *Manager) RemoveNote(path, note string) (bool, error) {
	// The caller names the note's WORDS, because that is what a row shows. The stored
	// line also carries its provenance, and DropSessionWrites matches byte-exactly, so
	// the words are resolved to the stored line first. Without this the delete control
	// silently did nothing: it matched a line that does not exist.
	stored, err := m.storedLineFor(path, note)
	if err != nil || stored == "" {
		return false, err
	}
	count, err := m.DropSessionWrites([]events.MemoryWrite{{Path: path, Note: stored, Target: "folder"}})
	return count > 0, err
}

// storedLineFor finds the note in a layer whose words match, and returns the stored
// note as DropSessionWrites compares it: the line without its leading date.
func (m *Manager) storedLineFor(path, note string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(normalize(string(data)), "\n") {
		plain := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if plain == "" {
			continue
		}
		if !SameNote(line, note) {
			continue
		}
		if date := parseNote(line).Date; date != "" {
			plain = strings.TrimSpace(strings.TrimPrefix(plain, date))
		}
		return plain, nil
	}
	return "", nil
}

// Layer is one memory file: which kind it is, the bare file name that names it, and
// its path. The folder layer's file name is derived from a hash of the folder, so the
// folder itself cannot be recovered from it — which is why an action names the FILE
// rather than the folder, and why LayerPath validates that name instead of trusting a
// path from a caller.
type Layer struct {
	Kind string `json:"kind"`
	File string `json:"file"`
	Path string `json:"path"`
}

// Layers lists the memory files in the directory: the machine layer, every agent
// layer, and every folder layer. Tombstones are not layers.
func (m *Manager) Layers() ([]Layer, error) {
	entries, err := os.ReadDir(m.Dir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	// A layer whose LAST note was removed has no file left, because the removal deletes
	// an empty layer. Its tombstone is still there and its undo must still be offered,
	// so a tombstone with no live counterpart contributes the layer it came from.
	names := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".md") {
			continue
		}
		if strings.HasSuffix(name, "-removed.md") {
			names[strings.TrimSuffix(name, "-removed.md")+".md"] = true
			continue
		}
		names[name] = true
	}
	out := []Layer{}
	for name := range names {
		kind := "folder"
		switch {
		case name == "machine.md":
			kind = "machine"
		case strings.HasPrefix(name, "agent-"):
			kind = "agent"
		}
		out = append(out, Layer{Kind: kind, File: name, Path: filepath.Join(m.Dir(), name)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].File < out[j].File
	})
	return out, nil
}

// LayerPath resolves a bare layer file name to its path. A name carrying a separator,
// a drive, a parent reference or any extension but .md is refused: the only paths this
// package will act on from outside are the files in its own directory.
func (m *Manager) LayerPath(file string) (string, error) {
	name := strings.TrimSpace(file)
	// Both separators are refused on every platform, not just the one this is built
	// for: on Linux filepath.Base leaves a backslash alone, so a Windows-shaped
	// nested name would pass a check that only asked filepath. CI caught exactly that.
	if name == "" || strings.ContainsAny(name, "/\\") || name != filepath.Base(name) ||
		!strings.HasSuffix(name, ".md") || strings.HasSuffix(name, "-removed.md") {
		return "", fmt.Errorf("not a memory layer file: %q", file)
	}
	path := filepath.Join(m.Dir(), name)
	// Belt and braces: the joined path must still be directly inside the directory.
	if filepath.Dir(path) != filepath.Clean(m.Dir()) {
		return "", fmt.Errorf("not a memory layer file: %q", file)
	}
	return path, nil
}
