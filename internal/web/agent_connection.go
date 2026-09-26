package web

import (
	"errors"
	"net/http"
	"time"

	"harness/internal/config"
	"harness/internal/events"
)

type pendingAgentConnection struct {
	AgentID     string `json:"agent_id"`
	Role        string `json:"role"`
	From        string `json:"from"`
	To          string `json:"to"`
	RequestedAt string `json:"requested_at"`
}

// Item 2ln (a): the route takes a ROLE. [[2iq]] built the Agents table in v1.16.0
// and found two of its three rows had to be read-only, because this route wrote
// agent.B and nothing else. The operator asked to "assign a model to an agent";
// two thirds read-only is not that.
//
// (b), and rel-1.17.0/W0 measured why the wording matters: a run resolves its
// connection from the SESSION (run.go:119,222,334,396), so a session already
// bound is never moved by a role change -- but r.cfg is a live accessor and the
// aux and compaction paths read agent.C at the point of use, so a compaction
// taken after a change uses the new c. b alone is deferred to idle, which is
// [[2ia]]'s machinery and is kept.
const (
	roleB = "b"
	roleC = "c"
	roleD = "d"
)

func agentRoleConnection(agent *config.Agent, role string) (string, bool) {
	switch role {
	case roleB:
		return agent.B, true
	case roleC:
		return agent.C, true
	case roleD:
		return agent.D, true
	}
	return "", false
}

func setAgentRoleConnection(agent *config.Agent, role, connectionID string) bool {
	switch role {
	case roleB:
		agent.B = connectionID
	case roleC:
		agent.C = connectionID
	case roleD:
		agent.D = connectionID
	default:
		return false
	}
	return true
}

var (
	errAgentNotFound = errors.New("agent not found")
	errUnknownRole   = errors.New("role must be b, c or d")
)

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
		Role         string `json:"role"`
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
	// An absent role is b: every caller that predates this item, including the
	// strip's switcher, keeps working without knowing a role exists.
	role := body.Role
	if role == "" {
		role = roleB
	}
	agent, ok := s.ConfigSnapshot().Agent(agentID)
	if !ok {
		writeError(w, http.StatusNotFound, "agent not found", "agent_id")
		return
	}
	current, known := agentRoleConnection(agent, role)
	if !known {
		writeError(w, http.StatusBadRequest, "role must be b, c or d", "role")
		return
	}
	// (d): d exists only when the agent has one. Assigning to a role the agent
	// does not have is refused here, so the page hiding the row is an
	// ergonomic and not the guarantee.
	if role == roleD && agent.D == "" {
		writeError(w, http.StatusBadRequest, "this agent has no d role", "role")
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
	if role != roleB {
		// (b): c and d take effect on the next run that reads them. Nothing is
		// pending, because no session is bound to them.
		if body.ConnectionID == current {
			writeJSON(w, http.StatusOK, map[string]any{"status": "applied", "agent_id": agentID, "role": role, "connection_id": body.ConnectionID})
			return
		}
		if err := s.writeAgentRole(agentID, role, body.ConnectionID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error(), "connection_id")
			return
		}
		s.bus.Publish(events.New(events.AgentConnectionChange, "", "", map[string]any{"status": "applied", "agent_id": agentID, "role": role, "from": current, "to": body.ConnectionID}))
		writeJSON(w, http.StatusOK, map[string]any{"status": "applied", "agent_id": agentID, "role": role, "connection_id": body.ConnectionID})
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
	change := pendingAgentConnection{AgentID: agentID, Role: roleB, From: agent.B, To: body.ConnectionID, RequestedAt: time.Now().UTC().Format(time.RFC3339Nano)}
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

// writeAgentRole writes one role's connection and saves. It is c and d's whole
// path: there is no pending state and no session to re-bind, because nothing is
// talking to those connections when the change is made.
func (s *Server) writeAgentRole(agentID, role, connectionID string) error {
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
		return errAgentNotFound
	}
	previous, _ := agentRoleConnection(&s.cfg.Agents[index], role)
	if !setAgentRoleConnection(&s.cfg.Agents[index], role, connectionID) {
		s.mu.Unlock()
		return errUnknownRole
	}
	if err := s.cfg.Save(s.configPath); err != nil {
		setAgentRoleConnection(&s.cfg.Agents[index], role, previous)
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	if s.runner != nil {
		s.runner.Configure(s.ConfigSnapshot())
	}
	return nil
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
