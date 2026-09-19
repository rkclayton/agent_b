package web

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"harness/internal/session"
	"harness/internal/worker"
)

// Item 2fc: the Plan page is plan-centric. It lists every plan, shows one
// plan's raw plan.md and its numbers, and runs Go on it, whichever chat (if
// any) is bound to it. These routes take a plan id where the chat-bound ones
// take a session id; both resolve to the same planTarget.

type planTarget struct {
	ID      string
	Dir     string
	Name    string
	Repo    string
	AgentID string
}

var errPlanNotFound = errors.New("plan not found")

func (s *Server) plansRoot() string { return filepath.Join(s.roots.Data, "plans") }

// planByID resolves a plan id against the plans folder's own listing, so a
// request can only name a plan that exists there.
func (s *Server) planByID(id string) (planTarget, error) {
	plans, err := session.ListPlans(s.plansRoot())
	if err != nil {
		return planTarget{}, err
	}
	for _, plan := range plans {
		if plan.ID == id {
			return planTarget{ID: plan.ID, Dir: filepath.Join(s.plansRoot(), plan.ID), Name: plan.Name, Repo: plan.Repo, AgentID: s.planAgent(plan.ID)}, nil
		}
	}
	return planTarget{}, errPlanNotFound
}

// planAgent is the agent a plan's worker runs for: the agent of an open chat
// bound to the plan, else the default agent.
func (s *Server) planAgent(planID string) string {
	for _, item := range s.planners(planID) {
		return item.AgentID
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.DefaultAgentID()
}

// planners are the open d chats bound to a plan, oldest first.
func (s *Server) planners(planID string) []*session.Session {
	var bound []*session.Session
	for _, item := range s.registry.List() {
		if item.Role == "d" && item.PlanID == planID && !item.IsClosed() {
			bound = append(bound, item)
		}
	}
	return bound
}

// planTargetFor is the plan a request names: plan_id, or the plan the chat in
// session_id is bound to.
func (s *Server) planTargetFor(sessionID, planID string) (planTarget, *session.Session, error) {
	if planID != "" {
		target, err := s.planByID(planID)
		return target, nil, err
	}
	item, ok := s.registry.Get(sessionID)
	if !ok {
		return planTarget{}, nil, fmt.Errorf("session not found")
	}
	dir, err := s.planDirFor(item)
	if err != nil {
		return planTarget{}, item, err
	}
	snapshot := item.Snapshot()
	return planTarget{ID: snapshot.PlanID, Dir: dir, Name: snapshot.PlanName, Repo: snapshot.PlanRepo, AgentID: snapshot.AgentID}, item, nil
}

// planPage answers GET /api/plan?plan_id=: the raw plan and its numbers.
func (s *Server) planPage(w http.ResponseWriter, planID string) {
	target, err := s.planByID(planID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error(), "plan_id")
		return
	}
	path := filepath.Join(target.Dir, "plan.md")
	text, err := os.ReadFile(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "plan.md was not found", "plan")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"plan_id": target.ID, "name": target.Name, "repo": target.Repo, "plan": string(text),
		"refusal": planRefusal(target.Dir), "stats": s.planStats(target, string(text), path),
	})
}

// planStats are the numbers the flyout summarises and the right side shows in
// full: items by marker, when plan.md last changed, the last Go this run of
// Agent_b started, the plans nested in or around this one's repository, and
// the chats bound to it.
func (s *Server) planStats(target planTarget, text, path string) map[string]any {
	markers := map[string]int{"open": 0, "done": 0, "stuck": 0, "partial": 0, "dropped": 0}
	for _, item := range worker.Parse(text) {
		switch item.Marker {
		case "x":
			markers["done"]++
		case "!":
			markers["stuck"]++
		case "~":
			markers["partial"]++
		case "-":
			markers["dropped"]++
		default:
			markers["open"]++
		}
	}
	stats := map[string]any{"markers": markers, "running": s.worker != nil && s.worker.Running(target.ID)}
	if info, err := os.Stat(path); err == nil {
		stats["changed_at"] = info.ModTime().UTC().Format(time.RFC3339)
	}
	s.mu.RLock()
	started := s.workerStates[target.ID].startedAt
	s.mu.RUnlock()
	if !started.IsZero() {
		stats["last_go"] = started.UTC().Format(time.RFC3339)
	}
	if s.worker != nil {
		if summary, _, present := s.worker.Result(target.ID); present {
			stats["last_result"] = summary
		}
	}
	related := []map[string]string{}
	if plans, err := session.ListPlans(s.plansRoot()); err == nil && target.Repo != "" {
		for _, plan := range plans {
			if plan.ID == target.ID || plan.Repo == "" {
				continue
			}
			if nestedPath(target.Repo, plan.Repo) || nestedPath(plan.Repo, target.Repo) {
				related = append(related, map[string]string{"id": plan.ID, "name": plan.Name, "repo": plan.Repo})
			}
		}
	}
	stats["related"] = related
	chats := []map[string]string{}
	for _, item := range s.planners(target.ID) {
		chats = append(chats, map[string]string{"id": item.ID, "label": item.Snapshot().Label})
	}
	stats["chats"] = chats
	return stats
}

