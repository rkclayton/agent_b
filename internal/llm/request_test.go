package llm

import (
	"encoding/json"
	"reflect"
	"testing"

	"harness/internal/config"
)

func TestLegacyConnectionMigrationLeavesRecordedChatRequestEqual2s5(t *testing.T) {
	legacy := []byte(`{"id":"fixture","base_url":"https://models.example.test/v1","model":"first","sampling":{"thinking":{"temperature":0.31,"top_p":0.91},"nonthinking":{"temperature":0.42,"top_p":0.82,"presence_penalty":0.7}},"reasoning":{"control":"top_level","enabled":true,"effort":"high","valid_efforts":["high"]},"context":{"n_ctx":64000,"reserve_output":8000},"capabilities":{"reasoning_control":"top_level","findings":["server reasoning budget: accepted"]}}`)
	var before config.Connection
	if err := json.Unmarshal(legacy, &before); err != nil {
		t.Fatal(err)
	}
	persisted, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	var after config.Connection
	if err := json.Unmarshal(persisted, &after); err != nil {
		t.Fatal(err)
	}
	request := Request{Thinking: true, MaxTokens: 731, Messages: []Message{{Role: "system", Content: "fixture prompt"}, {Role: "user", Content: "fixture turn"}}, Tools: []any{map[string]any{"type": "function", "function": map[string]any{"name": "fixture_tool"}}}}
	beforeBody, afterBody := BuildRequest(&before, request, true), BuildRequest(&after, request, true)
	if !reflect.DeepEqual(beforeBody, afterBody) {
		t.Fatalf("request changed across migration:\nbefore=%#v\nafter=%#v", beforeBody, afterBody)
	}
}

func TestBuildRequestUsesCanonicalReserveDefault(t *testing.T) {
	body := BuildRequest(&config.Connection{}, Request{}, false)
	if got := body["max_tokens"]; got != config.DefaultReserveOutput {
		t.Fatalf("max_tokens=%v, want %d", got, config.DefaultReserveOutput)
	}
}

func TestBuildRequestCarriesConnectionReasoningCap(t *testing.T) {
	connection := &config.Connection{Reasoning: config.Reasoning{Control: "chat_template_kwargs", Enabled: true, MaxTokens: 1000}}
	body := BuildRequest(connection, Request{Thinking: true}, false)
	kwargs := body["chat_template_kwargs"].(map[string]any)
	if _, ok := kwargs["reasoning_budget"]; ok {
		t.Fatalf("template reasoning budget leaked: %v", kwargs)
	}
	connection.Reasoning.Control = "top_level"
	body = BuildRequest(connection, Request{Thinking: true}, false)
	if _, ok := body["reasoning_budget"]; ok {
		t.Fatalf("unprobed server reasoning budget leaked: %v", body)
	}
	connection.Capabilities.Findings = append(connection.Capabilities.Findings, "server reasoning budget: accepted")
	body = buildRequest(connection, Request{}, false)
	if body["reasoning_budget"] != 1000 {
		t.Fatalf("body=%v", body)
	}
}

func TestBuildRequestKeepsOnlyLeadingSystemRole(t *testing.T) {
	body := BuildRequest(&config.Connection{}, Request{Messages: []Message{
		{Role: "system", Content: "prompt"},
		{Role: "user", Content: "hello"},
		{Role: "system", Content: "old abort"},
	}}, false)
	messages := body["messages"].([]Message)
	if messages[0].Role != "system" || messages[2].Role != "assistant" || messages[2].Content != "[harness note]\nold abort" {
		t.Fatalf("messages=%#v", messages)
	}
}
