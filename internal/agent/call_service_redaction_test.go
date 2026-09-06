package agent

import (
	"os"
	"strings"
	"testing"

	"harness/internal/events"
)

func TestCallServiceHeadersRedactedFromDurableEventsAndHistory(t *testing.T) {
	const secret = "model-supplied-header-secret"
	calls := []events.ToolCall{{
		ID: "call-1", Name: "call_service",
		Arguments: `{"service":"broker","method":"POST","path":"jobs","headers":{"Authorization":"model-supplied-header-secret"}}`,
	}}
	durableCalls := sanitizedToolCalls(calls)
	args, err := decodeToolArguments(calls[0].Arguments)
	if err != nil {
		t.Fatal(err)
	}
	durableArgs := sanitizedToolArguments("call_service", args)
	raw := redactToolCallHeaders(`{"tool_calls":[{"arguments":"model-supplied-header-secret"}]}`, calls)

	root := t.TempDir()
	writers, err := events.NewWriters(root)
	if err != nil {
		t.Fatal(err)
	}
	path, err := writers.OpenSession("main")
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	bus.SetDurableSink(writers.WriteRecord, nil, nil)
	response := events.New(events.ModelResponse, "main", "run-1", map[string]any{"tool_calls": durableCalls})
	response.Raw = raw
	bus.Publish(response)
	bus.Publish(events.New(events.ToolCallEvent, "main", "run-1", map[string]any{"name": "call_service", "args": durableArgs}))
	bus.Publish(events.New(events.MessageAppended, "main", "run-1", map[string]any{"message": events.Message{ID: "m-1", Role: "assistant", ToolCalls: durableCalls}}))
	if err := writers.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || !strings.Contains(string(data), redactedToolValue) {
		t.Fatalf("durable redaction failed: %s", data)
	}
	if strings.Contains(durableCalls[0].Arguments, secret) || !strings.Contains(durableCalls[0].Arguments, redactedToolValue) {
		t.Fatalf("history arguments=%s", durableCalls[0].Arguments)
	}
}

func TestCallServiceRedactionDoesNotMutateExecutionArguments(t *testing.T) {
	args := map[string]any{"headers": map[string]any{"Accept": "application/secret", "Authorization": "Bearer secret"}, "body": map[string]any{"kept": "yes"}}
	redacted := sanitizedToolArguments("call_service", args)
	if args["headers"].(map[string]any)["Accept"] != "application/secret" {
		t.Fatalf("execution arguments mutated: %#v", args)
	}
	if redacted["headers"].(map[string]any)["Accept"] != "application/secret" || redacted["headers"].(map[string]any)["Authorization"] != redactedToolValue || redacted["body"].(map[string]any)["kept"] != "yes" {
		t.Fatalf("redacted arguments=%#v", redacted)
	}
}

func TestToolCallDeltaPayloadsAreNotWrittenToEvents(t *testing.T) {
	if got := durableModelDeltaText("tool_call", `{"headers":{"Authorization":"secret"}}`); got != "" {
		t.Fatalf("durable tool-call delta=%q", got)
	}
	if got := durableModelDeltaText("content", "visible"); got != "visible" {
		t.Fatalf("content delta=%q", got)
	}
	if got := redactToolCallHeaders("raw secret", []events.ToolCall{{Name: "call_service", Arguments: "{"}}); got != "" {
		t.Fatalf("invalid call_service raw payload was retained: %q", got)
	}
}
