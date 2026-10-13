package tools

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	fileUndoRuns  = 20
	fileUndoBytes = int64(256 << 20)
)

type undoRecord struct {
	Kind, Path, Display, Blob, Digest string
	Existed                           bool
	Mode                              uint32
	Bytes                             int64
	At                                int64
}
type undoEntry struct {
	Before undoRecord
	After  undoRecord
	Saved  bool
}
type undoRun struct {
	loaded  bool
	entries map[string]*undoEntry
}
type FileUndoResult struct {
	Restored []string `json:"restored"`
	Skipped  []string `json:"skipped"`
}
type FileUndoStore struct {
	mu                sync.Mutex
	root              string
	maxRuns           int
	maxBytes          int64
	now               func() time.Time
	runs              map[string]*undoRun
	readFile          func(string) ([]byte, error)
	remove, removeAll func(string) error
}

func NewFileUndoStore(root string) *FileUndoStore {
	return &FileUndoStore{root: root, maxRuns: fileUndoRuns, maxBytes: fileUndoBytes, now: time.Now, runs: map[string]*undoRun{}, readFile: os.ReadFile, remove: os.Remove, removeAll: os.RemoveAll}
}

func (s *FileUndoStore) SetRoot(root string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.root, s.runs = root, map[string]*undoRun{}
}

func (s *FileUndoStore) runDir(sessionID, runID string) string {
	return filepath.Join(s.root, "file-undo", safeStoreName(sessionID), safeStoreName(runID))
}
func safeStoreName(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}
func undoKey(sessionID, runID string) string { return sessionID + "\x00" + runID }

