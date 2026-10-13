package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
)

func TestEverySizeRefusalUsesOneCompactionAndOneRetry2up(t *testing.T) {
	for _, refusal := range []string{
		"prompt too large: 12000 bytes (limit 10000)",
		"conversation too long: 61 messages (limit 60)",
		"request exceeds the available context size (16000 tokens)",
		"request exceeds context window",
	} {
		for _, twice := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/twice=%t", refusal, twice), func(t *testing.T) {
				var streamCalls, summaryCalls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
					var body struct {
						Stream bool `json:"stream"`
					}
					_ = json.NewDecoder(request.Body).Decode(&body)
					if !body.Stream {
						summaryCalls.Add(1)
						_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "DONE: old\nNEXT: continue\nFILES CHANGED: none\nOPEN QUESTIONS: none"}, "finish_reason": "stop"}}})
						return
					}
					call := streamCalls.Add(1)
					if call == 1 || twice {
						http.Error(w, `{"error":{"message":"`+refusal+`"}}`, http.StatusBadRequest)
						return
					}
					writeStreamChunk(t, w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "done"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 500, "completion_tokens": 1}})
				}))
				defer server.Close()

				root := t.TempDir()
				cfg := config.Defaults(root)
				cfg.Context.Accounting = "estimated"
				connection := cfg.Connections[0]
				connection.ID, connection.BaseURL, connection.Model = "fixture", server.URL, "fixture"
				connection.Context.NCtx, connection.Context.ReserveOutput = 32768, 4096
				cfg.Connections = []config.Connection{connection}
				bus := newCapturedBus()
				runner := NewRunner(bus.Bus, tools.New(), &PromptRenderer{text: "system"}, func(string) (*config.Connection, bool) { return &connection, true }, func() config.Config { return cfg })
				item := &session.Session{ID: "chat", ConnectionID: connection.ID, Workspace: root, Runnable: true, ToolsEnabled: map[string]bool{}, ToolCalls: map[string]int{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
				for index := 0; index < 8; index++ {
					item.Append(events.Message{ID: fmt.Sprintf("u-%d", index), Role: "user", Category: "history", Content: "old task", Tokens: 100})
					item.Append(events.Message{ID: fmt.Sprintf("a-%d", index), Role: "assistant", Category: "history", Content: strings.Repeat("old work ", 100), Tokens: 300})
				}
				item.Append(events.Message{ID: "task", Role: "user", Category: "history", Content: "CURRENT TASK WORD FOR WORD", Tokens: 10})

				reason, detail, _ := runner.Run(t.Context(), item, "run")
				compactions := 0
				for _, event := range bus.Recent(item.ID) {
					if event.Type == events.Compaction {
						compactions++
					}
				}
				if streamCalls.Load() != 2 || summaryCalls.Load() != 1 || compactions != 1 {
					t.Fatalf("stream=%d summary=%d compactions=%d reason=%s detail=%q", streamCalls.Load(), summaryCalls.Load(), compactions, reason, detail)
				}
				if twice {
					if reason != "model_error" || !strings.Contains(detail, refusal) {
						t.Fatalf("second refusal=%s %q", reason, detail)
					}
				} else if reason != "done" {
					t.Fatalf("retry=%s %q", reason, detail)
				}
			})
		}
	}
}

func TestEveryRunStartPinsItsUserMessage2uo(t *testing.T) {
	starts := []string{"message", "after failure", "Continue", "Revise plan", "queued", "phone", "worker", "helper", "scheduled"}
	for _, start := range starts {
		messages := []events.Message{{ID: "old", Role: "assistant"}, {ID: "task", Role: "user"}}
		if start == "queued" {
			messages = append(messages, events.Message{ID: "queued", Role: "user"})
		}
		if start == "after failure" || start == "queued" {
			messages = append(messages, events.Message{ID: "status", Role: "harness"})
		}
		if pin := session.RunPinFromTail(messages); pin != "task" {
			t.Errorf("%s pin=%q", start, pin)
		}
	}
	if pin := session.RunPinFromTail([]events.Message{{ID: "brief", Role: "harness"}}); pin != "brief" {
		t.Errorf("no-user pin=%q", pin)
	}
}

func TestFailedRunShapeCompactsBeforeItsFirstRequest2uo(t *testing.T) {
	summaries := 0
	server := newTemplateServer(t, func(body map[string]any, _ []map[string]any) map[string]any {
		if stream, _ := body["stream"].(bool); !stream {
			summaries++
			return map[string]any{"content": "DONE: earlier work\nNEXT: answer\nFILES CHANGED: none\nOPEN QUESTIONS: none"}
		}
		return map[string]any{"content": "carried on"}
	})
	runner, item, bus := templateRunnerReserve(t, server, 8192)
	for turn := 1; turn <= 10; turn++ {
		item.Append(events.Message{ID: fmt.Sprintf("u-%d", turn), Role: "user", Category: "history", Turn: turn, Content: "earlier request"})
		item.Append(events.Message{ID: fmt.Sprintf("a-%d", turn), Role: "assistant", Category: "history", Turn: turn, Content: strings.Repeat("earlier work ", 900)})
	}
	item.Append(events.Message{ID: "task", Role: "user", Category: "history", Turn: 11, Content: "carry on"})
	item.Append(events.Message{ID: "status", Role: "harness", Category: "history", Turn: 11, Content: "The prior run ended with model error: refused."})
	if reason, detail, _ := runner.Run(context.Background(), item, "run"); reason != "done" {
		t.Fatalf("run=%s %q", reason, detail)
	}
	attempts, budgets := summaryAttempts(bus, item.ID), []events.Budget{}
	for _, event := range bus.Recent(item.ID) {
		if event.Type == events.BudgetEvent {
			budgets = append(budgets, event.Data.(events.Budget))
		}
	}
	if summaries != 1 || len(attempts) != 1 || attempts[0].Outcome != "accepted" {
		t.Fatalf("summaries=%d attempts=%+v", summaries, attempts)
	}
	if len(budgets) < 2 || budgets[len(budgets)-1].UsedEst > budgets[len(budgets)-1].Ceiling/2 {
		t.Fatalf("post-compaction budget=%+v", budgets)
	}
}

func TestGuardUsesMeasuredAnchor2uo(t *testing.T) {
	if got := guardedPromptTokens(events.Budget{UsedEst: 9000, UsedMeasured: 8800, Mode: "estimated"}); got != 9000 {
		t.Fatalf("anchored=%d", got)
	}
	if got := guardedPromptTokens(events.Budget{UsedEst: 9000, Mode: "estimated"}); got != 9900 {
		t.Fatalf("unanchored=%d", got)
	}
}
