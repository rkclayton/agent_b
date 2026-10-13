package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
	toolpkg "harness/internal/tools"
)

type runPinTool string

func (p runPinTool) Name() string         { return string(p) }
func (runPinTool) Description() string    { return "pin" }
func (runPinTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (runPinTool) Call(context.Context, *session.Session, map[string]any) (string, error) {
	return "ok", nil
}

func TestUncoveredModelAnswerReturnsPinWholeRequestsAndEvents2uf(t *testing.T) {
	type answer struct {
		finish string
		text   string
		tool   string
		name   string
	}
	tests := []struct {
		name, reason, want string
		answers            []answer
	}{
		{name: "malformed twice", reason: "malformed_turn", answers: []answer{{finish: "tool_calls"}, {finish: "tool_calls"}}, want: "4650366533027185612cbe90b6d506379f2d6dd1a299fd1f539f20cb45ca5479"},
		{name: "truncated tool twice", reason: "length", answers: []answer{{finish: "length", tool: "c1", name: "write_file"}, {finish: "length", tool: "c2", name: "write_file"}}, want: "37cc7bbf3378293cbbd816c7796256f5c10c4636c3cc286a03376c4f463d9bf1"},
		{name: "truncated retry changes tool", reason: "length", answers: []answer{{finish: "length", tool: "c1", name: "write_file"}, {finish: "tool_calls", tool: "c2", name: "read_file"}}, want: "84d753e273fc86571fc7c73c53db9660be2a192228b4c9c0cfaa296b3025dbe2"},
		{name: "truncated final answer", reason: "length", answers: []answer{{finish: "length", text: "partial answer"}}, want: "d95fff0fb6328e4865f9e1d77fe6cd15fb802e4f845c45c9e157b31e9a5d4b71"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var mu sync.Mutex
			requests := []any{}
			index := 0
			runner, item, closeServer := backstopFixture(t, func(w http.ResponseWriter, request *http.Request) {
				var body any
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				mu.Lock()
				requests = append(requests, body)
				answer := test.answers[min(index, len(test.answers)-1)]
				index++
				mu.Unlock()
				delta := map[string]any{"content": answer.text}
				if answer.tool != "" {
					delta["tool_calls"] = []any{map[string]any{"index": 0, "id": answer.tool, "type": "function", "function": map[string]any{"name": answer.name, "arguments": `{}`}}}
				}
				writeStreamChunk(t, w, map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": answer.finish}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 1}})
			}, func(cfg *config.Config) {})
			defer closeServer()
			runner.tools = toolpkg.New(runPinTool("write_file"), runPinTool("read_file"))
			item.ToolsEnabled["read_file"] = true
			journal := []events.Event{}
			bus := events.NewBus()
			bus.SetSink(func(event events.Event) error {
				event.TS, event.Seq = "", 0
				event.Data = stableRunPinValue(event.Data)
				event.Body = stableRunPinValue(event.Body)
				journal = append(journal, event)
				return nil
			})
			runner.bus = bus
			reason, _, _ := runner.Run(context.Background(), item, "pin-run")
			if reason != test.reason {
				t.Fatalf("reason=%q, want %q", reason, test.reason)
			}
			encoded, err := json.Marshal(struct {
				Requests []any          `json:"requests"`
				Events   []events.Event `json:"events"`
			}{requests, journal})
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(encoded)
			got := hex.EncodeToString(digest[:])
			if got != test.want {
				t.Fatalf("whole requests and events digest=%s want=%s", got, test.want)
			}
		})
	}
}

func stableRunPinValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if key == "duration_ms" || key == "ms" {
				continue
			}
			if key == "host" {
				out[key] = "stand-in"
				continue
			}
			out[key] = stableRunPinValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = stableRunPinValue(item)
		}
		return out
	default:
		return value
	}
}
