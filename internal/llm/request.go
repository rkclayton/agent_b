package llm

import (
	"fmt"
	"log"

	"harness/internal/config"
)

func buildRequest(profile *config.Profile, request Request, stream bool) map[string]any {
	request.Messages = normalizeSystemRoles(request.Messages)
	sampling := profile.Sampling.Nonthinking
	if request.Thinking {
		sampling = profile.Sampling.Thinking
	}
	body := map[string]any{"model": profile.Model, "messages": request.Messages, "temperature": sampling.Temperature, "top_p": sampling.TopP, "presence_penalty": sampling.PresencePenalty, "max_tokens": request.MaxTokens, "stream": stream}
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
	if profile.Capabilities.Server == "llama.cpp" {
		body["top_k"] = sampling.TopK
		body["min_p"] = sampling.MinP
		body["repeat_penalty"] = sampling.RepeatPenalty
		body["cache_prompt"] = true
		body["return_progress"] = true
	}
	control := profile.Reasoning.Control
	if control == "auto" {
		control = profile.Capabilities.ReasoningControl
	}
	effortAllowed := len(profile.Reasoning.ValidEfforts) > 0 && contains(profile.Reasoning.ValidEfforts, profile.Reasoning.Effort)
	switch control {
	case "chat_template_kwargs":
		kwargs := map[string]any{"enable_thinking": profile.Reasoning.Enabled, "preserve_thinking": profile.Reasoning.Preserve}
		if effortAllowed {
			kwargs["reasoning_effort"] = profile.Reasoning.Effort
		}
		if profile.Reasoning.MaxTokens > 0 {
			kwargs["reasoning_budget"] = profile.Reasoning.MaxTokens
		}
		body["chat_template_kwargs"] = kwargs
	case "top_level":
		if effortAllowed {
			body["reasoning_effort"] = profile.Reasoning.Effort
		}
		if profile.Reasoning.MaxTokens > 0 {
			body["reasoning_budget"] = profile.Reasoning.MaxTokens
		}
	}
	return body
}

// normalizeSystemRoles keeps the request contract accepted by strict chat
// servers: zero or one system message, and when present it is message zero.
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

func BuildRequest(profile *config.Profile, request Request, stream bool) map[string]any {
	return buildRequest(profile, request, stream)
}
func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
