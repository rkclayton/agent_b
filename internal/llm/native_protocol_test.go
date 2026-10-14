package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"harness/internal/config"
)

func TestNativeProtocolsStreamWholeRun2rr(t *testing.T) {
	for _, protocol := range []string{config.ProtocolAnthropicMessages, config.ProtocolOpenAIResponses} {
		t.Run(protocol, func(t *testing.T) {
			var request map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if protocol == config.ProtocolAnthropicMessages {
					if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "fixture-key" || r.Header.Get("anthropic-version") == "" || r.Header.Get("Authorization") != "" {
						t.Errorf("anthropic request/header mismatch")
					}
				} else if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer fixture-key" || r.Header.Get("x-api-key") != "" {
					t.Errorf("responses request/header mismatch")
				}
				_ = json.NewDecoder(r.Body).Decode(&request)
				w.Header().Set("Content-Type", "text/event-stream")
				if protocol == config.ProtocolAnthropicMessages {
					for _, data := range []string{
						`{"type":"message_start","message":{"usage":{"input_tokens":40,"cache_read_input_tokens":80}}}`,
						`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
						`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"considering"}}`,
						`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signed"}}`,
						`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
						`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"hello"}}`,
						`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"a","name":"read_file","input":{}}}`,
						`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a\"}"}}`,
						`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"b","name":"list_dir","input":{}}}`,
						`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
						`{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":25}}`,
					} {
						fmt.Fprintf(w, "data: %s\n\n", data)
					}
				} else {
					for _, data := range []string{
						`{"type":"response.reasoning_summary_text.delta","delta":"considering"}`,
						`{"type":"response.output_text.delta","delta":"hello"}`,
						`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"a","name":"read_file","arguments":""}}`,
						`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"path\":\"a\"}"}`,
						`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","call_id":"b","name":"list_dir","arguments":""}}`,
						`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{}"}`,
						`{"type":"response.completed","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":120,"output_tokens":25,"input_tokens_details":{"cached_tokens":80},"output_tokens_details":{"reasoning_tokens":7}},"output":[{"type":"reasoning","id":"rs_1","encrypted_content":"opaque"}]}}`,
					} {
						fmt.Fprintf(w, "data: %s\n\n", data)
					}
				}
			}))
			defer server.Close()
			connection := &config.Connection{BaseURL: server.URL, APIKey: "fixture-key", Model: "fixture-model", Protocol: protocol, RequestTimeoutS: 5}
			response, err := New(connection).ChatStream(context.Background(), Request{MaxTokens: 32, Thinking: true, Messages: []Message{{Role: "user", Content: []any{map[string]any{"type": "text", "text": "look"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AA=="}}}}}, Tools: []any{map[string]any{"type": "function", "function": map[string]any{"name": "read_file", "description": "read", "parameters": map[string]any{"type": "object"}}}}}, func(Delta) {})
			if err != nil {
				t.Fatal(err)
			}
			if response.Content != "hello" || !strings.Contains(response.Reasoning, "considering") || len(response.ToolCalls) != 2 || response.FinishReason != "length" {
				t.Fatalf("native response mismatch: %+v", response)
			}
			if response.Usage.PromptTokens != 120 || response.Usage.CompletionTokens != 25 || response.Usage.CachedTokens != 80 {
				t.Fatalf("usage mismatch: %+v", response.Usage)
			}
			encoded, _ := json.Marshal(request)
			if !strings.Contains(string(encoded), "AA==") {
				t.Fatalf("image missing: %s", encoded)
			}
			_, carried := nativeRequest(connection, Request{Messages: []Message{{Role: "assistant", Content: "hello", ReasoningContent: response.Reasoning, ToolCalls: response.ToolCalls}, {Role: "tool", ToolCallID: "a", Content: "result"}}})
			carriedJSON, _ := json.Marshal(carried)
			if protocol == config.ProtocolAnthropicMessages && !strings.Contains(string(carriedJSON), `"signature":"signed"`) {
				t.Fatalf("thinking signature not carried: %s", carriedJSON)
			}
			if protocol == config.ProtocolOpenAIResponses && !strings.Contains(string(carriedJSON), `"encrypted_content":"opaque"`) {
				t.Fatalf("reasoning item not carried: %s", carriedJSON)
			}
			second, err := New(connection).ChatStream(context.Background(), Request{MaxTokens: 2048, Thinking: true, Messages: []Message{{Role: "assistant", Content: response.Content, ReasoningContent: response.Reasoning, ToolCalls: response.ToolCalls}, {Role: "tool", ToolCallID: "a", Content: "result"}}}, func(Delta) {})
			if err != nil {
				t.Fatal(err)
			}
			if second.Usage.CachedTokens != 80 {
				t.Fatalf("second-turn cached usage mismatch: %+v", second.Usage)
			}
		})
	}
}

func TestProtocolDetection2rr(t *testing.T) {
	for address, want := range map[string]string{"https://api.anthropic.com": config.ProtocolAnthropicMessages, "https://api.openai.com/v1": config.ProtocolOpenAIResponses, "https://gateway.example.test/v1": config.ProtocolChatCompletions} {
		if got := config.ProtocolForAddress(address); got != want {
			t.Errorf("%s: got %s want %s", address, got, want)
		}
	}
}

func TestNativeProtocolErrorsAndKeyHost2rr(t *testing.T) {
	for _, protocol := range []string{config.ProtocolAnthropicMessages, config.ProtocolOpenAIResponses} {
		for _, status := range []int{http.StatusUnauthorized, 529} {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "fixture refusal", status) }))
			connection := &config.Connection{BaseURL: server.URL, Protocol: protocol, RequestTimeoutS: 2}
			_, err := New(connection).Chat(context.Background(), Request{})
			server.Close()
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", status)) {
				t.Errorf("%s status %d: %v", protocol, status, err)
			}
		}
		connection := &config.Connection{BaseURL: "http://127.0.0.1:1", Protocol: protocol, RequestTimeoutS: 1}
		if _, err := New(connection).Chat(context.Background(), Request{}); err == nil {
			t.Errorf("%s unreachable host returned no error", protocol)
		}
	}
	var reached bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer other.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	connection := &config.Connection{BaseURL: first.URL, Protocol: config.ProtocolOpenAIResponses, APIKey: "fixture-key", RequestTimeoutS: 2}
	_, _ = New(connection).Chat(context.Background(), Request{})
	if reached {
		t.Fatal("credentialed request followed a redirect to another host")
	}
}
