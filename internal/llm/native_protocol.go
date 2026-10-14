package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"time"

	"harness/internal/config"
)

const nativeStateMark = "\n\x1eagentb-provider-state:"

func splitNativeState(reasoning string) (string, []map[string]any) {
	cut := strings.LastIndex(reasoning, nativeStateMark)
	if cut < 0 {
		return reasoning, nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(reasoning[cut+len(nativeStateMark):]))
	if err != nil {
		return reasoning, nil
	}
	var state []map[string]any
	if json.Unmarshal(raw, &state) != nil {
		return reasoning, nil
	}
	return reasoning[:cut], state
}

func joinNativeState(reasoning string, state []map[string]any) string {
	if len(state) == 0 {
		return reasoning
	}
	raw, _ := json.Marshal(state)
	return reasoning + nativeStateMark + base64.RawStdEncoding.EncodeToString(raw)
}

// VisibleReasoning removes provider continuation state before accounting or UI.
func VisibleReasoning(reasoning string) string {
	visible, _ := splitNativeState(reasoning)
	return visible
}

func (c *Client) nativeChatStream(ctx context.Context, request Request, onDelta func(Delta), onConnected func()) (Response, error) {
	path, body := nativeRequest(c.connection, request)
	encoded, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, bytes.NewReader(encoded))
	if err != nil {
		return Response{}, err
	}
	var connected atomic.Bool
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) {
		connected.Store(true)
		if onConnected != nil {
			onConnected()
		}
	}}))
	started := time.Now()
	response, err := c.http.Do(req)
	if err != nil {
		return Response{}, &TransportError{Kind: transportKind(connected.Load()), Err: err}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return Response{}, fmt.Errorf("chat stream HTTP %d: %s", response.StatusCode, raw)
	}
	result := Response{Usage: Usage{CachedTokens: -1}}
	if c.connection.Protocol == config.ProtocolAnthropicMessages {
		err = parseAnthropicStream(response.Body, &result, onDelta)
	} else {
		err = parseResponsesStream(response.Body, &result, onDelta)
	}
	result.DurationMS = time.Since(started).Milliseconds()
	return result, err
}

func nativeRequest(connection *config.Connection, request Request) (string, map[string]any) {
	if connection.Protocol == config.ProtocolAnthropicMessages {
		return "/v1/messages", anthropicRequest(connection, request)
	}
	return "/v1/responses", responsesRequest(connection, request)
}

