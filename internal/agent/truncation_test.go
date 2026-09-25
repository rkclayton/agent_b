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

type retryWriteTool struct{}

func (*retryWriteTool) Name() string        { return "write_file" }
func (*retryWriteTool) Description() string { return "write a file" }
func (*retryWriteTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}}
}
func (*retryWriteTool) Call(context.Context, *session.Session, map[string]any) (string, error) {
	return "wrote game.html", nil
}

func TestLengthDuringToolArgumentsDoesNotEnterToolHistory(t *testing.T) {
	var requests atomic.Int32
	var firstMaxTokens int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, request)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if stream, _ := body["stream"].(bool); !stream {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "Game edit"}, "finish_reason": "stop"}}})
			return
		}
		attempt := requests.Add(1)
		if attempt == 1 {
			firstMaxTokens = int(body["max_tokens"].(float64))
			delta := map[string]any{
				"content": "I chose the layout. ",
				"tool_calls": []any{map[string]any{
					"index": 0, "id": "call-1", "type": "function",
					"function": map[string]any{"name": "write_file", "arguments": `{"path":"game.html","content":"<!doctype`},
				}},
			}
			writeStreamChunk(t, w, map[string]any{
				"choices": []any{map[string]any{"delta": delta, "finish_reason": "length"}},
				"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": firstMaxTokens},
			})
			return
		}
		if attempt == 2 {
			if body["tool_choice"] != "auto" {
				t.Fatalf("retry relied on named tool_choice=%#v", body["tool_choice"])
			}
			messages, _ := body["messages"].([]any)
			encoded, _ := json.Marshal(messages)
			if !strings.Contains(string(encoded), "previous write_file call was truncated") {
				t.Fatalf("retry has no text re-ask: %s", encoded)
			}
			writeStreamChunk(t, w, map[string]any{
				"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{
					"index": 0, "id": "call-2", "type": "function", "function": map[string]any{"name": "write_file", "arguments": `{"path":"game.html","content":"done"}`},
				}}}, "finish_reason": "tool_calls"}},
				"usage": map[string]any{"prompt_tokens": 120, "completion_tokens": 20},
			})
			return
		}
		writeStreamChunk(t, w, map[string]any{
			"choices": []any{map[string]any{"delta": map[string]any{"content": "Done without retrying the call."}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 120, "completion_tokens": 10},
		})
	}))
	defer server.Close()

	runner, item, bus := truncationRunner(t, server.URL, "estimated")
	reason, detail, turns := runner.Run(context.Background(), item, "r1")
	if reason != "done" || detail != "" || turns != 3 {
		t.Fatalf("run=(%q,%q,%d)", reason, detail, turns)
	}
	if firstMaxTokens <= 0 {
		t.Fatalf("max_tokens=%d", firstMaxTokens)
	}
	messages := item.MessagesCopy()
	if len(messages) != 3 || messages[0].Role != "assistant" || len(messages[0].ToolCalls) != 1 || messages[0].ToolCalls[0].Name != "write_file" || messages[1].Role != "tool" || messages[2].Content != "Done without retrying the call." {
		t.Fatalf("messages=%#v", messages)
	}
	for _, message := range messages {
		if message.Role == "user" {
			t.Fatalf("harness wrote user message: %#v", message)
		}
	}
	retries, calls, results := 0, 0, 0
	for _, event := range bus.Recent(item.ID) {
		if event.Type == events.ModelRetry {
			retries++
		}
		if event.Type == events.ToolCallEvent {
			calls++
		}
		if event.Type == events.ToolResult {
			results++
		}
	}
	if retries != 1 || calls != 1 || results != 1 {
		t.Fatalf("retries=%d calls=%d results=%d", retries, calls, results)
	}
}

func TestToolCallParserGuards2k5(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if retry, stop := malformedToolTurnAction("tool_calls", 0, false); !retry || stop {
			t.Fatalf("empty first turn retry=%t stop=%t", retry, stop)
		}
		if retry, stop := malformedToolTurnAction("tool_calls", 0, true); retry || !stop {
			t.Fatalf("empty second turn retry=%t stop=%t", retry, stop)
		}
	})
	offered := map[string]bool{"read_file": true}
	t.Run("unknown", func(t *testing.T) {
		unknown := []events.ToolCall{{ID: "u", Name: "shell", Arguments: `{}`}}
		if calls, lines := guardModelToolCalls(unknown, offered); len(calls) != 0 || len(lines) != 1 || !strings.Contains(lines[0], "not offered") {
			t.Fatalf("unknown calls=%+v lines=%q", calls, lines)
		}
	})
	t.Run("duplicate", func(t *testing.T) {
		duplicates := []events.ToolCall{{ID: "a", Name: "read_file", Arguments: `{"path":"x"}`}, {ID: "b", Name: "read_file", Arguments: `{"path":"x"}`}}
		if calls, lines := guardModelToolCalls(duplicates, offered); len(calls) != 1 || len(lines) != 1 || !strings.Contains(lines[0], "duplicate") {
			t.Fatalf("duplicate calls=%+v lines=%q", calls, lines)
		}
	})
}

