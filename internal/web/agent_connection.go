package web

import (
	"net/http"
	"time"

	"harness/internal/config"
	"harness/internal/events"
)

type pendingAgentConnection struct {
	AgentID     string `json:"agent_id"`
	From        string `json:"from"`
	To          string `json:"to"`
	RequestedAt string `json:"requested_at"`
}

func (s *Server) agentConnectionChanges() map[string]pendingAgentConnection {
	s.agentConnectionMu.Lock()
	defer s.agentConnectionMu.Unlock()
	result := make(map[string]pendingAgentConnection, len(s.agentConnections))
	for id, change := range s.agentConnections {
		result[id] = change
	}
	return result
}

func (s *Server) agentConnection(w http.ResponseWriter, r *http.Request, agentID string) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var body struct {
		Action       string `json:"action"`
		ConnectionID string `json:"connection_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Action == "cancel" {
		s.agentConnectionMu.Lock()
		change, exists := s.agentConnections[agentID]
		if exists {
			delete(s.agentConnections, agentID)
		}
		s.agentConnectionMu.Unlock()
		if !exists {
			writeError(w, http.StatusNotFound, "no pending connection change", "agent_id")
			return
		}
		s.bus.Publish(events.New(events.AgentConnectionChange, "", "", map[string]any{"status": "cancelled", "agent_id": agentID, "from": change.From, "to": change.To}))
		writeJSON(w, http.StatusOK, map[string]any{"status": "cancelled", "agent_id": agentID})
		return
	}
	if body.Action != "set" || body.ConnectionID == "" {
		writeError(w, http.StatusBadRequest, "action must be set with connection_id, or cancel", "action")
		return
	}
	agent, ok := s.ConfigSnapshot().Agent(agentID)
	if !ok {
		writeError(w, http.StatusNotFound, "agent not found", "agent_id")
		return
	}
	if s.registry == nil {
		writeError(w, http.StatusServiceUnavailable, "agent runtime unavailable", "agent_id")
		return
	}
	if runnable, reason := s.registry.ConnectionRunnable(body.ConnectionID); !runnable {
		writeError(w, http.StatusBadRequest, reason, "connection_id")
		return
	}
	if body.ConnectionID == agent.B {
		s.agentConnectionMu.Lock()
		change, pending := s.agentConnections[agentID]
		s.agentConnectionMu.Unlock()
		if pending {
			writeJSON(w, http.StatusAccepted, map[string]any{"status": "pending", "change": change})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "applied", "agent_id": agentID, "connection_id": body.ConnectionID})
		return
	}
	change := pendingAgentConnection{AgentID: agentID, From: agent.B, To: body.ConnectionID, RequestedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	s.agentConnectionMu.Lock()
	s.agentConnections[agentID] = change
	s.agentConnectionMu.Unlock()
	s.bus.Publish(events.New(events.AgentConnectionChange, "", "", map[string]any{"status": "pending", "change": change}))
	if s.tryAgentIdle != nil {
		s.tryAgentIdle(agentID)
	}
	if _, pending := s.agentConnectionChanges()[agentID]; pending {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "pending", "change": change})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "applied", "agent_id": agentID, "connection_id": body.ConnectionID})
}

func (s *Server) applyPendingAgentConnection(agentID string) {
	s.agentConnectionMu.Lock()
	change, exists := s.agentConnections[agentID]
	if exists {
		delete(s.agentConnections, agentID)
	}
	s.agentConnectionMu.Unlock()
	if !exists {
		return
	}

	s.mu.Lock()
	index := -1
	for i := range s.cfg.Agents {
		if config.AgentID(s.cfg.Agents[i].Name) == agentID {
			index = i
			break
		}
	}
	if index < 0 {
		s.mu.Unlock()
		s.restorePendingAgentConnection(change)
		s.bus.Publish(events.New(events.Error, "", "", map[string]any{"where": "agent_connection_change", "message": "agent not found: " + agentID}))
		return
	}
	previous := s.cfg.Agents[index].B
	s.cfg.Agents[index].B = change.To
	if err := s.cfg.Save(s.configPath); err != nil {
		s.cfg.Agents[index].B = previous
		s.mu.Unlock()
		s.restorePendingAgentConnection(change)
		s.bus.Publish(events.New(events.Error, "", "", map[string]any{"where": "agent_connection_change", "message": err.Error()}))
		return
	}
	masked := s.cfg.Masked()
	s.mu.Unlock()
	if s.runner != nil {
		s.runner.Configure(s.ConfigSnapshot())
	}
	if s.registry != nil {
		if err := s.registry.ApplyAgentBinding(agentID); err != nil {
			s.bus.Publish(events.New(events.Error, "", "", map[string]any{"where": "agent_connection_change", "message": err.Error()}))
		}
	}
	s.bus.Publish(events.New(events.ConfigChanged, "", "", map[string]any{"config": masked}))
	s.bus.Publish(events.New(events.AgentConnectionChange, "", "", map[string]any{"status": "applied", "agent_id": agentID, "from": change.From, "to": change.To}))
}

func (s *Server) restorePendingAgentConnection(change pendingAgentConnection) {
	s.agentConnectionMu.Lock()
	if _, replaced := s.agentConnections[change.AgentID]; !replaced {
		s.agentConnections[change.AgentID] = change
	}
	s.agentConnectionMu.Unlock()
}
