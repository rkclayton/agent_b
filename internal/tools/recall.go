package tools

import (
	"context"
	"strings"

	"harness/internal/memory"
	"harness/internal/session"
)

type Recall struct {
	memory *memory.Manager
}

func NewRecall(manager *memory.Manager) *Recall { return &Recall{memory: manager} }
func (*Recall) Name() string                    { return "recall" }
func (*Recall) Description() string {
	return "Read durable folder and agent notes; takes no arguments. Use before remember to avoid duplicates; recall never writes."
}
func (*Recall) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (r *Recall) Call(_ context.Context, s *session.Session, _ map[string]any) (string, error) {
	workspace, err := r.memory.Read(s.Workspace)
	if err != nil {
		return "", err
	}
	agent, err := r.memory.ReadForAgent(s.AgentID)
	if err != nil {
		return "", err
	}
	if workspace == "" && agent == "" {
		return "No saved notes for this folder.", nil
	}
	parts := []string{}
	if workspace != "" {
		parts = append(parts, "Folder memory:\n"+newestFirst(workspace))
	}
	if agent != "" {
		parts = append(parts, "Agent memory:\n"+newestFirst(agent))
	}
	return strings.Join(parts, "\n\n"), nil
}

func newestFirst(value string) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	for left, right := 0, len(lines)-1; left < right; left, right = left+1, right-1 {
		lines[left], lines[right] = lines[right], lines[left]
	}
	return strings.Join(lines, "\n")
}
