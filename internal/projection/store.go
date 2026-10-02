package projection

import (
	"fmt"
	"sort"
	"sync"

	"harness/internal/events"
)

// Store is a discardable live cache over JSONL. It has no persisted state: the first
// snapshot projects through a captured record boundary; later durable records are
// folded once and emitted as versioned projection patches.
type Store struct {
	mu          sync.RWMutex
	cache       *Cache
	states      map[string]Snapshot
	sources     map[string]events.LogCursor
	initialized map[string]bool
	stale       map[string]string
	subscribers map[int]chan Patch
	next        int
}

func NewStore() *Store {
	return &Store{
		cache: NewCache(), states: map[string]Snapshot{}, sources: map[string]events.LogCursor{},
		initialized: map[string]bool{}, stale: map[string]string{}, subscribers: map[int]chan Patch{},
	}
}

func (s *Store) Apply(event events.Event, cursor events.LogCursor) {
	if event.SessionID == "" || cursor.Offset == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sources[event.SessionID] = cursor
	if !s.initialized[event.SessionID] {
		if len(s.subscribers) == 0 || event.Type != events.SessionCreated {
			return
		}
		s.states[event.SessionID] = Empty(event.SessionID)
		s.initialized[event.SessionID] = true
	}
	previous := s.states[event.SessionID]
	if previous.Cursor.Generation == cursor.Generation && previous.Cursor.Offset >= cursor.Offset {
		return
	}
	record := Record{Cursor: fromLogCursor(cursor), Event: event}
	next, patch, err := nextLive(previous, record)
	if err != nil {
		s.stale[event.SessionID] = err.Error()
		return
	}
	next.Stale, next.StaleReason = false, ""
	delete(s.stale, event.SessionID)
	s.states[event.SessionID] = next
	s.broadcastLocked(patch)
}

func (s *Store) MarkStale(event events.Event, err error) {
	if event.SessionID == "" || err == nil {
		return
	}
	s.mu.Lock()
	s.stale[event.SessionID] = fmt.Sprintf("event %s was not appended: %v", event.Type, err)
	for id, ch := range s.subscribers {
		delete(s.subscribers, id)
		close(ch)
	}
	s.mu.Unlock()
}

func (s *Store) Snapshot(sources map[string]events.LogCursor) (map[string]Snapshot, error) {
	s.mu.RLock()
	if s.snapshotCurrentLocked(sources) {
		result := s.snapshotValuesLocked(sources)
		s.mu.RUnlock()
		return result, nil
	}
	s.mu.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked(sources)
}

// Sources returns the last durably appended boundary already folded into the
// store. A live reader uses this cut rather than contending with the journal
// writer for a boundary which has not reached projection yet.
func (s *Store) Sources() map[string]events.LogCursor {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]events.LogCursor, len(s.sources))
	for id, source := range s.sources {
		result[id] = source
	}
	return result
}

// CurrentSnapshot takes the already-folded live cut in one read section. Each
// member of that cut came from a completed durable append; a concurrently
// appended event appears wholly in this answer or wholly in the next one.
func (s *Store) CurrentSnapshot() map[string]Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]Snapshot, len(s.states))
	for id, state := range s.states {
		if !s.initialized[id] {
			continue
		}
		if reason := s.stale[id]; reason != "" {
			state.Stale, state.StaleReason = true, reason
		}
		result[id] = SnapshotForRead(state)
	}
	return result
}

func (s *Store) snapshotCurrentLocked(sources map[string]events.LogCursor) bool {
	for id, source := range sources {
		state, ok := s.states[id]
		if !ok || !s.initialized[id] || state.Cursor.Generation != source.Generation || state.Cursor.Offset < source.Offset {
			return false
		}
	}
	return true
}

func (s *Store) snapshotValuesLocked(sources map[string]events.LogCursor) map[string]Snapshot {
	result := make(map[string]Snapshot, len(sources))
	for id := range sources {
		state := s.states[id]
		if reason := s.stale[id]; reason != "" {
			state.Stale, state.StaleReason = true, reason
		}
		result[id] = SnapshotForRead(state)
	}
	return result
}

