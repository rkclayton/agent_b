package llm

import (
	"testing"

	"harness/internal/config"
)

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
	if kwargs["reasoning_budget"] != 1000 {
		t.Fatalf("kwargs=%v", kwargs)
	}
	connection.Reasoning.Control = "top_level"
	body = BuildRequest(connection, Request{Thinking: true}, false)
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
