package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"harness/internal/events"
	"harness/internal/session"
)

// Item 2qh CHECK 2: an earlier result removed from live context remains
// searchable through the read-only tool backed by this chat's own journal.
func TestChatHistoryFindsPreFreshToolResult2qh(t *testing.T) {
	writers, err := events.NewWriters(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	if err := writers.Write(events.New(events.MessageAppended, "chat-1", "run", map[string]any{"message": events.Message{ID: "old-result", Role: "tool", Category: "files", Name: "read_file", Content: "needle-before-fresh-boundary"}})); err != nil {
		t.Fatal(err)
	}

	tool := NewChatHistory(func() ChatHistoryReader { return writers })
	item := &session.Session{ID: "chat-1"}
	registry := New(tool)
	if names := registry.Names(map[string]bool{}); len(names) != 1 || names[0] != "chat_history" {
		t.Fatalf("always-offered names=%v", names)
	}
	outcome := registry.CallDetailed(context.Background(), item, "chat_history", map[string]any{"query": "needle-before-fresh-boundary", "limit": float64(5)})
	result, err := outcome.Content, error(nil)
	if !outcome.OK {
		err = fmt.Errorf("call failed: %s", outcome.Content)
	}
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "needle-before-fresh-boundary") || !strings.Contains(result, "old-result") {
		t.Fatalf("result=%s", result)
	}
}