func (s *Store) Delete(sessionID string) {
	s.mu.Lock()
	delete(s.states, sessionID)
	delete(s.sources, sessionID)
	delete(s.initialized, sessionID)
	delete(s.stale, sessionID)
	s.cache.Clear()
	s.mu.Unlock()
}

// SubscribeSnapshot establishes one atomic cut: the subscriber is registered while
// the captured durable offsets are projected. Apply blocks until the snapshot is ready,
// so every later patch is strictly after its cursor.
func (s *Store) SubscribeSnapshot(sources map[string]events.LogCursor) (map[string]Snapshot, <-chan Patch, func(), error) {
	s.mu.Lock()
	id := s.next
	s.next++
	ch := make(chan Patch, 128)
	s.subscribers[id] = ch
	values, err := s.snapshotLocked(sources)
	if err != nil {
		delete(s.subscribers, id)
		close(ch)
		s.mu.Unlock()
		return nil, nil, func() {}, err
	}
	s.mu.Unlock()
	var once sync.Once
	return values, ch, func() {
		once.Do(func() {
			s.mu.Lock()
			if current, ok := s.subscribers[id]; ok {
				delete(s.subscribers, id)
				close(current)
			}
			s.mu.Unlock()
		})
	}, nil
}

func (s *Store) snapshotLocked(sources map[string]events.LogCursor) (map[string]Snapshot, error) {
	for id, source := range sources {
		known := s.sources[id]
		if known.Generation != source.Generation || known.Offset < source.Offset {
			s.sources[id] = source
		}
	}
	result := map[string]Snapshot{}
	ids := make([]string, 0, len(sources))
	for id := range sources {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		source := s.sources[id]
		state := s.states[id]
		if !s.initialized[id] || state.Cursor.Generation != source.Generation || state.Cursor.Offset != source.Offset {
			projected, err := s.cache.ProjectFile(source.Path, source.Offset)
			if err != nil {
				return nil, fmt.Errorf("project session %s: %w", id, err)
			}
			// A snapshot never advances the broadcast cursor. It may seed a session
			// nobody has been sent yet; once one has been, only Apply moves it, so a
			// record appended but not yet folded still reaches every subscriber as a
			// patch. A client holding this snapshot resyncs on the cursor mismatch.
			if !s.initialized[id] {
				s.states[id] = projected
				s.initialized[id] = true
			}
			state = projected
		}
		if reason := s.stale[id]; reason != "" {
			state.Stale, state.StaleReason = true, reason
			if s.states[id].Cursor == state.Cursor {
				s.states[id] = state
			}
		}
		result[id] = state
	}
	return cloneSnapshots(result), nil
}

func (s *Store) broadcastLocked(patch Patch) {
	for id, ch := range s.subscribers {
		select {
		case ch <- patch:
		default:
			// A dropped patch would make the client silently wrong. Closing forces
			// EventSource to reconnect and receive a fresh atomic snapshot.
			delete(s.subscribers, id)
			close(ch)
		}
	}
}

func fromLogCursor(value events.LogCursor) Cursor {
	return Cursor{Generation: value.Generation, Offset: value.Offset}
}

func cloneSnapshots(values map[string]Snapshot) map[string]Snapshot {
	result := make(map[string]Snapshot, len(values))
	for id, value := range values {
		result[id] = SnapshotForRead(value)
	}
	return result
}

// SnapshotForRead isolates the only live-mutable projection collection while
// retaining immutable history by reference.
func SnapshotForRead(value Snapshot) Snapshot {
	// Timeline events are immutable once appended. Sharing that backing storage
	// makes a read independent of retained history; an append can only write
	// beyond this slice's length. Chat is the one live-owned collection whose
	// current entry is updated in place while tokens stream, so isolate it.
	value.Chat = cloneChat(value.Chat)
	return value
}