func anthropicRequest(connection *config.Connection, request Request) map[string]any {
	body := map[string]any{"model": connection.Model, "max_tokens": nativeOutputLimit(request.MaxTokens), "stream": true}
	var messages []any
	var system []any
	for _, message := range request.Messages {
		visibleReasoning, state := splitNativeState(message.ReasoningContent)
		if message.Role == "system" {
			system = append(system, map[string]any{"type": "text", "text": fmt.Sprint(message.Content), "cache_control": map[string]any{"type": "ephemeral"}})
			continue
		}
		if message.Role == "tool" {
			messages = append(messages, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": message.ToolCallID, "content": fmt.Sprint(message.Content)}}})
			continue
		}
		content := nativeContent(message.Content, true)
		if message.Role == "assistant" {
			content = append(append([]any{}, mapsToAny(state)...), content...)
			if len(state) == 0 && visibleReasoning != "" {
				content = append([]any{map[string]any{"type": "thinking", "thinking": visibleReasoning, "signature": ""}}, content...)
			}
			for _, call := range message.ToolCalls {
				var input any
				if json.Unmarshal([]byte(call.Function.Arguments), &input) != nil {
					input = map[string]any{}
				}
				content = append(content, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": input})
			}
		}
		messages = append(messages, map[string]any{"role": message.Role, "content": content})
	}
	body["messages"] = messages
	if len(system) > 0 {
		body["system"] = system
	}
	if len(request.Tools) > 0 {
		body["tools"] = anthropicTools(request.Tools)
	}
	outputLimit := nativeOutputLimit(request.MaxTokens)
	if request.Thinking && outputLimit > 1024 {
		budget := max(request.ReasoningMaxTokens, 1024)
		if budget >= outputLimit {
			budget = outputLimit - 1
		}
		body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
	}
	return body
}

func responsesRequest(connection *config.Connection, request Request) map[string]any {
	body := map[string]any{"model": connection.Model, "max_output_tokens": nativeOutputLimit(request.MaxTokens), "stream": true, "store": false, "include": []string{"reasoning.encrypted_content"}}
	var input []any
	for _, message := range request.Messages {
		_, state := splitNativeState(message.ReasoningContent)
		input = append(input, mapsToAny(state)...)
		if message.Role == "tool" {
			input = append(input, map[string]any{"type": "function_call_output", "call_id": message.ToolCallID, "output": fmt.Sprint(message.Content)})
			continue
		}
		if message.Role == "assistant" {
			for _, call := range message.ToolCalls {
				input = append(input, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
			}
		}
		if fmt.Sprint(message.Content) != "" {
			input = append(input, map[string]any{"type": "message", "role": message.Role, "content": nativeContent(message.Content, false)})
		}
	}
	body["input"] = input
	if len(request.Tools) > 0 {
		body["tools"] = responsesTools(request.Tools)
	}
	if request.Thinking {
		body["reasoning"] = map[string]any{"effort": connection.Reasoning.Effort, "summary": "auto"}
	}
	return body
}

func mapsToAny(values []map[string]any) []any {
	out := make([]any, len(values))
	for i := range values {
		out[i] = values[i]
	}
	return out
}
func nativeOutputLimit(value int) int {
	if value > 0 {
		return value
	}
	return config.DefaultReserveOutput
}

func nativeContent(value any, anthropic bool) []any {
	if text, ok := value.(string); ok {
		if text == "" {
			return nil
		}
		kind := "input_text"
		if anthropic {
			kind = "text"
		}
		return []any{map[string]any{"type": kind, "text": text}}
	}
	raw, _ := json.Marshal(value)
	var parts []map[string]any
	_ = json.Unmarshal(raw, &parts)
	out := make([]any, 0, len(parts))
	for _, part := range parts {
		if part["type"] == "text" {
			kind := "input_text"
			if anthropic {
				kind = "text"
			}
			out = append(out, map[string]any{"type": kind, "text": part["text"]})
			continue
		}
		image, _ := part["image_url"].(map[string]any)
		address, _ := image["url"].(string)
		if anthropic && strings.HasPrefix(address, "data:") {
			halves := strings.SplitN(address, ",", 2)
			media := strings.TrimPrefix(strings.SplitN(halves[0], ";", 2)[0], "data:")
			if len(halves) == 2 {
				out = append(out, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": media, "data": halves[1]}})
			}
		} else if address != "" {
			out = append(out, map[string]any{"type": "input_image", "image_url": address})
		}
	}
	return out
}

func anthropicTools(tools []any) []any {
	out := make([]any, 0, len(tools))
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		fn, _ := tool["function"].(map[string]any)
		if fn != nil {
			out = append(out, map[string]any{"name": fn["name"], "description": fn["description"], "input_schema": fn["parameters"]})
		}
	}
	return out
}
func responsesTools(tools []any) []any {
	out := make([]any, 0, len(tools))
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		fn, _ := tool["function"].(map[string]any)
		if fn != nil {
			out = append(out, map[string]any{"type": "function", "name": fn["name"], "description": fn["description"], "parameters": fn["parameters"]})
		}
	}
	return out
}

func scanSSE(reader io.Reader, visit func(map[string]any) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	seen := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return err
		}
		seen = true
		if err := visit(event); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !seen {
		return fmt.Errorf("chat stream contained no decodable chunks")
	}
	return nil
}

func parseAnthropicStream(reader io.Reader, result *Response, delta func(Delta)) error {
	blocks := map[int]map[string]any{}
	args := map[int]*strings.Builder{}
	var state []map[string]any
	err := scanSSE(reader, func(e map[string]any) error {
		typ, _ := e["type"].(string)
		index := intNumber(e["index"])
		switch typ {
		case "message_start":
			m, _ := e["message"].(map[string]any)
			setAnthropicUsage(result, mapValueAny(m["usage"]))
		case "content_block_start":
			block := mapValueAny(e["content_block"])
			blocks[index] = block
			if block["type"] == "tool_use" {
				args[index] = &strings.Builder{}
			}
		case "content_block_delta":
			d := mapValueAny(e["delta"])
			switch d["type"] {
			case "text_delta":
				text, _ := d["text"].(string)
				result.Content += text
				delta(Delta{Kind: "content", Text: text})
			case "thinking_delta":
				text, _ := d["thinking"].(string)
				result.Reasoning += text
				delta(Delta{Kind: "reasoning", Text: text})
			case "signature_delta":
				blocks[index]["signature"] = d["signature"]
			case "input_json_delta":
				text, _ := d["partial_json"].(string)
				args[index].WriteString(text)
				delta(Delta{Kind: "tool_call", Index: index, Text: text})
			}
		case "message_delta":
			d := mapValueAny(e["delta"])
			result.FinishReason = anthropicStop(fmt.Sprint(d["stop_reason"]))
			setAnthropicUsage(result, mapValueAny(e["usage"]))
		case "error":
			er := mapValueAny(e["error"])
			return fmt.Errorf("chat stream: %v: %v", er["type"], er["message"])
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := 0; i < len(blocks); i++ {
		block := blocks[i]
		switch block["type"] {
		case "thinking", "redacted_thinking":
			if block["type"] == "thinking" {
				block["thinking"] = VisibleReasoning(result.Reasoning)
			}
			state = append(state, block)
		case "tool_use":
			call := ToolCall{ID: fmt.Sprint(block["id"]), Type: "function", Function: FunctionCall{Name: fmt.Sprint(block["name"]), Arguments: args[i].String()}}
			result.ToolCalls = append(result.ToolCalls, call)
		}
	}
	result.Reasoning = joinNativeState(result.Reasoning, state)
	return nil
}

func parseResponsesStream(reader io.Reader, result *Response, delta func(Delta)) error {
	calls := map[int]*ToolCall{}
	args := map[int]*strings.Builder{}
	var state []map[string]any
	err := scanSSE(reader, func(e map[string]any) error {
		typ, _ := e["type"].(string)
		index := intNumber(e["output_index"])
		switch typ {
		case "response.output_text.delta":
			text, _ := e["delta"].(string)
			result.Content += text
			delta(Delta{Kind: "content", Text: text})
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			text, _ := e["delta"].(string)
			result.Reasoning += text
			delta(Delta{Kind: "reasoning", Text: text})
		case "response.output_item.added":
			item := mapValueAny(e["item"])
			if item["type"] == "function_call" {
				calls[index] = &ToolCall{ID: fmt.Sprint(item["call_id"]), Type: "function", Function: FunctionCall{Name: fmt.Sprint(item["name"])}}
				args[index] = &strings.Builder{}
			}
		case "response.function_call_arguments.delta":
			text, _ := e["delta"].(string)
			if args[index] != nil {
				args[index].WriteString(text)
			}
			delta(Delta{Kind: "tool_call", Index: index, Text: text})
		case "response.completed", "response.incomplete":
			response := mapValueAny(e["response"])
			usage := mapValueAny(response["usage"])
			result.Usage.PromptTokens = intNumber(usage["input_tokens"])
			result.Usage.CompletionTokens = intNumber(usage["output_tokens"])
			result.Usage.CachedTokens = intNumber(mapValueAny(usage["input_tokens_details"])["cached_tokens"])
			result.Usage.ReasoningTokens = intNumber(mapValueAny(usage["output_tokens_details"])["reasoning_tokens"])
			if response["status"] == "incomplete" {
				result.FinishReason = "length"
			} else if len(calls) > 0 {
				result.FinishReason = "tool_calls"
			} else {
				result.FinishReason = "stop"
			}
			raw, _ := response["output"].([]any)
			for _, v := range raw {
				item := mapValueAny(v)
				if item["type"] == "reasoning" {
					state = append(state, item)
				}
			}
		case "error", "response.failed":
			return fmt.Errorf("chat stream: %v", e)
		}
		return nil
	})
	if err != nil {
		return err
	}
	indexes := make([]int, 0, len(calls))
	for index := range calls {
		indexes = append(indexes, index)
	}
	for i := 0; i < len(indexes); i++ {
		for j := i + 1; j < len(indexes); j++ {
			if indexes[j] < indexes[i] {
				indexes[i], indexes[j] = indexes[j], indexes[i]
			}
		}
	}
	for _, index := range indexes {
		calls[index].Function.Arguments = args[index].String()
		result.ToolCalls = append(result.ToolCalls, *calls[index])
	}
	result.Reasoning = joinNativeState(result.Reasoning, state)
	return nil
}

func mapValueAny(value any) map[string]any {
	out, _ := value.(map[string]any)
	if out == nil {
		return map[string]any{}
	}
	return out
}
func intNumber(value any) int {
	if n, ok := value.(float64); ok {
		return int(n)
	}
	return 0
}
func setAnthropicUsage(result *Response, usage map[string]any) {
	if _, ok := usage["input_tokens"]; ok {
		result.Usage.PromptTokens = intNumber(usage["input_tokens"]) + intNumber(usage["cache_creation_input_tokens"]) + intNumber(usage["cache_read_input_tokens"])
	}
	if _, ok := usage["cache_read_input_tokens"]; ok {
		result.Usage.CachedTokens = intNumber(usage["cache_read_input_tokens"])
	}
	if n := intNumber(usage["output_tokens"]); n > 0 {
		result.Usage.CompletionTokens = n
	}
}
func anthropicStop(reason string) string {
	switch reason {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return "stop"
	}
}
