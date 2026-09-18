package agent

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"harness/internal/session"
)

var addPlanPattern = regexp.MustCompile(`(?i)^\s*add\s+(.+?)\s+as\s+a\s+plan[.!]?\s*$`)

func requestedPlanPath(text string) (string, bool) {
	match := addPlanPattern.FindStringSubmatch(text)
	if len(match) != 2 {
		return "", false
	}
	value := strings.TrimSpace(match[1])
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	if value == "" || !filepath.IsAbs(filepath.FromSlash(value)) {
		return "", false
	}
	return filepath.Clean(filepath.FromSlash(value)), true
}

func (r *Runner) handlePlanRegistration(ctx context.Context, item *session.Session, runID string) (bool, string) {
	messages := item.MessagesCopy()
	if len(messages) == 0 || messages[len(messages)-1].Role != "user" {
		return false, ""
	}
	path, requested := requestedPlanPath(messages[len(messages)-1].Content)
	if !requested {
		return false, ""
	}
	return true, r.registerPlan(ctx, item, runID, path, "plan registration")
}

// handleModelPlanProposal lets the model raise the operator's own registration
// card (item 2fa): a final model message that is exactly
// "Add <absolute-path> as a plan" asks, through the same Allow-this card, named
// so the card says the model proposed it. Registration only; the worker never
// proposes, and b's read-only relation to plan files is unchanged.
func (r *Runner) handleModelPlanProposal(ctx context.Context, item *session.Session, runID, content string) (bool, string) {
	if item.Snapshot().Role == "c" {
		return false, ""
	}
	path, requested := requestedPlanPath(strings.TrimSpace(content))
	if !requested {
		return false, ""
	}
	return true, r.registerPlan(ctx, item, runID, path, "plan registration proposed by agent_b")
}

func (r *Runner) registerPlan(ctx context.Context, item *session.Session, runID, path, name string) string {
	if item.RegisterPlan == nil {
		return "plan registration is unavailable"
	}
	callID := r.id("plan-registration")
	approved, err := r.gate.WaitPolicyRequired(ctx, item, runID, callID, name, map[string]any{
		"path": path,
	})
	if err != nil {
		return "plan registration approval ended: " + err.Error()
	}
	if !approved {
		return "plan registration declined"
	}
	plan, created, err := item.RegisterPlan(path)
	if err != nil {
		return "plan registration failed: " + err.Error()
	}
	if !created {
		return "plan already exists: " + plan.Name
	}
	return "plan registered: " + plan.Name
}
