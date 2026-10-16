package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
)

func TestProviderOverloadIsRetried2rr(t *testing.T) {
	if !retryableModelCall(fmt.Errorf("chat stream HTTP 529: overloaded")) {
		t.Fatal("provider overload was not retryable")
	}
	if retryableModelCall(fmt.Errorf("chat stream HTTP 401: refused")) {
		t.Fatal("refused key was retryable")
	}
}

func fallbackConnectionFixture(t *testing.T, mainURL, fallbackURL, fallbackID string, fallbackWindow int) (*Runner, *session.Session, *capturedBus, *config.Config) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Defaults(root)
	cfg.Context.Accounting = "estimated"
	main := cfg.Connections[0]
	main.ID, main.Label, main.BaseURL, main.Model, main.Fallback = "main", "Main", mainURL, "main-model", fallbackID
	main.Context.NCtx, main.Context.ReserveOutput = 32768, 4096
	main.Capabilities.Streaming, main.Capabilities.ToolCalls = true, true
	fallback := main
	fallback.ID, fallback.Label, fallback.BaseURL, fallback.Model, fallback.Fallback = "backup", "Backup", fallbackURL, "backup-model", ""
	fallback.Context.NCtx, fallback.Context.ReserveOutput = fallbackWindow, min(4096, fallbackWindow/2)
	cfg.Connections = []config.Connection{main, fallback}
	bus := newCapturedBus()
	lookup := func(id string) (*config.Connection, bool) {
		for i := range cfg.Connections {
			if cfg.Connections[i].ID == id {
				return &cfg.Connections[i], true
			}
		}
		return nil, false
	}
	runner := NewRunner(bus.Bus, tools.New(), &PromptRenderer{text: "system"}, lookup, func() config.Config { return cfg })
	runner.modelRetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	item := &session.Session{ID: "main", ConnectionID: main.ID, Workspace: root, Runnable: true, ToolsEnabled: map[string]bool{}, ToolCalls: map[string]int{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	return runner, item, bus, &cfg
}

func streamReply(t *testing.T, w http.ResponseWriter, text string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	writeStreamChunk(t, w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": text}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 2}})
}

func TestUnreachableMainFallsBackWithNoticeAndNextMessageReturnsToMain2ro(t *testing.T) {
	var mainUp atomic.Bool
	var mainCalls, backupCalls atomic.Int32
	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mainCalls.Add(1)
		if !mainUp.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		streamReply(t, w, "main answer")
	}))
	defer main.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backupCalls.Add(1)
		streamReply(t, w, "backup answer")
	}))
	defer backup.Close()
	runner, item, bus, _ := fallbackConnectionFixture(t, main.URL, backup.URL, "backup", 32768)
	item.Append(events.Message{ID: "u1", Role: "user", Content: "first", Category: "history"})
	if reason, detail, _ := runner.Run(context.Background(), item, "r1"); reason != "done" || detail != "" {
		t.Fatalf("fallback run=(%q,%q)", reason, detail)
	}
	if mainCalls.Load() != 4 || backupCalls.Load() != 1 {
		t.Fatalf("calls main=%d backup=%d", mainCalls.Load(), backupCalls.Load())
	}
	notice := "Main did not answer — continuing on Backup"
	found := false
	for _, event := range bus.Recent(item.ID) {
		if event.Type == events.SessionUpdated {
			data, _ := event.Data.(map[string]any)
			found = found || data["notice"] == notice
		}
	}
	if !found {
		t.Fatalf("notice %q was not journalled", notice)
	}
	mainUp.Store(true)
	item.Append(events.Message{ID: "u2", Role: "user", Content: "second", Category: "history"})
	if reason, detail, _ := runner.Run(context.Background(), item, "r2"); reason != "done" || detail != "" {
		t.Fatalf("next run=(%q,%q)", reason, detail)
	}
	if mainCalls.Load() != 5 || backupCalls.Load() != 1 {
		t.Fatalf("next message calls main=%d backup=%d", mainCalls.Load(), backupCalls.Load())
	}
}

