package probe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/llm"
)

func TestProbeReasoningClassifiesEmissionShape(t *testing.T) {
	for _, shape := range []string{"structured", "inline", "none"} {
		t.Run(shape, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				message := map[string]any{"role": "assistant", "content": "391"}
				disabled := false
				if kwargs, ok := body["chat_template_kwargs"].(map[string]any); ok && kwargs["enable_thinking"] == false {
					disabled = true
				}
				if !disabled {
					switch shape {
					case "structured":
						message["reasoning_content"] = "17 times 23 is 391"
					case "inline":
						message["content"] = "<think>17 times 23 is 391</think>\n391"
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message}}})
			}))
			defer server.Close()

			connection := config.Defaults(t.TempDir()).Connections[0]
			connection.BaseURL = server.URL
			connection.Model = "reasoning-fixture"
			caps := config.Capabilities{ReasoningControl: "none"}
			findings := []string{}
			probeReasoning(context.Background(), llm.New(&connection), &connection, &caps, &findings)
			if caps.ReasoningEmission != shape {
				t.Fatalf("emission=%q findings=%v", caps.ReasoningEmission, findings)
			}
			joined := strings.Join(findings, "\n")
			if !strings.Contains(joined, "reasoning emission: "+shape) {
				t.Fatalf("findings=%v", findings)
			}
			if shape == "inline" && !strings.Contains(joined, "--reasoning-format deepseek") {
				t.Fatalf("inline finding has no server fix: %v", findings)
			}
		})
	}
}

func TestProbeReasoningBudgetRequiresAKeyThatActuallyBoundsThinking2xa(t *testing.T) {
	for _, test := range []struct {
		name, server, key, want string
		honors                  bool
	}{
		{name: "llama accepted", server: "llama.cpp", key: "reasoning_budget_tokens", honors: true, want: "accepted"},
		{name: "llama ignored", server: "llama.cpp", key: "reasoning_budget_tokens", honors: false, want: "rejected"},
		{name: "vllm accepted", server: "vllm", key: "thinking_token_budget", honors: true, want: "accepted"},
		{name: "ollama has no numeric budget", server: "ollama", key: "think", honors: false, want: "rejected"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				words := 4
				if _, probing := body[test.key]; probing {
					words = 80
					if test.honors {
						words = 32
					}
				}
				message := map[string]any{"role": "assistant", "content": "answer", "reasoning_content": strings.Repeat("step ", words)}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"choices": []any{map[string]any{"message": message, "finish_reason": "stop"}},
					"usage":   map[string]any{"completion_tokens": words + 1},
				})
			}))
			defer server.Close()

			connection := config.Defaults(t.TempDir()).Connections[0]
			connection.BaseURL, connection.Model = server.URL, "reasoning-fixture"
			caps := config.Capabilities{Server: test.server, ReasoningControl: "top_level"}
			findings := []string{}
			probeReasoning(context.Background(), llm.New(&connection), &connection, &caps, &findings)
			want := "server reasoning budget: " + test.want + "; key: " + test.key
			if !strings.Contains(strings.Join(findings, "\n"), want) {
				t.Fatalf("findings=%v, want %q", findings, want)
			}
		})
	}
}
