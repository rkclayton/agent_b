package tools

import (
	"context"
	"fmt"

	"harness/internal/events"
	"harness/internal/memory"
	"harness/internal/session"
)

type Remember struct {
	memory *memory.Manager
	bus    *events.Bus
}

func NewRemember(manager *memory.Manager, bus *events.Bus) *Remember {
	return &Remember{memory: manager, bus: bus}
}
func (*Remember) Name() string { return "remember" }
func (*Remember) Description() string {
	return "Save durable memory; recall first to avoid duplicates. agent carries across all chats of this agent: preferences, habits, anything for later chats. folder is the project repository this chat has written to; with none, it saves to agent."
}
func (*Remember) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"note": map[string]any{"type": "string"}, "target": map[string]any{"type": "string", "enum": []string{"folder", "agent"}, "default": "folder"}}, "required": []string{"note"}}
}
func (r *Remember) Call(ctx context.Context, s *session.Session, args map[string]any) (string, error) {
	note, ok := args["note"].(string)
	if !ok {
		return "", fmt.Errorf("note is empty")
	}
	target, _ := args["target"].(string)
	if target == "" {
		target = "folder"
	}
	var path string
	var duplicate bool
	var err error
	scope := ""
	if target == "folder" || target == "workspace" {
		// Item 2fh: a scratch chat has no folder layer of its own. A project fact
		// belongs to the plan repository it is working in; with none in scope the
		// note goes to the agent layer, which every chat of this agent loads.
		folder := s.MemoryFolder()
		if folder == "" {
			target, scope = "agent", " No project was in scope, so it went to the agent layer, which every chat of this agent loads."
		} else {
			target = "folder"
			path, duplicate, err = r.memory.Note(folder, note)
		}
	}
	if target == "agent" {
		path, duplicate, err = r.memory.NoteAgent(s.AgentID, note)
	} else if target != "folder" {
		return "", fmt.Errorf("target must be folder or agent")
	}
	if err != nil {
		return "", err
	}
	if duplicate {
		return "ok: already noted" + scope, nil
	}
	r.bus.Publish(events.New(events.MemoryNoted, s.ID, "", map[string]any{"note": note, "path": path, "target": target, "agent_id": s.AgentID}))
	return "ok: noted; active next session." + scope, nil
}
