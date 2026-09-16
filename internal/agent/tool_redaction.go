package agent

import (
	"encoding/json"
	"strings"

	"harness/internal/events"
)

const redactedToolValue = "[redacted]"

func sanitizedToolArguments(name string, args map[string]any) map[string]any {
	if name != "call_service" {
		return args
	}
	copy := cloneToolValue(args).(map[string]any)
	if headers, ok := copy["headers"].(map[string]any); ok {
		for key := range headers {
			if sensitiveServiceHeader(key) {
				headers[key] = redactedToolValue
			}
		}
	}
	return copy
}

func sanitizedToolCalls(calls []events.ToolCall) []events.ToolCall {
	out := append([]events.ToolCall(nil), calls...)
	for index := range out {
		if out[index].Name != "call_service" {
			continue
		}
		args, err := decodeToolArguments(out[index].Arguments)
		if err != nil {
			out[index].Arguments = `{"redacted":"invalid call_service arguments"}`
			continue
		}
		encoded, err := json.Marshal(sanitizedToolArguments(out[index].Name, args))
		if err != nil {
			out[index].Arguments = `{"redacted":"unavailable"}`
			continue
		}
		out[index].Arguments = string(encoded)
	}
	return out
}

func redactToolCallHeaders(raw string, calls []events.ToolCall) string {
	for _, call := range calls {
		if call.Name != "call_service" {
			continue
		}
		args, err := decodeToolArguments(call.Arguments)
		if err != nil {
			return ""
		}
		headers, _ := args["headers"].(map[string]any)
		for key, value := range headers {
			if !sensitiveServiceHeader(key) {
				continue
			}
			text, ok := value.(string)
			if ok && text != "" {
				raw = strings.ReplaceAll(raw, text, redactedToolValue)
			}
		}
	}
	return raw
}

func sensitiveServiceHeader(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "Authorization") || strings.EqualFold(strings.TrimSpace(name), "Proxy-Authorization")
}

func durableModelDeltaText(kind, value string) string {
	if kind == "tool_call" {
		return ""
	}
	return value
}

func decodeToolArguments(raw string) (map[string]any, error) {
	var value map[string]any
	err := json.Unmarshal([]byte(raw), &value)
	return value, err
}

func cloneToolValue(value any) any {
	switch item := value.(type) {
	case map[string]any:
		copy := make(map[string]any, len(item))
		for key, child := range item {
			copy[key] = cloneToolValue(child)
		}
		return copy
	case []any:
		copy := make([]any, len(item))
		for index, child := range item {
			copy[index] = cloneToolValue(child)
		}
		return copy
	default:
		return item
	}
}
