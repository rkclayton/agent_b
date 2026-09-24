package llm

import (
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
