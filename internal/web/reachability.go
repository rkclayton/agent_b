package web

import (
	"context"
	"sync"
	"time"
)

type probeRun struct{ cancel context.CancelFunc }

type connectionCheckState struct {
	probeMu           sync.Mutex
	probeCancels      map[string]*probeRun
	probeRetries      map[string]int
	reachabilityMu    sync.Mutex
	reachability      map[string]*reachabilityRetry
	reachabilityAfter func(time.Duration, func()) operatorTimer
	healthMu          sync.Mutex
	health            map[string]connectionHealth
	healthEvery       time.Duration
}

func newConnectionCheckState() connectionCheckState {
	return connectionCheckState{
		probeCancels: map[string]*probeRun{},
		probeRetries: map[string]int{},
		reachability: map[string]*reachabilityRetry{},
		reachabilityAfter: func(duration time.Duration, fn func()) operatorTimer {
			return time.AfterFunc(duration, fn)
		},
	}
}

func (s *connectionCheckState) cancelProbe(connectionID string) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	if probe := s.probeCancels[connectionID]; probe != nil {
		probe.cancel()
	}
}

func (s *connectionCheckState) cancelAllProbes() {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	for _, probe := range s.probeCancels {
		probe.cancel()
	}
}

func (s *connectionCheckState) clearProbe(connectionID string, current *probeRun) bool {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	if s.probeCancels[connectionID] != current {
		return false
	}
	delete(s.probeCancels, connectionID)
	return true
}

func (s *connectionCheckState) interval() time.Duration { return s.healthEvery }

func (s *connectionCheckState) setHealth(connectionID string, health connectionHealth) bool {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	if s.health == nil {
		s.health = map[string]connectionHealth{}
	}
	changed := s.health[connectionID] != health
	s.health[connectionID] = health
	return changed
}

func (s *connectionCheckState) healthSnapshot() map[string]connectionHealth {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	out := make(map[string]connectionHealth, len(s.health))
	for id, health := range s.health {
		out[id] = health
	}
	return out
}

type reachabilityRetry struct {
	attempt int
	timer   operatorTimer
}

var reachabilityBackoff = [...]time.Duration{
	time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	15 * time.Second,
	30 * time.Second,
}

func (s *Server) scheduleReachabilityProbe(connectionID string) {
	if connectionID == "" {
		return
	}
	s.connectionChecks.reachabilityMu.Lock()
	state := s.connectionChecks.reachability[connectionID]
	if state == nil {
		state = &reachabilityRetry{}
		s.connectionChecks.reachability[connectionID] = state
	}
	if state.timer == nil {
		s.scheduleReachabilityProbeLocked(connectionID, state)
	}
	s.connectionChecks.reachabilityMu.Unlock()
}

func (s *Server) scheduleReachabilityProbeLocked(connectionID string, state *reachabilityRetry) {
	index := min(state.attempt, len(reachabilityBackoff)-1)
	delay := reachabilityBackoff[index]
	state.attempt++
	state.timer = s.connectionChecks.reachabilityAfter(delay, func() {
		s.connectionChecks.reachabilityMu.Lock()
		current := s.connectionChecks.reachability[connectionID]
		if current != state {
			s.connectionChecks.reachabilityMu.Unlock()
			return
		}
		state.timer = nil
		s.connectionChecks.reachabilityMu.Unlock()
		connection, ok := s.Connection(connectionID)
		if !ok {
			s.clearReachabilityProbe(connectionID)
			return
		}
		// Recovery uses the existing completion-free health check. Capability
		// probing belongs only to Eval, and a resumed real request remains the
		// authority on whether the model can answer.
		s.checkConnectionHealth(context.Background(), *connection)
		health := s.connectionHealthState()[connectionID]
		succeeded := health.Lamp != "alarm"
		if succeeded {
			s.releaseReachableConnection(connectionID)
		} else {
			s.completeReachabilityProbe(connectionID, false)
		}
	})
}

func (s *Server) releaseReachableConnection(connectionID string) {
	s.completeReachabilityProbe(connectionID, true)
	if s.scheduler != nil {
		s.scheduler.ReleaseModel(connectionID)
	}
}

func (s *Server) cancelScheduledReachabilityProbe(connectionID string) {
	s.connectionChecks.reachabilityMu.Lock()
	if state := s.connectionChecks.reachability[connectionID]; state != nil && state.timer != nil {
		state.timer.Stop()
		state.timer = nil
	}
	s.connectionChecks.reachabilityMu.Unlock()
}

func (s *Server) completeReachabilityProbe(connectionID string, succeeded bool) {
	s.connectionChecks.reachabilityMu.Lock()
	state := s.connectionChecks.reachability[connectionID]
	if state == nil {
		s.connectionChecks.reachabilityMu.Unlock()
		return
	}
	if succeeded {
		if state.timer != nil {
			state.timer.Stop()
		}
		delete(s.connectionChecks.reachability, connectionID)
	} else if state.timer == nil {
		s.scheduleReachabilityProbeLocked(connectionID, state)
	}
	s.connectionChecks.reachabilityMu.Unlock()
}

func (s *Server) clearReachabilityProbe(connectionID string) {
	s.connectionChecks.reachabilityMu.Lock()
	if state := s.connectionChecks.reachability[connectionID]; state != nil && state.timer != nil {
		state.timer.Stop()
	}
	delete(s.connectionChecks.reachability, connectionID)
	s.connectionChecks.reachabilityMu.Unlock()
}
