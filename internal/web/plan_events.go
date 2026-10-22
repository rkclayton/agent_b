package web

import (
	"path/filepath"
	"time"

	"harness/internal/events"
	"harness/internal/session"
)

// PublishPlanChanges makes every new plan and every plan.md rewrite an event
// on the page's stream: plan.created / plan.updated carry the
// plan's id and its current list entry, so each open page follows the plan the
// moment it changes, whichever route changed it. No route removes a plan
// folder today, so plan.removed has no emitter yet.
func (s *Server) PublishPlanChanges() {
	root := filepath.Join(s.profileRoot(), "plans")
	session.PlanChanged = func(kind, planDir string) {
		id := ""
		data := map[string]any{}
		if plans, err := session.ListPlans(root); err == nil {
			for _, plan := range plans {
				if session.SamePath(plan.Repo, planDir) {
					id = plan.ID
					data["plan"] = plan
				}
			}
		}
		data["plan_id"] = id
		s.bus.Publish(events.New(kind, "", "", data))
	}
}

func (s *Server) planList() []session.Plan {
	plans, err := session.ListPlans(filepath.Join(s.profileRoot(), "plans"))
	if err != nil {
		return []session.Plan{}
	}
	for i := range plans {
		for _, chat := range s.registry.List() {
			snapshot := chat.Snapshot()
			if !snapshot.Closed && (snapshot.PlanID == plans[i].ID || session.SamePath(snapshot.Workspace, plans[i].Repo)) {
				plans[i].OpenChats++
			}
		}
		if state, ok := s.workerStates[plans[i].ID]; ok && !state.startedAt.IsZero() {
			plans[i].LastGo = state.startedAt.Format(time.RFC3339Nano)
		}
	}
	return plans
}
