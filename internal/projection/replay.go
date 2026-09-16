package projection

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"harness/internal/events"
)

type ReplayPatch struct {
	TS    string `json:"ts"`
	Patch Patch  `json:"patch"`
}

type Replay struct {
	Initial  map[string]Snapshot
	Sessions map[string]Snapshot
	Patches  []ReplayPatch
	Archives map[string]*events.HistoryArchive
}

func LoadReplay(paths []string) (*Replay, error) {
	result := &Replay{Initial: map[string]Snapshot{}, Sessions: map[string]Snapshot{}, Patches: []ReplayPatch{}, Archives: map[string]*events.HistoryArchive{}}
	used := map[string]bool{}
	for _, rawPath := range paths {
		path := strings.TrimSpace(rawPath)
		if path == "" {
			continue
		}
		records, _, err := ReadFile(path, 0)
		if err != nil {
			return nil, fmt.Errorf("replay %s: %w", path, err)
		}
		if len(records) == 0 {
			return nil, fmt.Errorf("replay %s: no events", path)
		}
		oldID := sessionID(records)
		id := oldID
		if id == "" {
			id = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		}
		base := id
		for suffix := 2; used[id]; suffix++ {
			id = fmt.Sprintf("%s-%d", base, suffix)
		}
		used[id] = true
		state := Empty(id)
		archive := events.NewHistoryArchive()
		if predecessor, ok := predecessorCursor(records); ok {
			previousPath := filepath.Join(filepath.Dir(path), predecessor.Generation)
			previous, _, previousErr := ProjectFile(previousPath, predecessor.Offset)
			if previousErr != nil {
				return nil, fmt.Errorf("replay predecessor %s: %w", previousPath, previousErr)
			}
			state = previous
			state.ID = id
		}
		state.LogPath = path
		result.Initial[id] = state
		for _, record := range records {
			record.Event.SessionID = id
			archive.Record(record.Event)
			next, patch, nextErr := Next(state, record)
			if nextErr != nil {
				return nil, fmt.Errorf("replay %s at byte %d: %w", path, record.Cursor.Offset, nextErr)
			}
			next.LogPath = path
			state = next
			result.Patches = append(result.Patches, ReplayPatch{TS: record.Event.TS, Patch: patch})
		}
		result.Sessions[id] = state
		result.Archives[id] = archive
	}
	if len(result.Patches) == 0 {
		return nil, fmt.Errorf("replay: provide at least one JSONL path")
	}
	sort.SliceStable(result.Patches, func(i, j int) bool {
		if result.Patches[i].Patch.SessionID == result.Patches[j].Patch.SessionID {
			return result.Patches[i].Patch.Cursor.Offset < result.Patches[j].Patch.Cursor.Offset
		}
		left, _ := time.Parse(time.RFC3339Nano, result.Patches[i].TS)
		right, _ := time.Parse(time.RFC3339Nano, result.Patches[j].TS)
		return left.Before(right)
	})
	return result, nil
}

// Keep the import anchored in this package's public replay boundary.
var _ events.Event
