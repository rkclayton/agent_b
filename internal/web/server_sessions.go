package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/projection"
	"harness/internal/session"
	workspaceinfo "harness/internal/workspace"
)

func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		values := []projection.Snapshot{}
		if s.projector != nil && s.writers != nil {
			projected, err := s.projector.Snapshot(s.writers.SessionCursors())
			if err != nil {
				writeError(w, 500, err.Error(), "projection")
				return
			}
			for _, value := range projected {
				values = append(values, value)
			}
		}
		writeJSON(w, 200, values)
	case http.MethodPost:
		var body struct {
			Label           string `json:"label"`
			AgentID         string `json:"agent_id"`
			ServerID        string `json:"server_id"`
			SourceSessionID string `json:"source_session_id"`
			Role            string `json:"role"`
			PlanID          string `json:"plan_id"`
		}
		if !decode(w, r, &body) {
			return
		}
		if body.SourceSessionID != "" {
			if body.Label != "" || body.AgentID != "" || body.ServerID != "" || body.Role != "" || body.PlanID != "" {
				writeError(w, 400, "source_session_id cannot be combined with overrides", "session")
				return
			}
			item, err := s.registry.CreateLike(body.SourceSessionID)
			if err != nil {
				writeError(w, 400, err.Error(), "session")
				return
			}
			if s.runner != nil {
				s.runner.PublishBudget(r.Context(), item)
			}
			writeJSON(w, 201, map[string]any{"session": item.Snapshot()})
			return
		}
		if body.AgentID == "" && body.ServerID != "" {
			s.mu.RLock()
			for _, candidate := range s.cfg.Agents {
				if candidate.B == body.ServerID {
					body.AgentID = config.AgentID(candidate.Name)
					break
				}
			}
			s.mu.RUnlock()
		}
		if body.AgentID == "" {
			s.mu.RLock()
			body.AgentID = s.cfg.DefaultAgentID()
			s.mu.RUnlock()
		}
		role := body.Role
		if role == "" {
			role = "b"
		}
		if role != "d" && role != "c" && body.PlanID != "" {
			writeError(w, 400, "plan_id is available only for roles c and d", "plan_id")
			return
		}
		if runnable, reason := s.registry.AgentRoleRunnable(body.AgentID, role); !runnable {
			writeError(w, 400, reason, "agent_id")
			return
		}
		item, err := s.registry.CreateRole(body.Label, body.AgentID, "", role, body.PlanID)
		if err != nil {
			writeError(w, 400, err.Error(), "session")
			return
		}
		if s.runner != nil {
			s.runner.PublishBudget(r.Context(), item)
		}
		writeJSON(w, 201, map[string]any{"session": item.Snapshot()})
	default:
		method(w)
	}
}

func (s *Server) plans(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.createPlan(w, r)
		return
	}
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	root := filepath.Join(s.roots.Data, "plans")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		writeJSON(w, 200, []any{})
		return
	}
	if err != nil {
		writeError(w, 500, err.Error(), "plans")
		return
	}
	_ = entries
	values, err := session.ListPlans(root)
	if err != nil {
		writeError(w, 500, err.Error(), "plans")
		return
	}
	writeJSON(w, 200, values)
}

func (s *Server) workspaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if s.workspaceState == nil {
		writeJSON(w, http.StatusOK, []workspaceinfo.Entry{})
		return
	}
	writeJSON(w, http.StatusOK, s.workspaceState.List())
}

