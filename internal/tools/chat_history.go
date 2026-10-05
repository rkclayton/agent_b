package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"harness/internal/events"
	"harness/internal/session"
)

type ChatHistoryReader interface {
	ReadChatHistory(sessionID, query string, offset, limit int) (events.ChatHistoryResult, error)
}

type ChatHistory struct {
	reader func() ChatHistoryReader
}

func NewChatHistory(reader func() ChatHistoryReader) *ChatHistory {
	return &ChatHistory{reader: reader}
}
func (*ChatHistory) Name() string { return "chat_history" }
func (*ChatHistory) Description() string {
	return "Search this chat's retained journal, or read its message entries by zero-based range. Read-only; use it for details from before a fresh-context boundary."
}
func (*ChatHistory) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query":  map[string]any{"type": "string", "description": "Optional case-insensitive text to search in this chat."},
			"offset": map[string]any{"type": "integer", "minimum": 0, "description": "Zero-based matching message offset."},
			"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "description": "Messages to return; defaults to 20."},
		},
		"additionalProperties": false,
	}
}
func (*ChatHistory) AlwaysAvailable() bool { return true }
func (t *ChatHistory) Call(_ context.Context, s *session.Session, args map[string]any) (string, error) {
	reader := t.reader()
	if reader == nil {
		return "", fmt.Errorf("chat history is not available")
	}
	query, _ := args["query"].(string)
	offset, err := historyInteger(args, "offset", 0)
	if err != nil {
		return "", err
	}
	limit, err := historyInteger(args, "limit", 20)
	if err != nil {
		return "", err
	}
	result, err := reader.ReadChatHistory(s.ID, strings.TrimSpace(query), offset, limit)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(result)
	return string(encoded), err
}

func historyInteger(args map[string]any, name string, fallback int) (int, error) {
	value, ok := args[name]
	if !ok {
		return fallback, nil
	}
	number, ok := value.(float64)
	if !ok || number != float64(int(number)) {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return int(number), nil
}