func (s *FileUndoStore) loadLocked(sessionID, runID string) (*undoRun, error) {
	key := undoKey(sessionID, runID)
	if run := s.runs[key]; run != nil && run.loaded {
		return run, nil
	}
	run := &undoRun{loaded: true, entries: map[string]*undoEntry{}}
	path := filepath.Join(s.runDir(sessionID, runID), "changes.jsonl")
	file, err := os.Open(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		defer file.Close()
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			var record undoRecord
			if json.Unmarshal(scanner.Bytes(), &record) != nil || record.Path == "" {
				continue
			}
			entry := run.entries[record.Path]
			if entry == nil {
				entry = &undoEntry{}
				run.entries[record.Path] = entry
			}
			if record.Kind == "before" {
				entry.Before = record
			}
			if record.Kind == "after" {
				entry.After, entry.Saved = record, true
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	}
	s.runs[key] = run
	return run, nil
}

func (s *FileUndoStore) appendLocked(sessionID, runID string, record undoRecord) error {
	dir := s.runDir(sessionID, runID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, "changes.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	data, _ := json.Marshal(record)
	if _, err = file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	now := s.now()
	return os.Chtimes(dir, now, now)
}

func (s *FileUndoStore) Prepare(sessionID, runID, path, display string) error {
	if s == nil || s.root == "" || sessionID == "" || runID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run, err := s.loadLocked(sessionID, runID)
	if err != nil {
		return err
	}
	if run.entries[path] != nil {
		return nil
	}
	record := undoRecord{Kind: "before", Path: path, Display: display, At: s.now().UnixNano()}
	info, statErr := os.Stat(path)
	if statErr == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("undo snapshot refused non-file %s", display)
		}
		data, readErr := s.readFile(path)
		if readErr != nil {
			return readErr
		}
		record.Existed, record.Mode, record.Bytes = true, uint32(info.Mode().Perm()), int64(len(data))
		sum := sha256.Sum256([]byte(path))
		record.Blob = hex.EncodeToString(sum[:]) + ".before"
		if err := s.makeRoomLocked(record.Bytes+4096, s.runDir(sessionID, runID)); err != nil {
			return err
		}
		if err := atomicBytes(filepath.Join(s.runDir(sessionID, runID), record.Blob), data, 0o600); err != nil {
			return err
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	if err := s.appendLocked(sessionID, runID, record); err != nil {
		return err
	}
	run.entries[path] = &undoEntry{Before: record}
	return s.pruneRunsLocked(sessionID)
}

func (s *FileUndoStore) Commit(sessionID, runID, path string) error {
	if s == nil || s.root == "" || sessionID == "" || runID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run, err := s.loadLocked(sessionID, runID)
	if err != nil {
		return err
	}
	entry := run.entries[path]
	if entry == nil {
		return fmt.Errorf("undo snapshot missing")
	}
	record := undoRecord{Kind: "after", Path: path, Display: entry.Before.Display, At: s.now().UnixNano()}
	data, err := s.readFile(path)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	record.Digest, record.Existed = hex.EncodeToString(sum[:]), true
	if err := s.appendLocked(sessionID, runID, record); err != nil {
		return err
	}
	entry.After, entry.Saved = record, true
	return nil
}

func (s *FileUndoStore) Undo(sessionID, runID string) (FileUndoResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, err := s.loadLocked(sessionID, runID)
	if err != nil {
		return FileUndoResult{}, err
	}
	paths := make([]string, 0, len(run.entries))
	for path, entry := range run.entries {
		if entry.Saved {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	result := FileUndoResult{}
	for _, path := range paths {
		entry := run.entries[path]
		current, readErr := s.readFile(path)
		matches := readErr == nil && digestBytes(current) == entry.After.Digest
		if !matches {
			result.Skipped = append(result.Skipped, entry.Before.Display)
			continue
		}
		if entry.Before.Existed {
			before, readErr := s.readFile(filepath.Join(s.runDir(sessionID, runID), entry.Before.Blob))
			if readErr != nil {
				return result, readErr
			}
			if err := atomicBytes(path, before, os.FileMode(entry.Before.Mode)); err != nil {
				return result, err
			}
		} else if err := s.remove(path); err != nil && !os.IsNotExist(err) {
			return result, err
		}
		result.Restored = append(result.Restored, entry.Before.Display)
	}
	return result, nil
}

func digestBytes(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func atomicBytes(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".agentb-undo-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if mode != 0 {
		_ = temp.Chmod(mode)
	}
	if _, err = temp.Write(data); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return AtomicReplace(name, path)
}

func (s *FileUndoStore) DeleteChat(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.runs {
		if strings.HasPrefix(key, sessionID+"\x00") {
			delete(s.runs, key)
		}
	}
	return s.removeAll(filepath.Join(s.root, "file-undo", safeStoreName(sessionID)))
}

func (s *FileUndoStore) pruneRunsLocked(sessionID string) error {
	parent := filepath.Join(s.root, "file-undo", safeStoreName(sessionID))
	entries, err := os.ReadDir(parent)
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	type namedTime struct {
		name string
		at   time.Time
	}
	runs := make([]namedTime, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			info, _ := entry.Info()
			runs = append(runs, namedTime{entry.Name(), info.ModTime()})
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].at.Before(runs[j].at) })
	for len(runs) > s.maxRuns {
		removed := filepath.Join(parent, runs[0].name)
		if err := s.removeAll(removed); err != nil {
			return err
		}
		for key := range s.runs {
			parts := strings.SplitN(key, "\x00", 2)
			if len(parts) == 2 && filepath.Clean(s.runDir(parts[0], parts[1])) == filepath.Clean(removed) {
				delete(s.runs, key)
			}
		}
		runs = runs[1:]
	}
	return nil
}

func (s *FileUndoStore) makeRoomLocked(need int64, protect string) error {
	type runSize struct {
		path  string
		at    time.Time
		bytes int64
	}
	var runs []runSize
	var total int64
	base := filepath.Join(s.root, "file-undo")
	_ = filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if !info.IsDir() {
			total += info.Size()
		}
		if info.IsDir() && filepath.Dir(filepath.Dir(path)) == base {
			runs = append(runs, runSize{path: path, at: info.ModTime()})
		}
		return nil
	})
	if need > s.maxBytes {
		return fmt.Errorf("file is larger than the 256 MiB undo store")
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].at.Before(runs[j].at) })
	for total+need > s.maxBytes && len(runs) > 0 {
		run := runs[0]
		runs = runs[1:]
		if filepath.Clean(run.path) == filepath.Clean(protect) {
			continue
		}
		_ = filepath.Walk(run.path, func(_ string, info os.FileInfo, err error) error {
			if err == nil && info != nil && !info.IsDir() {
				run.bytes += info.Size()
			}
			return nil
		})
		if err := s.removeAll(run.path); err != nil {
			return err
		}
		for key := range s.runs {
			parts := strings.SplitN(key, "\x00", 2)
			if len(parts) == 2 && filepath.Clean(s.runDir(parts[0], parts[1])) == filepath.Clean(run.path) {
				delete(s.runs, key)
			}
		}
		total -= run.bytes
	}
	if total+need > s.maxBytes {
		return fmt.Errorf("256 MiB undo store is full")
	}
	return nil
}