func TestRunPublishesCurrentToolAndCoversBetweenTurnAccounting(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, request)
			return
		}
		if requests.Add(1) == 1 {
			writeStreamChunk(t, w, map[string]any{
				"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
					map[string]any{"index": 0, "id": "first", "type": "function", "function": map[string]any{"name": "write_file", "arguments": `{"path":"first.txt","content":"first"}`}},
					map[string]any{"index": 1, "id": "second", "type": "function", "function": map[string]any{"name": "write_file", "arguments": `{"path":"second.txt","content":"second"}`}},
				}}, "finish_reason": "tool_calls"}},
				"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 20},
			})
			return
		}
		writeStreamChunk(t, w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "done"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 1}})
	}))
	defer server.Close()

	runner, item, bus := truncationRunner(t, server.URL, "estimated")
	reason, detail, turns := runner.Run(context.Background(), item, "r-current-tool")
	if reason != "done" || detail != "" || turns != 2 {
		t.Fatalf("run=(%q,%q,%d)", reason, detail, turns)
	}
	eventsSeen := bus.Recent(item.ID)
	sequence := make([]string, 0, len(eventsSeen))
	for _, event := range eventsSeen {
		data, _ := event.Data.(map[string]any)
		switch event.Type {
		case events.Stage:
			sequence = append(sequence, fmt.Sprintf("stage:%s:%s:t%v", data["stage"], data["state"], data["turn"]))
		case events.ToolCallEvent:
			sequence = append(sequence, "call:"+fmt.Sprint(data["call_id"]))
		case events.ToolResult:
			sequence = append(sequence, "result:"+fmt.Sprint(data["call_id"]))
		}
	}
	joined := strings.Join(sequence, "|")
	for _, ordered := range []string{
		"stage:dispatch:exit:t1|stage:execute:enter:t1|call:first|result:first|call:second|result:second|stage:execute:exit:t1",
		"stage:append:exit:t1|stage:compact:enter:t1|stage:compact:exit:t1|stage:assemble:enter:t2",
		"stage:parse:exit:t2|stage:append:enter:t2|stage:append:exit:t2|stage:append:enter:t2|stage:append:exit:t2",
	} {
		if !strings.Contains(joined, ordered) {
			t.Fatalf("event sequence missing %q:\n%s", ordered, joined)
		}
	}
}

