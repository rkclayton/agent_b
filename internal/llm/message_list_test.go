package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"harness/internal/config"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestHistoricalMessageListFixtures(t *testing.T) {
	fixtures := []struct {
		name    string
		history []Message
	}{
		{"2cw-compaction-then-continue", []Message{{Role: RoleUser, Content: "before"}, {Role: RoleHarness, Content: "summary"}, {Role: RoleUser, Content: "continue"}}},
		{"2df-stop-then-continue", []Message{{Role: RoleSystem, Content: "legacy abort"}, {Role: RoleUser, Content: "continue"}}},
		{"2dg-restore-then-continue", []Message{{Role: RoleHarness, Content: "restored note"}, {Role: RoleUser, Content: "continue"}}},
		{"2ir-unavailable-identity-then-continue", []Message{{Role: RoleUser, Content: "request"}, {Role: RoleHarness, Content: "identity unavailable"}, {Role: RoleUser, Content: "continue"}}},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			messages, err := BuildMessageList("system", fixture.history)
			if err != nil {
				t.Fatal(err)
			}
			if messages[0].Role != RoleSystem {
				t.Fatalf("first role=%q", messages[0].Role)
			}
			for index, message := range messages[1:] {
				if message.Role == RoleSystem || message.Role == RoleHarness {
					t.Fatalf("fixture leaked role at %d: %+v", index+1, message)
				}
			}
		})
	}
}

func TestMessageListPreservesMultipleToolCallsAndResults(t *testing.T) {
	calls := []ToolCall{{ID: "a", Type: "function", Function: FunctionCall{Name: "one", Arguments: "{}"}}, {ID: "b", Type: "function", Function: FunctionCall{Name: "two", Arguments: "{\"x\":2}"}}}
	history := []Message{{Role: RoleUser, Content: "go"}, {Role: RoleAssistant, ToolCalls: calls}, {Role: RoleTool, ToolCallID: "a", Content: "one"}, {Role: RoleTool, ToolCallID: "b", Content: "two"}}
	messages, err := BuildMessageList("system", history)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(messages[2].ToolCalls, calls) || messages[3].ToolCallID != "a" || messages[4].ToolCallID != "b" {
		t.Fatalf("tool order changed: %+v", messages)
	}
}

func TestMessageListRefusalNamesRuleAndIndex(t *testing.T) {
	err := ValidateMessageList([]Message{{Role: RoleSystem, Content: "system"}, {Role: RoleUser, Content: "go"}, {Role: RoleTool, ToolCallID: "missing", Content: "result"}})
	if err == nil || !strings.Contains(err.Error(), "tool-result-after-call") || !strings.Contains(err.Error(), "index 2") {
		t.Fatalf("refusal=%v", err)
	}
}