func nestedPath(root, candidate string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// createPlan answers POST /api/plans {repo}: the flyout's +. The folder goes
// through the same registration every other route uses — its refusals, its
// template, its plan.created — and an existing plan for the folder is returned
// rather than duplicated.
func (s *Server) createPlan(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repo string `json:"repo"`
	}
	if !decode(w, r, &body) {
		return
	}
	repo := strings.Trim(strings.TrimSpace(body.Repo), `"`)
	if repo == "" || !filepath.IsAbs(repo) {
		writeError(w, http.StatusBadRequest, "type the full path of an existing folder", "repo")
		return
	}
	plan, created, err := s.registry.EnsurePlan(repo)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "repo")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plan": plan, "created": created})
}

// planBuildDraft is the planning chat's opening message. v0.70.1 overrule
// (the planner's, vetoable by the operator): the operator's Yes to "Build plan
// now?" is the consent, so the harness sends exactly this fixed request as the
// chat's first message; it composes no other message in the operator's name.
const planBuildDraft = "Read this repository and draft its plan: the product and end goals, the architecture, and the first items, as plan-edit proposals."

// buildPlan answers POST /api/plans/build {plan_id, agent_id}: the planning
// chat for the plan — an open d chat bound to it, or a new one — on the
// agent's d profile, or b when none is assigned.
// buildPlanMu serialises "Build plan now?": two requests for one plan find or
// create one planning chat and send its opening request once (v0.70.1 cold
// review).
var buildPlanMu sync.Mutex

func (s *Server) buildPlan(w http.ResponseWriter, r *http.Request) {
	buildPlanMu.Lock()
	defer buildPlanMu.Unlock()
	var body struct {
		PlanID  string `json:"plan_id"`
		AgentID string `json:"agent_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	target, err := s.planByID(body.PlanID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error(), "plan_id")
		return
	}
	if bound := s.planners(target.ID); len(bound) > 0 {
		s.openPlanning(w, r, http.StatusOK, bound[0], map[string]any{"reused": true})
		return
	}
	agentID := body.AgentID
	if agentID == "" {
		agentID = target.AgentID
	}
	// With no d profile the planner is a b chat in the plan's repository, the
	// fallback the Plan page already names (2t-i): b plans, reviews are off.
	s.mu.RLock()
	agent, known := s.cfg.Agent(agentID)
	hasPlanner := known && agent.ProfileFor("d") != ""
	s.mu.RUnlock()
	if !hasPlanner {
		if target.Repo == "" {
			writeError(w, http.StatusBadRequest, "this plan names no repository to plan in", "plan_id")
			return
		}
		// An open b chat already planning in this repository is reused, so a
		// second Yes does not start a second planning chat.
		for _, item := range s.registry.List() {
			if item.Role == "b" && !item.IsClosed() && strings.EqualFold(filepath.Clean(item.Workspace), filepath.Clean(target.Repo)) {
				s.openPlanning(w, r, http.StatusOK, item, map[string]any{"fallback": true, "reused": true})
				return
			}
		}
		created, err := s.registry.Create("", agentID, target.Repo)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error(), "agent_id")
			return
		}
		s.openPlanning(w, r, http.StatusCreated, created, map[string]any{"fallback": true})
		return
	}
	created, err := s.registry.CreateRole("", agentID, "", "d", target.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "agent_id")
		return
	}
	s.openPlanning(w, r, http.StatusCreated, created, map[string]any{})
}

// openPlanning answers "Build plan now?" with the planning chat. A chat that
// has no message yet is sent the opening request, so its first turn begins on
// the operator's Yes; a chat already under way is only opened, with the
// request offered as a draft rather than sent a second time.
func (s *Server) openPlanning(w http.ResponseWriter, r *http.Request, status int, item *session.Session, extra map[string]any) {
	payload := map[string]any{"session_id": item.ID, "draft": planBuildDraft, "sent": false}
	for key, value := range extra {
		payload[key] = value
	}
	if len(item.MessagesCopy()) == 0 && s.scheduler != nil {
		result, err := s.scheduler.Submit(r.Context(), item.ID, planBuildDraft)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error(), "session_id")
			return
		}
		payload["sent"], payload["run_id"] = true, result.RunID
	}
	payload["session"] = item.Snapshot()
	writeJSON(w, status, payload)
}

func (s *Server) buildPlanRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	s.buildPlan(w, r)
}

// workerProfileBusy is why Go cannot start now, or "": the profile the worker
// would run on is serving as many runs as it may (item 2fc).
func (s *Server) workerProfileBusy(agentID string) string {
	if s.scheduler == nil {
		return ""
	}
	s.mu.RLock()
	agent, ok := s.cfg.Agent(agentID)
	var profileID, label string
	if ok {
		profileID = agent.ProfileFor("c")
		if profile, found := s.cfg.Profile(profileID); found {
			label = profile.Label
		}
	}
	s.mu.RUnlock()
	if profileID == "" {
		return ""
	}
	if busy, role := s.scheduler.ProfileBusy(profileID); busy {
		if label == "" {
			label = profileID
		}
		return fmt.Sprintf("the model is busy: %s is running on %s; Go waits for it to finish", role, label)
	}
	return ""
}