func TestMalformedHistoryRepairPreservesFailureNoteWithoutRestoringFailedAction(t *testing.T) {
	var rejected atomic.Int32
	var dispatched atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/apply-template":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(body["messages"])
			if strings.Contains(string(encoded), `call-bad`) && strings.Contains(string(encoded), `tool_calls`) {
				rejected.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"error":{"message":"Failed to parse tool call arguments as JSON: missing closing quote"}}`)
				return
			}
			data, _ := json.Marshal(map[string]string{"prompt": string(encoded)})
			_, _ = w.Write(data)
		case "/tokenize":
			fmt.Fprint(w, `{"tokens":[1,2,3,4]}`)
		case "/v1/chat/completions":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(body["messages"])
			dispatched.Store(string(encoded))
			writeStreamChunk(t, w, map[string]any{
				"choices": []any{map[string]any{"delta": map[string]any{"content": "Recovered."}, "finish_reason": "stop"}},
				"usage":   map[string]any{"prompt_tokens": 40, "completion_tokens": 2},
			})
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	runner, item, bus := truncationRunner(t, server.URL, "exact")
	item.Append(events.Message{ID: "m-bad", Role: "assistant", Content: "I chose the game design.", Category: "history", Turn: 1, ToolCalls: []events.ToolCall{{ID: "call-bad", Name: "write_file", Arguments: `{"path":"game.html","content":"<!doctype`}}})
	failed := false
	item.Append(events.Message{ID: "m-result", Role: "tool", Content: "error: invalid JSON arguments", Category: "results", Turn: 1, ToolCallID: "call-bad", Name: "write_file", OK: &failed})
	item.Append(events.Message{ID: "m-later", Role: "user", Content: "please recover", Category: "history", Turn: 0})

	reason, detail, turns := runner.Run(context.Background(), item, "r2")
	if reason != "done" || detail != "" || turns != 1 {
		t.Fatalf("run=(%q,%q,%d)", reason, detail, turns)
	}
	if rejected.Load() != 0 {
		t.Fatalf("apply-template rejects=%d, malformed history must be repaired before accounting", rejected.Load())
	}
	messages := item.MessagesCopy()
	if len(messages) != 3 || messages[0].ID != "m-bad" || len(messages[0].ToolCalls) != 0 || messages[1].ID != "m-later" {
		t.Fatalf("repaired messages=%#v", messages)
	}
	if messages[2].Content != "Recovered." {
		t.Fatalf("final=%#v", messages[2])
	}
	for _, message := range messages {
		if len(message.ToolCalls) != 0 || message.ToolCallID == "call-bad" || strings.Contains(message.Content, "invalid JSON arguments") || strings.Contains(message.Content, "<!doctype") {
			t.Fatalf("failed action was restored to transcript: %#v", message)
		}
	}
	dispatchedMessages, _ := dispatched.Load().(string)
	if dispatchedMessages == "" || strings.Contains(dispatchedMessages, "call-bad") || strings.Contains(dispatchedMessages, "<!doctype") || strings.Contains(dispatchedMessages, "cut off") {
		t.Fatalf("dispatched transcript retained malformed history: %s", dispatchedMessages)
	}
	foundBody, foundRetry := false, false
	for _, event := range bus.Recent(item.ID) {
		if event.Type == events.Error {
			data, _ := event.Data.(map[string]any)
			if data["where"] == "tool_history" {
				foundBody = true
			}
		}
		if event.Type == events.ModelRetry {
			foundRetry = true
		}
	}
	if !foundBody || !foundRetry {
		t.Fatalf("foundBody=%t foundRetry=%t", foundBody, foundRetry)
	}
}

func TestRequestTokenLimitUsesRemainingContextAfterReserve(t *testing.T) {
	connection := &config.Connection{Context: config.Context{NCtx: 32768, ReserveOutput: 10240}}
	budget := events.Budget{NCtx: 32768, Reserve: 10240, UsedEst: 8451, Mode: "exact"}
	if got := requestTokenLimit(connection, budget, guardedPromptTokens(budget)); got != 10240 {
		t.Fatalf("max_tokens=%d, want 10240", got)
	}
	budget.Mode = "estimated"
	if got := guardedPromptTokens(budget); got != 9297 {
		t.Fatalf("guarded prompt=%d, want 9297", got)
	}
	budget.UsedEst = 30000
	if got := requestTokenLimit(connection, budget, guardedPromptTokens(budget)); got != 0 {
		t.Fatalf("estimated exhausted max_tokens=%d, want 0 before compaction", got)
	}
	connection.Context.ReserveOutput = 1024
	budget.Mode, budget.UsedEst = "exact", 20000
	if got := requestTokenLimit(connection, budget, guardedPromptTokens(budget)); got != minimumOutputFloor {
		t.Fatalf("configured-low max_tokens=%d, want floor %d", got, minimumOutputFloor)
	}
}

func truncationRunner(t *testing.T, baseURL, accounting string) (*Runner, *session.Session, *capturedBus) {
	t.Helper()
	cfg := config.Defaults(t.TempDir())
	cfg.Context.Accounting = accounting
	connection := cfg.Connections[0]
	connection.ID, connection.Label, connection.BaseURL, connection.Model = "main", "main", baseURL, "test-model"
	connection.RequestTimeoutS = 5
	connection.Context.NCtx = 32768
	connection.Context.ReserveOutput = 10240
	connection.Capabilities.NCtx = 32768
	connection.Capabilities.Streaming = true
	connection.Capabilities.ToolCalls = true
	connection.Capabilities.OverflowBehavior = "error"
	connection.Capabilities.Tokenize = accounting == "exact"
	connection.Capabilities.ApplyTemplate = accounting == "exact"
	connection.Capabilities.ApplyTemplateTools = accounting == "exact"
	cfg.Connections = []config.Connection{connection}
	cfg.Agents = []config.Agent{{Name: "Main", B: "main", Toolset: config.FullToolset()}}
	bus := newCapturedBus()
	runner := NewRunner(bus.Bus, tools.New(&retryWriteTool{}), &PromptRenderer{text: "system {{workspace}} {{memory}} {{tools}}"}, cfg.Connection, func() config.Config { return cfg })
	enabled := map[string]bool{}
	for _, name := range config.FullToolset() {
		enabled[name] = true
	}
	item := &session.Session{ID: "main", ConnectionID: "main", Workspace: t.TempDir(), Run: session.RunState{Status: "running", MaxTurns: cfg.Run.MaxTurns}, Runnable: true, ToolsEnabled: enabled, ToolCalls: map[string]int{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}, Budget: events.Budget{NCtx: 32768, Reserve: 10240}}
	return runner, item, bus
}

func writeStreamChunk(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", encoded)
}
