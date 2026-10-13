package tools

import (
	"context"
	"fmt"
	"strings"

	"harness/internal/session"
)

type Ask struct{}

func NewAsk() *Ask               { return &Ask{} }
func (*Ask) Name() string        { return "ask" }
func (*Ask) Description() string { return "Ask the user one question and wait for their answer." }
func (*Ask) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"question": map[string]any{"type": "string", "maxLength": 1000}, "choices": map[string]any{"type": "array", "maxItems": 4, "items": map[string]any{"type": "string", "maxLength": 60}}}, "required": []string{"question"}, "additionalProperties": false}
}
func (*Ask) Question(args map[string]any) (string, []string, error) {
	question, _ := args["question"].(string)
	question = strings.TrimSpace(question)
	if question == "" || len([]rune(question)) > 1000 {
		return "", nil, fmt.Errorf("question must be 1 to 1000 characters")
	}
	raw, _ := args["choices"].([]any)
	if len(raw) > 4 {
		return "", nil, fmt.Errorf("choices may contain at most 4 items")
	}
	choices := make([]string, 0, len(raw))
	for _, value := range raw {
		choice, ok := value.(string)
		choice = strings.TrimSpace(choice)
		if !ok || choice == "" || len([]rune(choice)) > 60 {
			return "", nil, fmt.Errorf("each choice must be 1 to 60 characters")
		}
		choices = append(choices, choice)
	}
	return question, choices, nil
}
func (*Ask) Call(context.Context, *session.Session, map[string]any) (string, error) {
	return "", fmt.Errorf("ask is available only through the run loop")
}
