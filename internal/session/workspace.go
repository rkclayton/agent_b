package session

import (
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type WriteRecord struct {
	SessionID string
	At        time.Time
}
// Item 2mn (a): THE KEY IS THE FILE, not the workspace it was reached through.
//
// It was keyed by (the writing session's own root, the path relative to that
// root). Two sessions with different scratch roots writing ONE file in a shared
// plan repository therefore landed in two different maps, and no conflict could
// ever fire between them — confirmed by reading the source before this was
// written, and by the reproduction in the item's @verify.
//
// The registry holds no persisted state, so the key changes with no migration:
// it is one in-memory map, rebuilt every launch.
type WorkspaceRegistry struct {
	mu         sync.Mutex
	lastWriter map[string]WriteRecord
}

func NewWorkspaceRegistry() *WorkspaceRegistry {
	return &WorkspaceRegistry{lastWriter: map[string]WriteRecord{}}
}

// FileKey is a file's canonical identity: its absolute, cleaned path, compared
// without case on Windows because the file system is. Two sessions that resolve
// the same file by different routes produce the same key.
func FileKey(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return strings.ToLower(filepath.ToSlash(filepath.Clean(abs)))
}

func (w *WorkspaceRegistry) LastWriter(path string) (WriteRecord, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	record, ok := w.lastWriter[FileKey(path)]
	return record, ok
}
func (w *WorkspaceRegistry) RecordWrite(path, sessionID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastWriter[FileKey(path)] = WriteRecord{SessionID: sessionID, At: time.Now().UTC()}
}