func TestHarnessNoteTailReplays2yh(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Messages []Message }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		last := req.Messages[len(req.Messages)-1]
		answer := "ANSWER: " + fmt.Sprint(last.Content)
		if last.Role == RoleAssistant {
			answer = fmt.Sprint(last.Content) + " continued"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": answer}, "finish_reason": "stop"}}})
	}))
	defer model.Close()
	client := New(&config.Connection{BaseURL: model.URL, Model: "fixture", RequestTimeoutS: 5})
	cases := []struct {
		name    string
		history []Message
		want    string
	}{
		{"cutoff replay", []Message{{Role: RoleAssistant, Content: "earlier answer"}, {Role: RoleUser, Content: "next task"}, {Role: RoleHarness, Content: "The prior run ended with length: Settings reserve C:/Data detail"}}, "The prior run was cut off for length."},
		{"cancel replay", []Message{{Role: RoleHarness, Content: "[HARNESS ABORT RECORD] synthetic partial output"}, {Role: RoleUser, Content: "next task"}, {Role: RoleHarness, Content: "The prior run ended because you stopped it."}}, "The prior run was stopped by you."},
		{"cancel wire tail", []Message{{Role: RoleUser, Content: "earlier task"}, {Role: RoleUser, Content: "next task"}, {Role: RoleHarness, Content: "The prior run ended because you stopped it."}}, "The prior run was stopped by you."},
		{"unreachable", []Message{{Role: RoleUser, Content: "next task"}, {Role: RoleHarness, Content: "The prior run ended because the model server example.org could not be reached: detail"}}, "The prior run ended because the server was unreachable."},
		{"abort tail", []Message{{Role: RoleUser, Content: "next task"}, {Role: RoleHarness, Content: "[HARNESS ABORT RECORD] synthetic partial output"}}, ""},
		{"legacy system", []Message{{Role: RoleUser, Content: "next task"}, {Role: RoleSystem, Content: "legacy note"}}, ""},
		{"retry", []Message{{Role: RoleUser, Content: "next task"}, {Role: RoleHarness, Content: "The previous response ended as tool_calls but contained no calls."}}, ""},
		{"no user", []Message{{Role: RoleHarness, Content: "recorded context"}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := fmt.Sprint(tc.history)
			messages, err := BuildMessageList("system", tc.history)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Chat(context.Background(), Request{Messages: messages})
			if err != nil {
				t.Fatal(err)
			}
			wantAnswer := "ANSWER: next task"
			if tc.name == "no user" {
				wantAnswer = "ANSWER: [harness note]\nContinue from the recorded context."
			}
			if response.Content != wantAnswer {
				t.Errorf("stand-in continued the trailing note: %q roles=%v", response.Content, func() []string {
					var roles []string
					for _, m := range messages {
						roles = append(roles, m.Role)
					}
					return roles
				}())
			}
			if tc.want != "" {
				found := false
				for _, m := range messages[:len(messages)-1] {
					if m.Content == harnessNotePrefix+tc.want {
						found = true
					}
				}
				if !found {
					t.Errorf("missing exact plain note %q", tc.want)
				}
			}
			if fmt.Sprint(tc.history) != before {
				t.Fatal("durable input changed")
			}
		})
	}
}

func TestPreparedHistoryKeepsAccountingIdentity2yh(t *testing.T) {
	history := []Message{{Role: RoleUser, Content: "old"}, {Role: RoleAssistant, Content: "reply"}, {Role: RoleUser, Content: "new"}, {Role: RoleHarness, Content: "note"}}
	messages, order := PrepareHistory(history)
	if !reflect.DeepEqual(order, []int{0, 1, 3, 2}) || messages[len(messages)-1].Content != "new" {
		t.Fatal(order, messages)
	}
	genuine := []Message{{Role: RoleUser, Content: "ask"}, {Role: RoleAssistant, Content: "[harness note] genuine model text"}}
	messages, order = PrepareHistory(genuine)
	if !reflect.DeepEqual(messages, genuine) || !reflect.DeepEqual(order, []int{0, 1}) {
		t.Fatal("a real reply was reclassified")
	}
}

func TestEveryWireAdapterOrdersLegacyNotes2yh(t *testing.T) {
	for _, protocol := range []string{config.ProtocolChatCompletions, config.ProtocolAnthropicMessages, config.ProtocolOpenAIResponses} {
		connection := &config.Connection{Protocol: protocol}
		request := Request{Messages: []Message{{Role: RoleSystem, Content: "system"}, {Role: RoleUser, Content: "next"}, {Role: RoleHarness, Content: "note"}}}
		var body map[string]any
		if protocol == config.ProtocolChatCompletions {
			body = BuildRequest(connection, request, false)
		} else {
			_, body = nativeRequest(connection, request)
		}
		raw, _ := json.Marshal(body)
		var decoded map[string]any
		_ = json.Unmarshal(raw, &decoded)
		list, _ := decoded["messages"].([]any)
		if list == nil {
			list, _ = decoded["input"].([]any)
		}
		if len(list) == 0 || list[len(list)-1].(map[string]any)["role"] != RoleUser {
			t.Fatalf("protocol %s last message is not user: %s", protocol, raw)
		}
	}
}
