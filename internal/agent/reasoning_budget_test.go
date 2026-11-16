package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/llm"
	"harness/internal/session"
	"harness/internal/tools"
)

func reasoningBudgetRunner2xa(t *testing.T, handler http.Handler, configure ...func(*config.Connection)) (*Runner, *session.Session, *capturedBus, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		handler.ServeHTTP(w, request)
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	cfg := config.Defaults(root)
	cfg.Context.Accounting = "estimated"
	connection := cfg.Connections[0]
	connection.ID, connection.Label, connection.BaseURL, connection.Model = "main", "main", server.URL, "test-model"
	connection.Context.NCtx, connection.Context.ReserveOutput = 32768, 10240
	connection.Reasoning = config.Reasoning{Control: "chat_template_kwargs", Enabled: true}
	connection.Capabilities.NCtx, connection.Capabilities.Streaming = 32768, true
	connection.Capabilities.OverflowBehavior = "error"
	connection.Capabilities.Findings = []string{"server reasoning budget: rejected; key: reasoning_budget_tokens"}
	for _, change := range configure {
		change(&connection)
	}
	cfg.Connections = []config.Connection{connection}
	cfg.Agents = []config.Agent{{Name: "Main", B: "main", Thinking: true, Toolset: config.FullToolset()}}
	bus := newCapturedBus()
	runner := NewRunner(bus.Bus, tools.New(), &PromptRenderer{text: "system"}, cfg.Connection, func() config.Config { return cfg })
	item := &session.Session{ID: "main", AgentID: "Main", ConnectionID: "main", Workspace: root, Runnable: true, ToolsEnabled: map[string]bool{}, ToolCalls: map[string]int{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	item.Append(events.Message{ID: "u1", Role: "user", Content: "work", Category: "history"})
	return runner, item, bus, &calls
}

func TestReportedContextRunsWithoutSavedSize2yg(t *testing.T) {
	for _, tc := range []struct{ saved, reported int }{{0, 32768}, {0, 0}, {64000, 32768}} {
		saved := tc.saved
		runner, s, bus, _ := reasoningBudgetRunner2xa(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/props" {
				_ = json.NewEncoder(w).Encode(map[string]any{"n_ctx": 32768})
				return
			}
			writeStreamChunk(t, w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "answer"}, "finish_reason": "stop"}}})
		}), func(c *config.Connection) { c.Context.NCtx = saved; c.Capabilities.NCtx = tc.reported })
		if reason, detail, _ := runner.Run(context.Background(), s, "fixture"); reason != "done" {
			t.Fatalf("%s: %s", reason, detail)
		}
		want := saved
		if want == 0 {
			want = 32768
		}
		found := false
		for _, e := range bus.Recent(s.ID) {
			if e.Type == events.BudgetEvent {
				if b, ok := e.Data.(events.Budget); ok && b.NCtx == want {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("no budget at %d", want)
		}
	}
}

func TestLargerReplyDoesNotEnlargeThinking2yf(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, "invalid", 400)
			return
		}
		if body["reasoning_budget_tokens"] != float64(5120) || body["max_tokens"].(float64) <= 50000 {
			t.Errorf("allowances=%v", body)
		}
		writeStreamChunk(t, w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "answer"}, "finish_reason": "stop"}}})
	})
	runner, s, _, _ := reasoningBudgetRunner2xa(t, handler, func(c *config.Connection) {
		c.Context.NCtx = 64000
		c.Context.ReserveOutput = 8192
		c.Capabilities.Findings = []string{"server reasoning budget: accepted; key: reasoning_budget_tokens"}
	})
	if reason, detail, _ := runner.Run(context.Background(), s, "fixture"); reason != "done" {
		t.Fatalf("%s: %s", reason, detail)
	}
}

func TestRejectedBudgetUsesOneBoundedThinkingPassAndOneAnswerPass2xa(t *testing.T) {
	firstReasoning := strings.Repeat("thought ", 200)
	var requestIndex atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body struct {
			MaxTokens          int            `json:"max_tokens"`
			Messages           []llm.Message  `json:"messages"`
			ChatTemplateKwargs map[string]any `json:"chat_template_kwargs"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if requestIndex.Add(1) == 1 {
			if body.MaxTokens != 5120 || body.ChatTemplateKwargs["enable_thinking"] != true {
				t.Fatalf("first request=%+v", body)
			}
			writeStreamChunk(t, w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning_content": firstReasoning}, "finish_reason": "length"}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5120}})
			return
		}
		if body.MaxTokens <= 10240 || body.MaxTokens > 32768 || body.ChatTemplateKwargs["enable_thinking"] != false {
			t.Fatalf("answer request=%+v", body)
		}
		encoded, _ := json.Marshal(body.Messages)
		if !strings.Contains(string(encoded), strings.TrimSpace(firstReasoning)) {
			t.Fatalf("answer request did not retain prior thought: %s", encoded)
		}
		writeStreamChunk(t, w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "final answer"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 3}})
	})
	runner, item, bus, calls := reasoningBudgetRunner2xa(t, handler)
	reason, detail, turns := runner.Run(context.Background(), item, "r1")
	if reason != "done" || detail != "" || turns != 1 || calls.Load() != 2 {
		t.Fatalf("run=(%q,%q,%d) calls=%d", reason, detail, turns, calls.Load())
	}
	messages := item.MessagesCopy()
	reply := messages[len(messages)-1]
	if reply.Content != "final answer" || strings.Contains(reply.Content, "answer taken from") || strings.Contains(reply.Content, "cut short") || reply.Reasoning != strings.TrimSpace(firstReasoning) {
		t.Fatalf("reply=%+v", reply)
	}
	requests, responseTurns := 0, []int{}
	for _, event := range bus.Recent(item.ID) {
		if event.Type == events.ModelRequest {
			requests++
			data, _ := event.Data.(map[string]any)
			responseTurns = append(responseTurns, int(data["turn"].(int)))
		}
	}
	if requests != 2 || len(responseTurns) != 2 || responseTurns[0] != 1 || responseTurns[1] != 1 {
		t.Fatalf("requests=%d turns=%v", requests, responseTurns)
	}
}

func TestRejectedBudgetDoesNotUseAnswerPassWhenTheFirstPassAnswers2xa(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body struct {
			MaxTokens          int            `json:"max_tokens"`
			ChatTemplateKwargs map[string]any `json:"chat_template_kwargs"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.MaxTokens != 5120 || body.ChatTemplateKwargs["enable_thinking"] != true {
			t.Fatalf("request=%+v", body)
		}
		writeStreamChunk(t, w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning_content": "brief thought", "content": "answer in one pass"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 8}})
	})
	runner, item, _, calls := reasoningBudgetRunner2xa(t, handler)
	reason, detail, turns := runner.Run(context.Background(), item, "r1")
	if reason != "done" || detail != "" || turns != 1 || calls.Load() != 1 {
		t.Fatalf("run=(%q,%q,%d) calls=%d", reason, detail, turns, calls.Load())
	}
}
