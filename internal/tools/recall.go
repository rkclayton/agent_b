package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"harness/internal/events"
	"harness/internal/memory"
	"harness/internal/session"
)

// ChatHistoryReader reads this chat's retained journal; item 2wd moved it here
// when `chat_history` merged into `recall`.
type ChatHistoryReader interface {
	ReadChatHistory(sessionID, query string, offset, limit int) (events.ChatHistoryResult, error)
}

type Recall struct {
	memory *memory.Manager
	record func() ChatHistoryReader
}

func NewRecall(manager *memory.Manager, record func() ChatHistoryReader) *Recall {
	return &Recall{memory: manager, record: record}
}
func (*Recall) Name() string { return "recall" }
func (*Recall) Description() string {
	return "A detail from earlier in this chat or your saved notes is not in front of you — a path, a decision, an error message. Find it here instead of guessing."
}
func (*Recall) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query":  map[string]any{"type": "string", "description": "Optional case-insensitive text to find in this chat's record and your saved notes. Without any argument, every saved note is returned."},
			"offset": map[string]any{"type": "integer", "minimum": 0, "description": "Zero-based matching result offset."},
			"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "description": "Results to return; defaults to 20."},
		},
		"additionalProperties": false,
	}
}

type recallEntry struct {
	Source  string          `json:"source"`
	Layer   string          `json:"layer,omitempty"`
	Note    string          `json:"note,omitempty"`
	Message *events.Message `json:"message,omitempty"`
}

func (r *Recall) Call(_ context.Context, s *session.Session, args map[string]any) (string, error) {
	_, hasQuery := args["query"]
	_, hasOffset := args["offset"]
	_, hasLimit := args["limit"]
	workspace, err := r.memory.Read(s.Workspace)
	if err != nil {
		return "", err
	}
	agent, err := r.memory.ReadForAgent(s.AgentID)
	if err != nil {
		return "", err
	}
	if !hasQuery && !hasOffset && !hasLimit {
		return allNotes(workspace, agent), nil
	}
	query, _ := args["query"].(string)
	query = strings.TrimSpace(query)
	offset, err := historyInteger(args, "offset", 0)
	if err != nil {
		return "", err
	}
	if offset < 0 {
		return "", fmt.Errorf("offset must be at least zero")
	}
	limit, err := historyInteger(args, "limit", 20)
	if err != nil {
		return "", err
	}
	limit = min(max(limit, 1), 50)
	notes := append(matchingNotes("folder", workspace, query), matchingNotes("agent", agent, query)...)
	// This chat's record comes first, then saved notes; each newest first.
	chat, err := r.chatTotal(s.ID, query)
	if err != nil {
		return "", err
	}
	entries := []recallEntry{}
	if high := min(chat, offset+limit); offset < high {
		read, err := r.record().ReadChatHistory(s.ID, query, chat-high, high-offset)
		if err != nil {
			return "", err
		}
		for index := len(read.Entries) - 1; index >= 0; index-- {
			entries = append(entries, recallEntry{Source: "chat", Message: &read.Entries[index]})
		}
	}
	for index := max(0, offset-chat); index < len(notes) && len(entries) < limit; index++ {
		entries = append(entries, notes[index])
	}
	encoded, err := json.Marshal(map[string]any{"entries": entries, "total": chat + len(notes)})
	return string(encoded), err
}

// chatTotal counts this chat's matching messages; a chat with no journal yet,
// or a caller with no journal at all, has none.
func (r *Recall) chatTotal(sessionID, query string) (int, error) {
	if r.record == nil || r.record() == nil {
		return 0, nil
	}
	result, err := r.record().ReadChatHistory(sessionID, query, 0, 1)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	return result.Total, err
}

func allNotes(workspace, agent string) string {
	if workspace == "" && agent == "" {
		return "No saved notes for this folder."
	}
	parts := []string{}
	if workspace != "" {
		parts = append(parts, "Folder memory:\n"+newestFirst(workspace))
	}
	if agent != "" {
		parts = append(parts, "Agent memory:\n"+newestFirst(agent))
	}
	return strings.Join(parts, "\n\n")
}

func matchingNotes(layer, value, query string) []recallEntry {
	if value == "" {
		return nil
	}
	needle := strings.ToLower(query)
	out := []recallEntry{}
	for _, line := range strings.Split(newestFirst(value), "\n") {
		if strings.TrimSpace(line) != "" && strings.Contains(strings.ToLower(line), needle) {
			out = append(out, recallEntry{Source: "note", Layer: layer, Note: line})
		}
	}
	return out
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

func newestFirst(value string) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	for left, right := 0, len(lines)-1; left < right; left, right = left+1, right-1 {
		lines[left], lines[right] = lines[right], lines[left]
	}
	return strings.Join(lines, "\n")
}