func TestFallbackThatCannotFitNamesBothConnectionsAndSizes2ro(t *testing.T) {
	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "unavailable", http.StatusBadGateway) }))
	defer main.Close()
	var backupCalls atomic.Int32
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { backupCalls.Add(1); streamReply(t, w, "wrong") }))
	defer backup.Close()
	runner, item, _, _ := fallbackConnectionFixture(t, main.URL, backup.URL, "backup", 64)
	item.Append(events.Message{ID: "u1", Role: "user", Content: strings.Repeat("large request ", 200), Category: "history"})
	reason, detail, _ := runner.Run(context.Background(), item, "r1")
	if reason != "context_exhausted" || !strings.Contains(detail, "Main (32768 tokens)") || !strings.Contains(detail, "Backup (64 tokens)") || !strings.Contains(detail, "request") {
		t.Fatalf("run=(%q,%q)", reason, detail)
	}
	if backupCalls.Load() != 0 {
		t.Fatalf("oversize fallback was contacted %d times", backupCalls.Load())
	}
}

func TestClientErrorAndNoneDoNotSwitch2ro(t *testing.T) {
	for _, test := range []struct {
		name, fallback string
		status         int
	}{
		{name: "400", fallback: "backup", status: http.StatusBadRequest},
		{name: "none", fallback: "", status: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			var bodies [][]byte
			main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				bodies = append(bodies, body)
				http.Error(w, "fixture", test.status)
			}))
			defer main.Close()
			var backupCalls atomic.Int32
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { backupCalls.Add(1); streamReply(t, w, "wrong") }))
			defer backup.Close()
			runner, item, bus, _ := fallbackConnectionFixture(t, main.URL, backup.URL, test.fallback, 32768)
			item.Append(events.Message{ID: "u1", Role: "user", Content: "hello", Category: "history"})
			reason, _, _ := runner.Run(context.Background(), item, "r1")
			if reason != "model_error" || backupCalls.Load() != 0 {
				t.Fatalf("reason=%q backup_calls=%d", reason, backupCalls.Load())
			}
			if test.name == "none" && (len(bodies) != 4 || !bytes.Equal(bodies[0], bodies[3])) {
				t.Fatalf("none changed ordinary retry bytes: requests=%d", len(bodies))
			}
			for _, event := range bus.Recent(item.ID) {
				if event.Type == events.SessionUpdated && strings.Contains(fmt.Sprint(event.Data), "continuing on") {
					t.Fatalf("unexpected fallback notice: %#v", event.Data)
				}
			}
		})
	}
}

func runSingleReply(t *testing.T, delta map[string]any) (string, string, events.Message) {
	return runSingleReplyAtContext(t, delta, 32768)
}

func runSingleReplyAtContext(t *testing.T, delta map[string]any, nctx int) (string, string, events.Message) {
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
	connection.Context.NCtx, connection.Context.ReserveOutput = nctx, 4096
	connection.Capabilities.NCtx = 32768
	connection.Capabilities.Streaming, connection.Capabilities.ToolCalls = true, true
	cfg.Connections = []config.Connection{connection}
	runner := NewRunner(events.NewBus(), tools.New(), &PromptRenderer{text: "system"}, func(id string) (*config.Connection, bool) { return &connection, id == connection.ID }, func() config.Config { return cfg })
	item := &session.Session{ID: "main", ConnectionID: connection.ID, Workspace: root, Runnable: true, ToolsEnabled: map[string]bool{}, ToolCalls: map[string]int{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	item.Append(events.Message{ID: "u1", Role: "user", Content: "continue", Category: "history"})
	reason, detail, _ := runner.Run(context.Background(), item, "r1")
	messages := item.MessagesCopy()
	return reason, detail, messages[len(messages)-1]
}

func TestUnknownAndOverstatedContextsStillRunAgainstFixture2qr(t *testing.T) {
	for _, nctx := range []int{0, 32_000_000} {
		reason, detail, reply := runSingleReplyAtContext(t, map[string]any{"content": "ok"}, nctx)
		if reason != "done" || detail != "" || reply.Content != "ok" {
			t.Fatalf("n_ctx=%d reason=%q detail=%q reply=%+v", nctx, reason, detail, reply)
		}
	}
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

func TestAnnouncedActionWithoutToolIsTheDoneReply2vf(t *testing.T) {
	reason, detail, reply := runSingleReply(t, map[string]any{"content": "Now I'll write the game as a single self-contained file."})
	if reason != "done" || detail != "" {
		t.Fatalf("run=(%q,%q)", reason, detail)
	}
	if !strings.Contains(reply.Content, "Now I'll write") {
		t.Fatalf("reply=%+v", reply)
	}
}
