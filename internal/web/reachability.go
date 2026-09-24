package web

import "time"

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
	s.reachabilityMu.Lock()
	state := s.reachability[connectionID]
	if state == nil {
		state = &reachabilityRetry{}
		s.reachability[connectionID] = state
	}
	if state.timer == nil {
		s.scheduleReachabilityProbeLocked(connectionID, state)
	}
	s.reachabilityMu.Unlock()
}

func (s *Server) scheduleReachabilityProbeLocked(connectionID string, state *reachabilityRetry) {
	index := min(state.attempt, len(reachabilityBackoff)-1)
	delay := reachabilityBackoff[index]
	state.attempt++
	state.timer = s.reachabilityAfter(delay, func() {
		s.reachabilityMu.Lock()
		current := s.reachability[connectionID]
		if current != state {
			s.reachabilityMu.Unlock()
			return
		}
		state.timer = nil
		s.reachabilityMu.Unlock()
		connection, ok := s.Connection(connectionID)
		if !ok {
			s.clearReachabilityProbe(connectionID)
			return
		}
		s.startProbe(connection)
	})
}

func (s *Server) cancelScheduledReachabilityProbe(connectionID string) {
	s.reachabilityMu.Lock()
	if state := s.reachability[connectionID]; state != nil && state.timer != nil {
		state.timer.Stop()
		state.timer = nil
	}
	s.reachabilityMu.Unlock()
}

func (s *Server) completeReachabilityProbe(connectionID string, succeeded bool) {
	s.reachabilityMu.Lock()
	state := s.reachability[connectionID]
	if state == nil {
		s.reachabilityMu.Unlock()
		return
	}
	if succeeded {
		if state.timer != nil {
			state.timer.Stop()
		}
		delete(s.reachability, connectionID)
	} else if state.timer == nil {
		s.scheduleReachabilityProbeLocked(connectionID, state)
	}
	s.reachabilityMu.Unlock()
}

func (s *Server) clearReachabilityProbe(connectionID string) {
	s.reachabilityMu.Lock()
	if state := s.reachability[connectionID]; state != nil && state.timer != nil {
		state.timer.Stop()
	}
	delete(s.reachability, connectionID)
	s.reachabilityMu.Unlock()
}
