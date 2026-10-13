package web

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"
)

type pageMeasurementState struct {
	mu                   sync.RWMutex
	measurements         map[string]measureState
	cancels              map[string]context.CancelFunc
	navigationMu         sync.Mutex
	requestWatchdogAfter time.Duration
	requestWatchdogLog   func(string)
	navigationIDs        map[string]time.Time
}

func newPageMeasurementState() pageMeasurementState {
	return pageMeasurementState{
		measurements:  map[string]measureState{},
		cancels:       map[string]context.CancelFunc{},
		navigationIDs: map[string]time.Time{},
	}
}

func (s *pageMeasurementState) measurement(id string) (measureState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.measurements[id]
	return state, ok
}

func (s *pageMeasurementState) start(id string, cancel context.CancelFunc, total int) (measureState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.measurements[id].Running {
		return measureState{}, false
	}
	state := measureState{Running: true, Text: "brief 0 of " + strconv.Itoa(total), Total: total}
	s.measurements[id] = state
	s.cancels[id] = cancel
	return state, true
}

func (s *pageMeasurementState) stop(id string) (measureState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cancel := s.cancels[id]
	state := s.measurements[id]
	if cancel == nil || !state.Running {
		return state, false
	}
	state.Text = fmt.Sprintf("stopping after brief %d of %d", state.Processed, state.Total)
	s.measurements[id] = state
	cancel()
	return state, true
}

func (s *pageMeasurementState) clearCancel(id string) {
	s.mu.Lock()
	delete(s.cancels, id)
	s.mu.Unlock()
}

func (s *pageMeasurementState) set(id string, state measureState) {
	s.mu.Lock()
	s.measurements[id] = state
	s.mu.Unlock()
}

func (s *pageMeasurementState) watchdog() (time.Duration, func(string)) {
	return s.requestWatchdogAfter, s.requestWatchdogLog
}

func (s *pageMeasurementState) claimNavigationID(id string) bool {
	s.navigationMu.Lock()
	defer s.navigationMu.Unlock()
	now := time.Now()
	for key, seen := range s.navigationIDs {
		if now.Sub(seen) > 24*time.Hour {
			delete(s.navigationIDs, key)
		}
	}
	if _, exists := s.navigationIDs[id]; exists {
		return false
	}
	s.navigationIDs[id] = now
	return true
}
