package web

import (
	"context"
	"sync"

	"harness/internal/chatstore"
	"harness/internal/session"
)

type chatState struct {
	mu      sync.RWMutex
	store   *chatstore.Store
	entries []chatstore.Entry
	watchMu sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
}

func (s *chatState) reset(root string, registry *session.Registry) {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	s.stopLocked()
	s.store = chatstore.New(root)
	s.startLocked(registry)
}

func (s *chatState) storeSnapshot() *chatstore.Store { return s.store }

func (s *chatState) lockForMutation(registry *session.Registry) func() {
	s.watchMu.Lock()
	s.stopLocked()
	return func() {
		s.startLocked(registry)
		s.watchMu.Unlock()
	}
}

func (s *chatState) count() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return int64(len(s.entries))
}

func (s *chatState) stopLocked() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.done != nil {
		<-s.done
	}
	s.cancel, s.done = nil, nil
}

func (s *chatState) startLocked(registry *session.Registry) {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	done := make(chan struct{})
	s.done = done
	go func() {
		defer close(done)
		_ = s.store.Watch(ctx, func(entries []chatstore.Entry) {
			s.mu.Lock()
			s.entries = entries
			s.mu.Unlock()
			registry.ReconcileChatHomes(entries)
		})
	}()
}