func (s *Server) workspaceAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || s.workspaceState == nil {
		method(w)
		return
	}
	action := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/workspaces/"), "/")
	var body struct {
		Dir       string `json:"dir"`
		Hash      string `json:"hash"`
		SessionID string `json:"session_id"`
		Confirm   bool   `json:"confirm"`
	}
	if !decode(w, r, &body) {
		return
	}
	switch action {
	case "memory-clear":
		if !body.Confirm {
			writeError(w, http.StatusBadRequest, "memory clear requires confirmation", "confirm")
			return
		}
		if s.memoryState == nil {
			writeError(w, 500, "memory manager unavailable", "memory")
			return
		}
		if err := s.memoryState.Clear(body.Dir); err != nil {
			writeError(w, 500, err.Error(), "memory")
			return
		}
		s.registry.ClearWorkspaceMemory(body.Dir)
		for _, item := range s.registry.List() {
			if strings.EqualFold(filepath.Clean(item.Workspace), filepath.Clean(body.Dir)) {
				s.bus.Publish(events.New(events.MemoryCleared, item.ID, "", map[string]any{"dir": body.Dir}))
			}
		}
		writeJSON(w, 200, map[string]any{"dir": body.Dir, "cleared": true})
	case "policy-approve":
		state, err := s.workspaceState.Approve(body.Dir, body.Hash)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error(), "policy")
			return
		}
		if err := s.registry.ApplyRepoPolicySession(body.SessionID, state); err != nil {
			writeError(w, http.StatusNotFound, err.Error(), "session")
			return
		}
		snapshot, _ := s.registry.Get(body.SessionID)
		s.bus.Publish(events.New(events.PolicyApproved, body.SessionID, "", map[string]any{"dir": body.Dir, "path": state.Path, "hash": state.Hash, "approved_at": state.ApprovedAt, "approved": true, "persisted": true, "scope": "session", "tools": snapshot.Snapshot().Tools}))
		writeJSON(w, 200, state)
	case "policy-once":
		state, err := s.workspaceState.PolicyForDecision(body.Dir, body.Hash)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error(), "policy")
			return
		}
		state.Approved = true
		if err := s.registry.ApplyRepoPolicySession(body.SessionID, state); err != nil {
			writeError(w, http.StatusNotFound, err.Error(), "session")
			return
		}
		snapshot, _ := s.registry.Get(body.SessionID)
		s.bus.Publish(events.New(events.PolicyApproved, body.SessionID, "", map[string]any{"dir": body.Dir, "path": state.Path, "hash": state.Hash, "approved": true, "persisted": false, "scope": "session", "tools": snapshot.Snapshot().Tools}))
		writeJSON(w, 200, state)
	case "policy-deny":
		if err := s.registry.DenyRepoPolicy(body.SessionID); err != nil {
			writeError(w, 404, err.Error(), "session")
			return
		}
		s.bus.Publish(events.New(events.PolicyDenied, body.SessionID, "", map[string]any{"dir": body.Dir, "hash": body.Hash}))
		writeJSON(w, 200, map[string]any{"denied": true})
	case "policy-revoke":
		if err := s.workspaceState.Revoke(body.Dir); err != nil {
			writeError(w, 500, err.Error(), "policy")
			return
		}
		s.registry.RevokeRepoPolicy(body.Dir)
		for _, item := range s.registry.List() {
			if strings.EqualFold(filepath.Clean(item.Workspace), filepath.Clean(body.Dir)) {
				s.bus.Publish(events.New(events.PolicyRevoked, item.ID, "", map[string]any{"dir": body.Dir}))
			}
		}
		writeJSON(w, 200, map[string]any{"dir": body.Dir, "revoked": true})
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	tail := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	parts := strings.Split(strings.Trim(tail, "/"), "/")
	if len(parts) < 1 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	if len(parts) == 2 && parts[1] == "result-label" && r.Method == http.MethodPost {
		item, ok := s.registry.Get(id)
		if !ok {
			writeError(w, http.StatusNotFound, "session not found", "session_id")
			return
		}
		var body struct {
			Label string `json:"label"`
			RunID string `json:"run_id"`
		}
		if !decode(w, r, &body) {
			return
		}
		if body.Label != "productive" && body.Label != "stuck" && body.Label != "mixed" {
			writeError(w, http.StatusBadRequest, "label must be productive, stuck, or mixed", "label")
			return
		}
		run := item.Snapshot().Run
		if run.Status == "running" || run.Status == "queued" || run.Status == "stopping" || run.LastStopReason == "" {
			writeError(w, http.StatusConflict, "a completed run is required", "session_id")
			return
		}
		if body.RunID == "" || body.RunID != run.LastRunID {
			writeError(w, http.StatusConflict, "the completed run has changed", "run_id")
			return
		}
		run.ResultLabel = body.Label
		item.SetRun(run)
		s.bus.Publish(events.New(events.RunLabeled, id, body.RunID, map[string]any{"label": body.Label}))
		writeJSON(w, http.StatusOK, map[string]any{"session_id": id, "run_id": body.RunID, "label": body.Label})
		return
	}
	if len(parts) == 3 && parts[1] == "grants" && parts[2] == "revoke" && r.Method == http.MethodPost {
		if err := s.operatorRequest(r); err != nil {
			writeError(w, http.StatusForbidden, "Run as you can be revoked only by the verified local operator process", "session_id")
			return
		}
		if _, ok := s.registry.Get(id); !ok {
			writeError(w, http.StatusNotFound, "session not found", "session_id")
			return
		}
		if s.runner != nil {
			s.runner.RevokeSessionGrants(id)
		}
		writeJSON(w, http.StatusOK, map[string]any{"session_id": id, "revoked": true})
		return
	}
	if len(parts) == 3 && parts[1] == "messages" && parts[2] == "drop-last" && r.Method == http.MethodPost {
		message, err := s.registry.DropLastMessage(id)
		if err != nil {
			status := http.StatusConflict
			if strings.Contains(err.Error(), "not found") {
				status = http.StatusNotFound
			}
			writeError(w, status, err.Error(), "session")
			return
		}
		if item, ok := s.registry.Get(id); ok && s.runner != nil {
			s.runner.PublishBudget(r.Context(), item)
		}
		writeJSON(w, http.StatusOK, map[string]any{"message_id": message.ID, "role": message.Role})
		return
	}
	if len(parts) == 2 && parts[1] == "reset" && r.Method == http.MethodPost {
		if s.scheduler != nil && s.scheduler.Active(id) {
			if r.URL.Query().Get("force") != "1" {
				writeError(w, 409, "session is running", "session_id")
				return
			}
			s.scheduler.Stop(id, false)
			deadline := time.Now().Add(2 * time.Second)
			for s.scheduler.Active(id) && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
		}
		path, err := s.registry.Reset(id)
		if err != nil {
			writeError(w, 409, err.Error(), "session")
			return
		}
		if item, ok := s.registry.Get(id); ok && s.runner != nil {
			s.runner.PublishBudget(r.Context(), item)
		}
		writeJSON(w, 200, map[string]string{"log_path": path})
		return
	}
	if len(parts) == 2 && parts[1] == "reopen" && r.Method == http.MethodPost {
		if err := s.registry.Reopen(id); err != nil {
			status := http.StatusConflict
			if strings.Contains(err.Error(), "not found") {
				status = http.StatusNotFound
			}
			writeError(w, status, err.Error(), "session")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"session_id": id})
		return
	}
	// Item 2gq (v1.2.5): the separate permanent-delete route is gone. Closing a
	// chat deletes it, so there is one way to remove a chat and no second,
	// differently-gated one to keep in step with it. DELETE on the session is
	// that way, and it is handled below.
	switch r.Method {
	case http.MethodPost:
		var body struct {
			Label    *string `json:"label"`
			AgentID  *string `json:"agent_id"`
			ServerID *string `json:"server_id"`
		}
		if !decode(w, r, &body) {
			return
		}
		if body.Label == nil && body.AgentID == nil && body.ServerID == nil {
			writeError(w, 400, "label or agent_id is required", "session")
			return
		}
		if body.AgentID == nil && body.ServerID != nil {
			s.mu.RLock()
			for _, candidate := range s.cfg.Agents {
				if candidate.B == *body.ServerID {
					value := config.AgentID(candidate.Name)
					body.AgentID = &value
					break
				}
			}
			s.mu.RUnlock()
		}
		if body.AgentID != nil {
			if err := s.registry.SetAgent(id, *body.AgentID); err != nil {
				status := http.StatusBadRequest
				field := "agent_id"
				if strings.Contains(err.Error(), "not found") {
					status, field = http.StatusNotFound, "session"
				} else if strings.Contains(err.Error(), "running") || strings.Contains(err.Error(), "closed") {
					status, field = http.StatusConflict, "session"
				}
				writeError(w, status, err.Error(), field)
				return
			}
		}
		if body.Label != nil {
			if err := s.registry.Rename(id, *body.Label); err != nil {
				status := http.StatusNotFound
				if strings.Contains(err.Error(), "closed") {
					status = http.StatusConflict
				}
				writeError(w, status, err.Error(), "session")
				return
			}
		}
		item, ok := s.registry.Get(id)
		if !ok {
			writeError(w, 404, "session not found", "session")
			return
		}
		if body.AgentID != nil && s.runner != nil {
			s.runner.PublishBudget(r.Context(), item)
		}
		writeJSON(w, 200, map[string]any{"session": item.Snapshot()})
	case http.MethodDelete:
		// Item 2gq: a chat closed before this release is still in history and
		// is still closed; closing it again is what deletes it. So "already
		// closed" is not an error here - it is the second half of the journey.
		if err := s.registry.Close(id); err != nil && !strings.Contains(err.Error(), "already closed") {
			status := 404
			if strings.Contains(err.Error(), "running") || strings.Contains(err.Error(), "closed") {
				status = 409
			}
			writeError(w, status, err.Error(), "session")
			return
		}
		if s.runner != nil {
			s.runner.LapseSessionGrants(id)
		}
		if s.operatorFiles != nil {
			if item, ok := s.registry.Get(id); ok {
				if path, err := s.operatorFiles.ExportChat(item.Snapshot()); err != nil {
					s.bus.Publish(events.New(events.Error, id, "", map[string]any{"where": "chat_export", "message": err.Error()}))
				} else {
					s.bus.Publish(events.New(events.ChatExported, id, "", map[string]any{"path": path}))
				}
			}
		}
		// Item 2gq (v1.2.5): closing a chat IS deleting it. The journal, the
		// scratch folder, its attachments, its history entry and its tab go;
		// what the chat produced elsewhere - memory notes, plans, reflection
		// rows, files written into a repository - is not the chat and stays.
		// The export above runs first, so the markdown of what was said
		// survives the chat itself.
		inventory, err := s.registry.Delete(id)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error(), "session")
			return
		}
		if s.projector != nil {
			s.projector.Delete(id)
		}
		writeJSON(w, 200, map[string]any{"session_id": id, "inventory": inventory})
	default:
		method(w)
	}
}
