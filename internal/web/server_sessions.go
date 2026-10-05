package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
			ConnectionID    string `json:"connection_id"`
			SourceSessionID string `json:"source_session_id"`
			Role            string `json:"role"`
			PlanID          string `json:"plan_id"`
		}
		if !decode(w, r, &body) {
			return
		}
		if body.SourceSessionID != "" {
			if body.Label != "" || body.AgentID != "" || body.ConnectionID != "" || body.Role != "" || body.PlanID != "" {
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
		if body.ConnectionID != "" {
			if runnable, reason := s.registry.ConnectionRunnable(body.ConnectionID); !runnable {
				writeError(w, http.StatusBadRequest, reason, "connection_id")
				return
			}
		}
		if body.AgentID == "" && body.ConnectionID != "" {
			s.mu.RLock()
			for _, candidate := range s.cfg.Agents {
				if candidate.B == body.ConnectionID {
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
		if body.ConnectionID != "" && item.ConnectionID != body.ConnectionID {
			if err := s.registry.SetConnection(item.ID, body.ConnectionID); err != nil {
				writeError(w, http.StatusBadRequest, err.Error(), "connection_id")
				return
			}
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
	root := filepath.Join(s.profileRoot(), "plans")
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
	writeJSON(w, http.StatusOK, s.knownFolders())
}

func (s *Server) knownFolders() []workspaceinfo.Entry {
	entries := make([]workspaceinfo.Entry, 0)
	seen := map[string]bool{}
	scratch := filepath.Clean(filepath.Join(s.profileRoot(), "scratch"))
	chats := filepath.Clean(filepath.Join(s.profileRoot(), "chats"))
	for _, entry := range s.workspaceState.List() {
		clean := filepath.Clean(entry.Dir)
		if withinRoot(scratch, clean) || withinRoot(chats, clean) || (entry.MemoryCount == 0 && entry.Policy == nil) {
			continue
		}
		key := strings.ToLower(clean)
		seen[key] = true
		entries = append(entries, entry)
	}
	for _, plan := range s.planList() {
		if strings.TrimSpace(plan.Repo) == "" {
			continue
		}
		clean := filepath.Clean(plan.Repo)
		key := strings.ToLower(clean)
		if !seen[key] {
			seen[key] = true
			entries = append(entries, workspaceinfo.Entry{Dir: clean})
		}
	}
	return entries
}

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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

func (s *Server) agentMemoryRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || s.memoryState == nil {
		method(w)
		return
	}
	var body struct {
		AgentID string `json:"agent_id"`
		Note    string `json:"note"`
		Confirm bool   `json:"confirm"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !body.Confirm || strings.TrimSpace(body.AgentID) == "" || strings.TrimSpace(body.Note) == "" {
		writeError(w, http.StatusBadRequest, "agent memory removal requires agent_id, note and confirmation", "memory")
		return
	}
	removed, err := s.memoryState.RemoveAgent(body.AgentID, body.Note)
	if err != nil {
		writeError(w, 500, err.Error(), "memory")
		return
	}
	// A retained session can still project a note after another session already
	// removed its durable line. Removal is idempotent: reconcile every live
	// projection even when the file is already clean.
	s.registry.RemoveAgentMemoryNote(body.AgentID, body.Note)
	if !removed {
		writeJSON(w, 200, map[string]any{"removed": false, "already_absent": true})
		return
	}
	writeJSON(w, 200, map[string]any{"removed": true})
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	tail := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	parts := strings.Split(strings.Trim(tail, "/"), "/")
	if len(parts) < 1 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	if len(parts) == 2 && parts[1] == "history" && r.Method == http.MethodGet {
		if s.projector == nil {
			writeError(w, http.StatusNotFound, "session not found", "session_id")
			return
		}
		value, ok := s.projector.CurrentSnapshot()[id]
		if !ok {
			writeError(w, http.StatusNotFound, "session not found", "session_id")
			return
		}
		before := len(value.Chat)
		if raw := r.URL.Query().Get("before"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 0 || parsed > before {
				writeError(w, http.StatusBadRequest, "before is outside this chat", "before")
				return
			}
			before = parsed
		}
		start := max(0, before-firstScreenEntries)
		writeJSON(w, http.StatusOK, map[string]any{
			"session_id": id, "start": start, "before": before, "total": len(value.Chat),
			"chat": value.Chat[start:before],
		})
		return
	}
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
			writeError(w, http.StatusForbidden, "Run as you can be revoked only by the verified local process of the user", "session_id")
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
	if len(parts) == 2 && parts[1] == "report" {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 2 && parts[1] == "rebind" && r.Method == http.MethodPost {
		item, ok := s.registry.Get(id)
		if !ok {
			writeError(w, http.StatusNotFound, "session not found", "session")
			return
		}
		snapshot := item.Snapshot()
		if _, exists := s.Connection(snapshot.ConnectionID); exists {
			writeError(w, http.StatusConflict, "session connection is not missing", "connection_id")
			return
		}
		target := ""
		s.mu.RLock()
		for _, connection := range s.cfg.Connections {
			if connection.Label == snapshot.BConnection {
				target = connection.ID
				break
			}
		}
		s.mu.RUnlock()
		if target == "" {
			writeError(w, http.StatusNotFound, "no connection named "+snapshot.BConnection, "connection_id")
			return
		}
		if err := s.registry.SetConnection(id, target); err != nil {
			writeError(w, http.StatusConflict, err.Error(), "connection_id")
			return
		}
		updated, _ := s.registry.Get(id)
		writeJSON(w, http.StatusOK, map[string]any{"session": updated.Snapshot()})
		return
	}
	// Item 2hq (v1.6.2): close and delete are distinct HTTP acts. Closing keeps
	// the retained chat and only removes it from the open tab set; reopening is
	// the existing inverse above. Permanent deletion remains DELETE below and
	// refuses an open chat.
	if len(parts) == 2 && parts[1] == "close" && r.Method == http.MethodPost {
		if err := s.registry.Close(id); err != nil {
			status := http.StatusConflict
			if strings.Contains(err.Error(), "not found") {
				status = http.StatusNotFound
			}
			writeError(w, status, err.Error(), "session")
			return
		}
		if s.runner != nil {
			s.runner.LapseSessionGrants(id)
		}
		writeJSON(w, http.StatusOK, map[string]string{"session_id": id})
		return
	}
	switch r.Method {
	case http.MethodPost:
		var body struct {
			Label        *string `json:"label"`
			AgentID      *string `json:"agent_id"`
			ConnectionID *string `json:"connection_id"`
		}
		if !decode(w, r, &body) {
			return
		}
		if body.Label == nil && body.AgentID == nil && body.ConnectionID == nil {
			writeError(w, 400, "label or agent_id is required", "session")
			return
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
		if body.ConnectionID != nil {
			if err := s.registry.SetConnection(id, *body.ConnectionID); err != nil {
				status := http.StatusBadRequest
				field := "connection_id"
				if strings.Contains(err.Error(), "session not found") {
					status, field = http.StatusNotFound, "session"
				} else if strings.Contains(err.Error(), "running") || strings.Contains(err.Error(), "closed") {
					status, field = http.StatusConflict, "session"
				}
				writeError(w, status, err.Error(), field)
				return
			}
		}
		if body.Label != nil {
			if item, ok := s.registry.Get(id); ok && item.Snapshot().Scratch {
				if _, err := s.chatStore.Rename(id, *body.Label); err != nil {
					writeError(w, http.StatusConflict, err.Error(), "label")
					return
				}
			}
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
		if (body.AgentID != nil || body.ConnectionID != nil) && s.runner != nil {
			s.runner.PublishBudget(r.Context(), item)
		}
		writeJSON(w, 200, map[string]any{"session": item.Snapshot()})
	case http.MethodDelete:
		item, ok := s.registry.Get(id)
		if !ok {
			writeError(w, http.StatusNotFound, "session not found", "session")
			return
		}
		// Item 2py (f): DELETE ON AN OPEN OR RUNNING CHAT IS ONE ACT. "if its live chat
		// you close the chat then delete it": a running run is stopped, the chat is
		// closed, and then it is deleted.
		if item.IsRunning() && s.scheduler != nil {
			s.scheduler.Stop(id, false)
		}
		if !item.Snapshot().Closed {
			if err := s.registry.Close(id); err != nil {
				writeError(w, http.StatusConflict, err.Error(), "session")
				return
			}
		}
		// Item 2hq, as 2py changed it: the journal, the scratch folder, its
		// attachments, its history entry and its tab go, and no copy is written;
		// what the chat produced elsewhere - memory notes, plans, files written
		// into a repository - is not the chat and stays.
		inventory, err := s.deleteChat(item)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error(), "session")
			return
		}
		writeJSON(w, 200, map[string]any{"session_id": id, "inventory": inventory})
	default:
		method(w)
	}
}

func (s *Server) deleteChat(item *session.Session) (events.SessionInventory, error) {
	id := item.ID
	if s.runner != nil {
		s.runner.LapseSessionGrants(id)
	}
	// Item 2py (a): DELETE DELETES. No export is written; what was said is gone, and
	// every client — the paired phone included — is told so it drops its copy.
	inventory, err := s.registry.Delete(id)
	if err == nil && s.projector != nil {
		s.projector.Delete(id)
	}
	if err == nil {
		s.bus.Publish(events.New(events.ChatDeleted, "", "", map[string]any{"session_id": id}))
	}
	return inventory, err
}
