package tools

import (
	"context"
	"fmt"
	"strings"

	"harness/internal/session"
)

type Todo struct{}

func NewTodo() *Todo              { return &Todo{} }
func (*Todo) Name() string        { return "todo" }
func (*Todo) Description() string { return "Replace the visible task list for this chat." }
func (*Todo) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"items": map[string]any{"type": "array", "maxItems": 20, "items": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string", "maxLength": 120}, "status": map[string]any{"type": "string", "enum": []string{"pending", "doing", "done"}}}, "required": []string{"text", "status"}, "additionalProperties": false}}}, "required": []string{"items"}, "additionalProperties": false}
}
func (*Todo) Call(_ context.Context, _ *session.Session, args map[string]any) (string, error) {
	items, ok := args["items"].([]any)
	if !ok || len(items) > 20 {
		return "", fmt.Errorf("items must be an array of at most 20 tasks")
	}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return "", fmt.Errorf("each task must be an object")
		}
		text, _ := item["text"].(string)
		status, _ := item["status"].(string)
		if text = strings.TrimSpace(text); text == "" || len([]rune(text)) > 120 {
			return "", fmt.Errorf("task text must be 1 to 120 characters")
		}
		if status != "pending" && status != "doing" && status != "done" {
			return "", fmt.Errorf("task status must be pending, doing, or done")
		}
	}
	return "task list updated", nil
}
