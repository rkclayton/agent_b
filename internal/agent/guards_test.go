package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
	toolpkg "harness/internal/tools"
)

func observeRepeat(t *testing.T, guard *runGuards, count int) string {
	t.Helper()
	reason := ""
	for index := 1; index <= count; index++ {
		reason, _, _ = guard.Observe(string(rune('a'+index)), "read_file", map[string]any{"path": "same"}, "same result", false)
	}
	return reason
}

func TestRepeatGuardAsksAtConfiguredConsecutiveCount2tc(t *testing.T) {
	guard := newRunGuards(5)
	if reason := observeRepeat(t, guard, 4); reason != "" {
		t.Fatalf("four repeats reason=%q", reason)
	}
	if reason := observeRepeat(t, guard, 1); reason != "cycle" {
		t.Fatalf("fifth repeat reason=%q", reason)
	}

	guard = newRunGuards(5)
	for index := 0; index < 3; index++ {
		guard.Observe("same", "read_file", map[string]any{"path": "same"}, "same result", false)
		if reason, _, _ := guard.Observe("other", "read_file", map[string]any{"path": "other"}, "other result", false); reason != "" {
			t.Fatalf("interleaved call reason=%q", reason)
		}
	}
	if reason, _, _ := guard.Observe("last", "read_file", map[string]any{"path": "same"}, "same result", false); reason != "" {
		t.Fatalf("non-consecutive repeats reason=%q", reason)
	}

	if reason := observeRepeat(t, newRunGuards(8), 8); reason != "cycle" {
		t.Fatalf("eighth repeat reason=%q", reason)
	}
	if reason := observeRepeat(t, newRunGuards(0), 12); reason != "" {
		t.Fatalf("disabled repeat guard reason=%q", reason)
	}
}

func TestToolFailuresNeverStopTheRun2tc(t *testing.T) {
	guard := newRunGuards(0)
	for index := 0; index < 12; index++ {
		if reason, _, _ := guard.Observe("failure", "tool", map[string]any{"index": index}, "failed", false); reason != "" {
			t.Fatalf("failure %d stopped run: %q", index+1, reason)
		}
	}
}

type failureThenSuccessTool2tc struct{}

func (failureThenSuccessTool2tc) Name() string        { return "failure_fixture" }
func (failureThenSuccessTool2tc) Description() string { return "fixture" }
func (failureThenSuccessTool2tc) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"index": map[string]any{"type": "integer"}}}
}
func (failureThenSuccessTool2tc) Call(_ context.Context, _ *session.Session, args map[string]any) (string, error) {
	if args["index"].(float64) <= 12 {
		return "", errors.New("planned fixture failure")
	}
	return "fixture success", nil
}

func TestTwelveFailuresAcrossAndWithinTurnsStillEndDone2tc(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		results := 0
		for _, message := range body.Messages {
			if message.Role == "tool" {
				results++
			}
		}
		delta, finish := map[string]any{}, "tool_calls"
		if results < 12 {
			calls := []map[string]any{}
			for index := results + 1; index <= results+3; index++ {
				calls = append(calls, map[string]any{"index": index - results - 1, "id": fmt.Sprintf("failure-%d", index), "type": "function", "function": map[string]any{"name": "failure_fixture", "arguments": fmt.Sprintf(`{"index":%d}`, index)}})
			}
			delta["tool_calls"] = calls
		} else if results == 12 {
			delta["tool_calls"] = []map[string]any{{"index": 0, "id": "success-13", "type": "function", "function": map[string]any{"name": "failure_fixture", "arguments": `{"index":13}`}}}
		} else {
			delta["content"], finish = "finished after failures", "stop"
		}
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 1}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer model.Close()

	cfg := config.Defaults(t.TempDir())
	cfg.Context.Accounting = "estimated"
	connection := cfg.Connections[0]
	connection.ID, connection.BaseURL = "main", model.URL
	connection.Context.NCtx, connection.Context.ReserveOutput = 32768, 8192
	connection.Capabilities.Streaming, connection.Capabilities.ToolCalls = true, true
	bus := events.NewBus()
	runner := NewRunner(bus, toolpkg.New(failureThenSuccessTool2tc{}), &PromptRenderer{text: "system"}, func(id string) (*config.Connection, bool) { return &connection, id == connection.ID }, func() config.Config { return cfg })
	item := &session.Session{ID: "failure-chain", ConnectionID: connection.ID, Workspace: t.TempDir(), Runnable: true, Run: session.RunState{Status: "running", MaxTurns: 8}, ToolsEnabled: map[string]bool{"failure_fixture": true}, ToolCalls: map[string]int{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	if _, err := runner.AddUser(context.Background(), item, "keep going after failures"); err != nil {
		t.Fatal(err)
	}
	reason, detail, _ := runner.Run(context.Background(), item, "run")
	if reason != "done" || item.ToolCalls["failure_fixture"] != 13 {
		t.Fatalf("reason=%q detail=%q calls=%d", reason, detail, item.ToolCalls["failure_fixture"])
	}
}
