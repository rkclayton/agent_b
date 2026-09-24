package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
)

func runSingleReply(t *testing.T, delta map[string]any) (string, string, events.Message) {
	t.Helper()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeStreamChunk(t, w, map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 4}})
	}))
	defer model.Close()
	root := t.TempDir()
	cfg := config.Defaults(root)
	cfg.Context.Accounting = "estimated"
	connection := cfg.Connections[0]
	connection.ID, connection.BaseURL, connection.Model = "main", model.URL, "fake"
	connection.Context.NCtx, connection.Context.ReserveOutput = 32768, 4096
	connection.Capabilities.Streaming, connection.Capabilities.ToolCalls = true, true
	cfg.Connections = []config.Connection{connection}
	runner := NewRunner(events.NewBus(), tools.New(), &PromptRenderer{text: "system"}, func(id string) (*config.Connection, bool) { return &connection, id == connection.ID }, func() config.Config { return cfg })
	item := &session.Session{ID: "main", ConnectionID: connection.ID, Workspace: root, Runnable: true, ToolsEnabled: map[string]bool{}, ToolCalls: map[string]int{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	item.Append(events.Message{ID: "u1", Role: "user", Content: "continue", Category: "history"})
	reason, detail, _ := runner.Run(context.Background(), item, "r1")
	messages := item.MessagesCopy()
	return reason, detail, messages[len(messages)-1]
}

func TestReasoningOnlyReplyIsShownAndNamed(t *testing.T) {
	reason, detail, reply := runSingleReply(t, map[string]any{"reasoning_content": "The answer is forty-two."})
	if reason != "reply_empty_reasoning_shown" || detail != "reply empty — reasoning shown" {
		t.Fatalf("run=(%q,%q)", reason, detail)
	}
	if !strings.Contains(reply.Content, "The answer is forty-two.") || !strings.Contains(reply.Content, "answer taken from the model's reasoning") || reply.Reasoning != "The answer is forty-two." {
		t.Fatalf("reply=%+v", reply)
	}
}

func TestAnnouncedActionWithoutToolIsNotDone(t *testing.T) {
	reason, detail, reply := runSingleReply(t, map[string]any{"content": "Now I'll write the game as a single self-contained file."})
	if reason != "announced_action_and_stopped" || detail != "announced an action and stopped" {
		t.Fatalf("run=(%q,%q)", reason, detail)
	}
	if !strings.Contains(reply.Content, "Now I'll write") {
		t.Fatalf("reply=%+v", reply)
	}
}
