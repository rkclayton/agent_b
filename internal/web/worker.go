package web

import (
	"context"
	"errors"
	"net/http"
	"time"

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
	target, _, err := s.planTargetFor(r.URL.Query().Get("session_id"), r.URL.Query().Get("plan_id"))
	if err != nil {
		status := http.StatusBadRequest
		if err.Error() == "session not found" || errors.Is(err, errPlanNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error(), "session_id")
		return
	}
	planDir := target.Dir
	if target.ID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"waiting": false, "running": false, "enabled": false, "items": 0})
		return
	}
	plan := &worker.Plan{Dir: planDir}
	text, err := plan.Read()
	if err != nil {
		writeError(w, http.StatusNotFound, "plan.md was not found", "plan")
		return
	}
	running := s.worker != nil && s.worker.Running(target.ID)
	items := worker.Parse(text)
	refusal := planRefusal(planDir)
	// Item 2bq: the plan lint runs before Go; an error refuses it and every
	// finding is drawn on the panel.
	diagnostics := worker.Lint(planDir)
	if refusal == "" {
		refusal = worker.Refusal(diagnostics)
	}
	// Item 2fc: Go refuses while the worker's model is at its limit, rather
	// than queueing the worker behind a planner or a chat on that connection.
	if refusal == "" && !running {
		refusal = s.workerConnectionBusy(target.AgentID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"waiting":     worker.RemainingIn(items, planDir),
		"running":     running,
		"enabled":     worker.RemainingIn(items, planDir) && !running && refusal == "",
		"items":       len(items),
		"refusal":     refusal,
		"diagnostics": diagnostics,
	})
}

func (s *Server) planGoStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string `json:"session_id"`
		PlanID    string `json:"plan_id"`
		Stop      bool   `json:"stop"`
	}
	if !decode(w, r, &body) {
		return
	}
	if s.worker == nil {
		writeError(w, http.StatusServiceUnavailable, "the worker is not available on this build", "worker")
		return
	}
	var target planTarget
	if body.PlanID != "" {
		found, err := s.planByID(body.PlanID)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error(), "plan_id")
			return
		}
		target = found
	} else {
		item, ok := s.registry.Get(body.SessionID)
		if !ok {
			writeError(w, http.StatusNotFound, "session not found", "session_id")
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
		planDir, err := s.planDirFor(item)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error(), "session_id")
			return
		}
		target = planTarget{ID: snapshot.PlanID, Dir: planDir, Repo: snapshot.PlanRepo, AgentID: snapshot.AgentID}
	}
	if body.Stop {
		writeJSON(w, http.StatusOK, map[string]any{"stopped": s.worker.Stop(target.ID, s.workerSessionID(target.ID))})
		return
	}
	planDir := target.Dir
	if s.worker.Running(target.ID) {
		writeError(w, http.StatusConflict, "a worker is already running on this plan", "worker")
		return
	}
	if refusal := planRefusal(planDir); refusal != "" {
		writeError(w, http.StatusConflict, refusal, "plan")
		return
	}
	if refusal := worker.Refusal(worker.Lint(planDir)); refusal != "" {
		writeError(w, http.StatusConflict, refusal, "plan")
		return
	}
	if refusal := s.workerConnectionBusy(target.AgentID); refusal != "" {
		writeError(w, http.StatusConflict, refusal, "worker")
		return
	}

	// The worker is its own session: a c-role one, bound to the same plan and
	// repo, with no chat. The d-session that pressed Go keeps its own thread.
	created, err := s.registry.CreateRole("worker", target.AgentID, "", "c", target.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "worker")
		return
	}
	plan := &worker.Plan{Dir: planDir}
	repo := target.Repo
	s.setWorkerSession(target.ID, created.ID)
	go func() {
		_, _ = s.worker.Go(context.Background(), created, plan, repo)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"started": created.ID, "plan_id": target.ID})
}

// planWorker reports the done card's summary once a worker has finished.
func (s *Server) planWorker(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	target, _, resolveErr := s.planTargetFor(r.URL.Query().Get("session_id"), r.URL.Query().Get("plan_id"))
	if resolveErr != nil && r.URL.Query().Get("plan_id") != "" {
		writeError(w, http.StatusNotFound, resolveErr.Error(), "plan_id")
		return
	}
	if resolveErr != nil && resolveErr.Error() == "session not found" {
		writeError(w, http.StatusNotFound, "session not found", "session_id")
		return
	}
	planID := target.ID
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
	session   string
	startedAt time.Time
}

func (s *Server) setWorkerSession(planID, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workerStates == nil {
		s.workerStates = map[string]workerState{}
	}
	state := s.workerStates[planID]
	state.session = sessionID
	state.startedAt = time.Now()
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
