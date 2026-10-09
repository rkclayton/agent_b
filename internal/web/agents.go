package web

import (
	"net/http"
	"strings"

	"harness/internal/config"
	"harness/internal/events"
)

type agentRequest struct {
	Action     string   `json:"action"`
	ID         string   `json:"id"`
	SourceID   string   `json:"source_id"`
	Name       string   `json:"name"`
	Connection string   `json:"connection_id"`
	Model      string   `json:"model"`
	Prompt     string   `json:"prompt"`
	Tools      []string `json:"tools"`
	Private    bool     `json:"private"`
}

func (s *Server) agentsEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"agents": s.ConfigSnapshot().Agents})
		return
	}
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var body agentRequest
	if !decode(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	s.mu.Lock()
	before := append([]config.Agent(nil), s.cfg.Agents...)
	find := func(id string) int {
		for i := range s.cfg.Agents {
			if config.AgentID(s.cfg.Agents[i].Name) == id {
				return i
			}
		}
		return -1
	}
	changedID, deletedID := "", ""
	switch body.Action {
	case "add", "duplicate":
		source := find("agent_b")
		if body.Action == "duplicate" && body.SourceID != "" {
			source = find(body.SourceID)
		}
		if source < 0 {
			s.mu.Unlock()
			writeError(w, 404, "source agent not found", "source_id")
			return
		}
		if body.Name == "" {
			s.mu.Unlock()
			writeError(w, 400, "name is required", "name")
			return
		}
		next := s.cfg.Agents[source]
		next.Name = body.Name
		s.cfg.Agents = append(s.cfg.Agents, next)
		changedID = config.AgentID(next.Name)
	case "update":
		index := find(body.ID)
		if index < 0 {
			s.mu.Unlock()
			writeError(w, 404, "agent not found", "id")
			return
		}
		builtIn := body.ID == "agent_b"
		if !builtIn {
			if body.Name == "" {
				s.mu.Unlock()
				writeError(w, 400, "name is required", "name")
				return
			}
			s.cfg.Agents[index].Name = body.Name
			s.cfg.Agents[index].Prompt = body.Prompt
			s.cfg.Agents[index].Toolset = append([]string(nil), body.Tools...)
			s.cfg.Agents[index].Private = body.Private
		} else if (body.Name != "" && body.Name != "agent_b") || body.Prompt != "" || body.Private || (body.Tools != nil && !sameStrings(body.Tools, s.cfg.Agents[index].Toolset)) {
			s.mu.Unlock()
			writeError(w, 400, "agent_b name, prompt and tools are read-only", "agent")
			return
		}
		if body.Connection != "" {
			s.cfg.Agents[index].B = body.Connection
		}
		s.cfg.Agents[index].Model = strings.TrimSpace(body.Model)
		changedID = config.AgentID(s.cfg.Agents[index].Name)
	case "delete":
		if body.ID == "agent_b" {
			s.mu.Unlock()
			writeError(w, 400, "agent_b cannot be deleted", "id")
			return
		}
		index := find(body.ID)
		if index < 0 {
			s.mu.Unlock()
			writeError(w, 404, "agent not found", "id")
			return
		}
		deletedID = body.ID
		s.cfg.Agents = append(s.cfg.Agents[:index], s.cfg.Agents[index+1:]...)
	default:
		s.mu.Unlock()
		writeError(w, 400, "action must be add, duplicate, update or delete", "action")
		return
	}
	if err := s.cfg.Validate(); err != nil {
		s.cfg.Agents = before
		s.mu.Unlock()
		writeError(w, 400, err.Error(), "agent")
		return
	}
	if err := s.saveProfileConfig(*s.cfg); err != nil {
		s.cfg.Agents = before
		s.mu.Unlock()
		writeError(w, 500, err.Error(), "config")
		return
	}
	masked := s.cfg.Masked()
	s.mu.Unlock()
	if s.runner != nil {
		s.runner.Configure(s.ConfigSnapshot())
	}
	if deletedID != "" {
		for _, item := range s.registry.List() {
			if item.Snapshot().AgentID == deletedID {
				s.setOrQueueSessionAgent(item.ID, "agent_b")
			}
		}
		if s.memoryState != nil {
			_ = s.memoryState.DeleteAgent(deletedID)
		}
		if s.statsState != nil {
			_ = s.statsState.Delete(deletedID)
		}
	} else if body.Action == "update" && body.ID == changedID {
		_ = s.registry.ApplyAgentBinding(changedID)
	} else if body.Action == "update" && body.ID != changedID {
		if s.memoryState != nil {
			_ = s.memoryState.RenameAgent(body.ID, changedID)
		}
		if s.statsState != nil {
			_ = s.statsState.Rename(body.ID, changedID)
		}
		for _, item := range s.registry.List() {
			if item.Snapshot().AgentID == body.ID {
				s.setOrQueueSessionAgent(item.ID, changedID)
			}
		}
	}
	s.bus.Publish(events.New(events.ConfigChanged, "", "", map[string]any{"config": masked}))
	writeJSON(w, http.StatusOK, map[string]any{"agents": s.ConfigSnapshot().Agents, "agent_id": changedID})
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *Server) setOrQueueSessionAgent(sessionID, agentID string) bool {
	if err := s.registry.SetAgent(sessionID, agentID); err == nil {
		return false
	}
	s.agentConnectionMu.Lock()
	s.pendingSessionAgents[sessionID] = agentID
	s.agentConnectionMu.Unlock()
	return true
}

func (s *Server) applyPendingSessionAgents() {
	s.agentConnectionMu.Lock()
	pending := s.pendingSessionAgents
	s.pendingSessionAgents = map[string]string{}
	s.agentConnectionMu.Unlock()
	for sessionID, agentID := range pending {
		if err := s.registry.SetAgent(sessionID, agentID); err != nil {
			s.agentConnectionMu.Lock()
			s.pendingSessionAgents[sessionID] = agentID
			s.agentConnectionMu.Unlock()
		}
	}
}
