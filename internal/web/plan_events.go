package web

import (
	"path/filepath"

	"harness/internal/events"
	"harness/internal/session"
)

// PublishPlanChanges makes every new plan and every plan.md rewrite an event
// on the page's stream (item 2bq): plan.created / plan.updated carry the
// plan's id and its current list entry, so each open page follows the plan the
// moment it changes, whichever route changed it. No route removes a plan
// folder today, so plan.removed has no emitter yet.
func (s *Server) PublishPlanChanges() {
	root := filepath.Join(s.roots.Data, "plans")
	session.PlanChanged = func(kind, planDir string) {
		id := filepath.Base(planDir)
		data := map[string]any{"plan_id": id}
		if plans, err := session.ListPlans(root); err == nil {
			for _, plan := range plans {
				if plan.ID == id {
					data["plan"] = plan
				}
			}
		}
		s.bus.Publish(events.New(kind, "", "", data))
	}
}

func (s *Server) planList() []session.Plan {
	plans, err := session.ListPlans(filepath.Join(s.roots.Data, "plans"))
	if err != nil {
		return []session.Plan{}
	}
	return plans
}
