package projection

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Item 2m5 (b): STARTUP DOES NOT PARSE WHAT IT DOES NOT NEED.
//
// The operator's launch at 08:30 on 2026-09-28 took 8,513 ms to listen, of which
// `parse and project journals` was 6,797 ms — 80% — and a cold one before it took
// 16,435 ms of 21,018 ms. His `chats/` holds 34 files and 215,039,674 bytes, two of
// them 56 MB and 48 MB, and startup projected every byte of it before it could
// answer on a port.
//
// MEASURED, on a copy of his own chats, before choosing a mechanism:
//
//	PROJECT ALL: 33 journals, 218,150,298 bytes, 6.244s
//	  s29  journal 56,945,309  snapshot 1,788,347  decode 10ms
//	  s9   journal 48,566,796  snapshot   922,425  decode  4ms
//	SNAPSHOT TOTAL 6,723,596 bytes; DECODING ALL SNAPSHOTS 45ms
//
// That is the whole argument for this file. The PROJECTED STATE of every chat he
// has is 6.7 MB and reads back in 45 ms, while the journals it came from are 218 MB
// and take 6.2 seconds. The projection is bounded by what a chat SHOWS — its
// messages, its chat entries, its timeline — and a journal is bounded by nothing,
// because it records every delta that ever arrived. So the projection is what
// startup should read, and the journal is what it should read only when the
// projection it has is not the projection that journal would produce.
//
// The cache records HOW FAR into the journal a snapshot was projected. A chat that
// has not grown since is read whole from its snapshot; a chat that has grown — and
// every restored chat grows, because restoring one writes its own record — is
// carried forward by projecting ONLY THE RECORDS PAST THAT OFFSET onto the state
// that was stored. A journal that shrank, or whose offset no longer lands on a
// record boundary, is projected from the beginning, which is what every launch did
// before this existed. Nothing here writes, renames or deletes anything under
// `chats/` — item 2m5 (c).
const cacheSchema = 1

// cacheEntry is one chat's projected state with the identity of the journal it was
// projected from. Both halves matter: a snapshot without the journal's size and
// time cannot be known to be current, and trusting a stale one would show the
// operator a chat missing its newest turns.
type cacheEntry struct {
	Schema   int      `json:"schema"`
	Journal  string   `json:"journal"`
	Offset   int64    `json:"offset"`
	Snapshot Snapshot `json:"snapshot"`
}

// CacheEntryPath is where one journal's snapshot lives. The name is the journal's
// own base name, so the mapping is obvious to anyone reading the folder, and the
// directory is the caller's to choose — it is never inside `chats/`.
func CacheEntryPath(dir, journal string) string {
	base := strings.TrimSuffix(filepath.Base(journal), filepath.Ext(journal))
	return filepath.Join(dir, base+".json")
}

// LoadCachedSnapshot returns a chat's state without projecting its whole journal,
// and false when it cannot. False is never an error the caller must handle: it
// means "project this one", which is what startup did for every journal before.
//
// A journal that has grown since the snapshot was stored is not a miss. The records
// past the stored offset are projected onto the stored state, so the launch after a
// long chat is used pays for what was added to it and not for what it holds.
func LoadCachedSnapshot(dir, journal string) (Snapshot, bool) {
	if strings.TrimSpace(dir) == "" {
		return Snapshot{}, false
	}
	info, err := os.Stat(journal)
	if err != nil {
		return Snapshot{}, false
	}
	raw, err := os.ReadFile(CacheEntryPath(dir, journal))
	if err != nil {
		return Snapshot{}, false
	}
	var entry cacheEntry
	if json.Unmarshal(raw, &entry) != nil {
		return Snapshot{}, false
	}
	// The journal a snapshot names is the one it must be used for. A cache folder
	// copied between installations, or a chat renamed on disk, is a miss.
	if entry.Schema != cacheSchema || !strings.EqualFold(filepath.Base(entry.Journal), filepath.Base(journal)) {
		return Snapshot{}, false
	}
	if entry.Offset <= 0 || entry.Offset > info.Size() {
		return Snapshot{}, false
	}
	state := entry.Snapshot
	state.LogPath = journal
	if entry.Offset == info.Size() {
		return state, true
	}
	tail, _, err := projectTail(journal, entry.Offset, state)
	if err != nil {
		return Snapshot{}, false
	}
	tail.LogPath = journal
	return tail, true
}

