package events

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Writers struct {
	mu         sync.Mutex
	dir, start string
	global     *os.File
	sessions   map[string]*os.File
	paths      map[string]string
	sizes      map[string]int64
	history    map[string]*historyIndex
}

func NewWriters(dir string) (*Writers, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	stamp := time.Now().UTC().Format("20060102T150405.000Z")
	file, err := os.OpenFile(filepath.Join(dir, "Agent_b-"+stamp+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &Writers{dir: dir, start: stamp, global: file, sessions: map[string]*os.File{}, paths: map[string]string{}, sizes: map[string]int64{}, history: map[string]*historyIndex{}}, nil
}
func (w *Writers) OpenSession(id string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if old := w.sessions[id]; old != nil {
		if err := old.Sync(); err != nil {
			return "", err
		}
		if err := old.Close(); err != nil {
			return "", err
		}
	}
	path := filepath.Join(w.dir, fmt.Sprintf("%s-%s.jsonl", id, time.Now().UTC().Format("20060102T150405.000Z")))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return "", err
	}
	w.sessions[id] = file
	w.paths[id] = path
	w.sizes[id] = info.Size()
	w.history[id] = newHistoryIndex()
	return path, nil
}
func (w *Writers) CloseSession(id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	file := w.sessions[id]
	if file == nil {
		return nil
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if closeErr == nil {
		delete(w.sessions, id)
		delete(w.paths, id)
		delete(w.sizes, id)
		delete(w.history, id)
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
func (w *Writers) Write(event Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	file := w.global
	if event.SessionID != "" && w.sessions[event.SessionID] != nil {
		file = w.sessions[event.SessionID]
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	line := append(data, '\n')
	offset := w.sizes[event.SessionID]
	written, err := file.Write(line)
	if event.SessionID != "" && file == w.sessions[event.SessionID] {
		w.sizes[event.SessionID] += int64(written)
	}
	if err != nil {
		return err
	}
	if written != len(line) {
		return io.ErrShortWrite
	}
	if index := w.history[event.SessionID]; index != nil {
		index.record(event, historyLocation{path: w.paths[event.SessionID], offset: offset, length: written})
	}
	if event.Type == "run.stopped" {
		return file.Sync()
	}
	return nil
}

func (w *Writers) ResolveHistory(sessionID, ref string) (HistoryLookup, error) {
	w.mu.Lock()
	index := w.history[sessionID]
	if index == nil {
		w.mu.Unlock()
		return HistoryLookup{}, fmt.Errorf("history is not available for this session")
	}
	lookup, err := index.resolve(ref, func(record historyRecord) (Message, error) {
		return Message{}, nil
	})
	if err != nil || lookup.Kind == "manifest" {
		w.mu.Unlock()
		return lookup, err
	}
	record := index.records[ref]
	w.mu.Unlock()
	message, err := readHistoryMessage(record.location)
	if err != nil {
		return HistoryLookup{}, err
	}
	if message.ID != ref {
		return HistoryLookup{}, fmt.Errorf("recorded history ref mismatch: got %q, want %q", message.ID, ref)
	}
	lookup.Message = message
	return lookup, nil
}

func (h *historyIndex) record(event Event, location historyLocation) {
	data := replayMap(event.Data)
	switch event.Type {
	case MessageAppended:
		var wrapper struct {
			Message Message `json:"message"`
		}
		if decodeReplay(event.Data, &wrapper) == nil {
			h.append(wrapper.Message, location, false)
		}
	case MessageUpdated:
		h.update(replayString(data["id"]), replayMap(data["patch"]))
	case Compaction:
		if replayString(data["kind"]) == "summarize" {
			h.compact(replayString(data["summary_message_id"]), replayStrings(data["affected_ids"]))
		}
	}
}

func readHistoryMessage(location historyLocation) (Message, error) {
	file, err := os.Open(location.path)
	if err != nil {
		return Message{}, err
	}
	defer file.Close()
	data := make([]byte, location.length)
	if _, err := file.ReadAt(data, location.offset); err != nil && err != io.EOF {
		return Message{}, err
	}
	var event Event
	if err := json.Unmarshal(data, &event); err != nil {
		return Message{}, err
	}
	if event.Type != MessageAppended {
		return Message{}, fmt.Errorf("recorded history location is %q, not %q", event.Type, MessageAppended)
	}
	var wrapper struct {
		Message Message `json:"message"`
	}
	if err := decodeReplay(event.Data, &wrapper); err != nil {
		return Message{}, err
	}
	return wrapper.Message, nil
}
func (w *Writers) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	var first error
	for _, file := range append([]*os.File{w.global}, mapFiles(w.sessions)...) {
		if file == nil {
			continue
		}
		if err := file.Sync(); err != nil && first == nil {
			first = err
		}
		if err := file.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
func mapFiles(values map[string]*os.File) []*os.File {
	out := make([]*os.File, 0, len(values))
	for _, file := range values {
		out = append(out, file)
	}
	return out
}
