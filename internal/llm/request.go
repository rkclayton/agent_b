package llm

import (
	"encoding/json"
	"fmt"
	"log"

	"harness/internal/config"
)

func buildRequest(connection *config.Connection, request Request, stream bool) map[string]any {
	request.Messages = normalizeSystemRoles(request.Messages)
	sampling := connection.Sampling.Nonthinking
	if request.Thinking {
		sampling = connection.Sampling.Thinking
		if connection.IsQwen38() && sampling.Temperature == .6 {
			sampling.Temperature = 1
		}
	}
	body := map[string]any{"model": connection.Model, "messages": request.Messages, "temperature": sampling.Temperature, "top_p": sampling.TopP, "presence_penalty": sampling.PresencePenalty, "max_tokens": request.MaxTokens, "stream": stream}
	if request.MaxTokens == 0 {
		body["max_tokens"] = config.DefaultReserveOutput
	}
	if len(request.Tools) > 0 {
		body["tools"] = request.Tools
		if request.ToolChoice != nil {
			body["tool_choice"] = request.ToolChoice
		}
	}
	if stream {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	if connection.Capabilities.Server == "llama.cpp" || contains(connection.Capabilities.Findings, "sampling top_k/min_p: accepted") {
		body["top_k"] = sampling.TopK
		body["min_p"] = sampling.MinP
	}
	if connection.Capabilities.Server == "llama.cpp" {
		body["repeat_penalty"] = sampling.RepeatPenalty
		body["cache_prompt"] = true
		body["return_progress"] = true
	}
	control := connection.Reasoning.Control
	if control == "auto" {
		control = connection.Capabilities.ReasoningControl
	}
	effortAllowed := len(connection.Reasoning.ValidEfforts) > 0 && contains(connection.Reasoning.ValidEfforts, connection.Reasoning.Effort)
	switch control {
	case "chat_template_kwargs":
		kwargs := map[string]any{"enable_thinking": connection.Reasoning.Enabled, "preserve_thinking": connection.Reasoning.Preserve}
		if effortAllowed {
			kwargs["reasoning_effort"] = connection.Reasoning.Effort
		}
		body["chat_template_kwargs"] = kwargs
	case "top_level":
		if effortAllowed {
			body["reasoning_effort"] = connection.Reasoning.Effort
		}
		if connection.Reasoning.MaxTokens > 0 && contains(connection.Capabilities.Findings, "server reasoning budget: accepted") {
			body["reasoning_budget"] = connection.Reasoning.MaxTokens
		} else if request.ReasoningMaxTokens > 0 && contains(connection.Capabilities.Findings, "server reasoning budget: accepted") {
			body["reasoning_budget"] = request.ReasoningMaxTokens
		}
	}
	return body
}

// normalizeSystemRoles keeps the request contract accepted by strict chat
// connections: zero or one system message, and when present it is message zero.
// Durable history is not rewritten; historical harness notes are represented
// as explicit assistant context only in the outbound request.
func normalizeSystemRoles(messages []Message) []Message {
	out := append([]Message(nil), messages...)
	for i := range out {
		if out[i].Role != "system" || i == 0 {
			continue
		}
		out[i].Role = "assistant"
		out[i].Content = fmt.Sprintf("[harness note]\n%v", out[i].Content)
	}
	return out
}

func logSystemRoleViolation(messages []Message, status int) {
	if status != 400 {
		return
	}
	for i, message := range messages {
		if message.Role == "system" && i > 0 {
			log.Printf("chat HTTP 400: system role at offending message index %d", i)
			return
		}
	}
}

func BuildRequest(connection *config.Connection, request Request, stream bool) map[string]any {
	return buildRequest(connection, request, stream)
}
func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// SerializedBytes is the size of the request body this client would send, in the
// bytes the server actually counts. Item 2l8: the token budget cannot see this
// number, and a server that caps the serialized request refuses a body the token
// budget believes has room.
func SerializedBytes(connection *config.Connection, request Request, stream bool) int {
	encoded, err := json.Marshal(buildRequest(connection, request, stream))
	if err != nil {
		return 0
	}
	return len(encoded)
}