// projectTail applies the records after an offset to a state that was projected up
// to it. The projector is the same one a full parse uses — there is no second
// implementation of what a record means, which is the rule this product keeps
// everywhere it replays.
func projectTail(journal string, from int64, state Snapshot) (Snapshot, int64, error) {
	file, err := os.Open(journal)
	if err != nil {
		return Snapshot{}, 0, err
	}
	defer file.Close()
	if _, err := file.Seek(from, io.SeekStart); err != nil {
		return Snapshot{}, 0, err
	}
	records, consumed, err := read(filepath.Base(journal), file, 0)
	if err != nil {
		return Snapshot{}, 0, err
	}
	for _, record := range records {
		record.Cursor.Offset += from
		next, nextErr := NextState(state, record)
		if nextErr != nil {
			return Snapshot{}, 0, fmt.Errorf("project %s at byte %d: %w", journal, record.Cursor.Offset, nextErr)
		}
		state = next
	}
	return state, from + consumed, nil
}

// ProjectTailPatches folds only complete records after from onto state and returns
// the wire patches for those records. A reconnect uses it with the last snapshot it
// actually delivered, so it does not resend the retained store.
func ProjectTailPatches(journal string, from int64, state Snapshot) (Snapshot, []Patch, error) {
	file, err := os.Open(journal)
	if err != nil {
		return Snapshot{}, nil, err
	}
	defer file.Close()
	if _, err := file.Seek(from, io.SeekStart); err != nil {
		return Snapshot{}, nil, err
	}
	records, _, err := read(filepath.Base(journal), file, 0)
	if err != nil {
		return Snapshot{}, nil, err
	}
	patches := make([]Patch, 0, len(records))
	for _, record := range records {
		record.Cursor.Offset += from
		next, patch, nextErr := Next(state, record)
		if nextErr != nil {
			return Snapshot{}, nil, fmt.Errorf("project %s at byte %d: %w", journal, record.Cursor.Offset, nextErr)
		}
		state = next
		if len(patch.Operations) > 0 {
			patches = append(patches, patch)
		}
	}
	return state, patches, nil
}

// StoreCachedSnapshot records a projection and the offset it was projected through.
// A failure to write is returned for the caller to log and ignore: the product must
// start whether or not it can cache, and the next launch simply parses again.
func StoreCachedSnapshot(dir, journal string, snapshot Snapshot, offset int64) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("projection cache: no directory")
	}
	if offset <= 0 {
		return fmt.Errorf("projection cache: refusing to record %s at offset %d", filepath.Base(journal), offset)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	encoded, err := json.Marshal(cacheEntry{
		Schema:   cacheSchema,
		Journal:  filepath.Base(journal),
		Offset:   offset,
		Snapshot: snapshot,
	})
	if err != nil {
		return err
	}
	final := CacheEntryPath(dir, journal)
	temporary := final + ".part"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return err
	}
	// Renamed into place, so a launch that dies mid-write leaves the previous
	// snapshot rather than a half-written one that would have to be detected.
	if err := os.Rename(temporary, final); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("publish projection cache: %w", err)
	}
	return nil
}

// PruneCachedSnapshots removes entries for journals that are no longer there. A
// deleted chat leaves its journal deleted by the code that owns deletion; this
// only stops the cache folder growing without bound beside it.
func PruneCachedSnapshots(dir string, journals []string) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	keep := map[string]bool{}
	for _, journal := range journals {
		keep[strings.ToLower(filepath.Base(CacheEntryPath(dir, journal)))] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if !keep[strings.ToLower(entry.Name())] {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
