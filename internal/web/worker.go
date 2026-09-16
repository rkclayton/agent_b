package web

import (
	"context"
	"net/http"

	"harness/internal/agent"
	"harness/internal/worker"
)

// planGo starts the worker on a plan, or reports what it is doing.
//
// Go is the operator's action, so it is a POST like any mutation; GET answers
// the button's enable state, which is exactly two facts — is there a waiting
// item, and is a worker already running.
func (s *Server) planGo(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.planGoState(w, r)
	case http.MethodPost:
		s.planGoStart(w, r)
	default:
		method(w)
	}
}

func (s *Server) planGoState(w http.ResponseWriter, r *http.Request) {
	item, ok := s.registry.Get(r.URL.Query().Get("session_id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found", "session_id")
		return
	}
	planDir, err := s.planDirFor(item)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "session_id")
		return
	}
	if item.Snapshot().PlanID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"waiting": false, "running": false, "enabled": false, "items": 0})
		return
	}
	plan := &worker.Plan{Dir: planDir}
	text, err := plan.Read()
	if err != nil {
		writeError(w, http.StatusNotFound, "plan.md was not found", "plan")
		return
	}
	running := s.worker != nil && s.worker.Running(item.Snapshot().PlanID)
	items := worker.Parse(text)
	writeJSON(w, http.StatusOK, map[string]any{
		"waiting": worker.Remaining(items),
		"running": running,
		"enabled": worker.Remaining(items) && !running,
		"items":   len(items),
	})
}

func (s *Server) planGoStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string `json:"session_id"`
		Stop      bool   `json:"stop"`
	}
	if !decode(w, r, &body) {
		return
	}
	item, ok := s.registry.Get(body.SessionID)
	if !ok {
		writeError(w, http.StatusNotFound, "session not found", "session_id")
		return
	}
	if s.worker == nil {
		writeError(w, http.StatusServiceUnavailable, "the worker is not available on this build", "worker")
		return
	}
	snapshot := item.Snapshot()
	// Go belongs to a plan, not to a folder. A chat that merely sits in a
	// plan repository has no plan id, and a worker started without one would
	// share a running slot with every other unbound worker.
	if snapshot.PlanID == "" {
		writeError(w, http.StatusBadRequest, "this chat is not bound to a plan", "session_id")
		return
	}
	if body.Stop {
		writeJSON(w, http.StatusOK, map[string]any{"stopped": s.worker.Stop(snapshot.PlanID, s.workerSessionID(snapshot.PlanID))})
		return
	}
	planDir, err := s.planDirFor(item)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "session_id")
		return
	}
	if s.worker.Running(snapshot.PlanID) {
		writeError(w, http.StatusConflict, "a worker is already running on this plan", "worker")
		return
	}
	// The worker is its own session: a c-role one, bound to the same plan and
	// repo, with no chat. The d-session that pressed Go keeps its own thread.
	created, err := s.registry.CreateRole("worker", snapshot.AgentID, "", "c", snapshot.PlanID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "worker")
		return
	}
	plan := &worker.Plan{Dir: planDir}
	repo := snapshot.PlanRepo
	s.setWorkerSession(snapshot.PlanID, created.ID)
	go func() {
		_, _ = s.worker.Go(context.Background(), created, plan, repo)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"started": created.ID, "plan_id": snapshot.PlanID})
}

// planWorker reports the done card's summary once a worker has finished.
func (s *Server) planWorker(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	item, ok := s.registry.Get(r.URL.Query().Get("session_id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found", "session_id")
		return
	}
	planID := item.Snapshot().PlanID
	if s.worker == nil {
		writeJSON(w, http.StatusOK, map[string]any{"done": false})
		return
	}
	summary, err, present := s.worker.Result(planID)
	if !present || s.worker.Running(planID) {
		writeJSON(w, http.StatusOK, map[string]any{"done": false})
		return
	}
	payload := map[string]any{"done": true, "summary": summary}
	if err != "" {
		payload["error"] = err
	}
	writeJSON(w, http.StatusOK, payload)
}


// The session id is kept so Stop can reach the run as well as the loop; the
// outcome itself lives in the driver, which records it before it announces it.
type workerState struct {
	session string
}

func (s *Server) setWorkerSession(planID, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workerStates == nil {
		s.workerStates = map[string]workerState{}
	}
	state := s.workerStates[planID]
	state.session = sessionID
	s.workerStates[planID] = state
}

func (s *Server) workerSessionID(planID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.workerStates[planID].session
}

// schedulerSubmitter adapts the scheduler to the worker's seam: the worker only
// needs to start a run and stop one, and it gets no privileged path to either.
type schedulerSubmitter struct{ scheduler *agent.Scheduler }

func (a schedulerSubmitter) Submit(ctx context.Context, sessionID, text string) (string, error) {
	result, err := a.scheduler.Submit(ctx, sessionID, text)
	return result.RunID, err
}

func (a schedulerSubmitter) Stop(sessionID string, all bool) int {
	return len(a.scheduler.Stop(sessionID, all))
}
