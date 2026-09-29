package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/llm"
	"harness/internal/session"
	"harness/internal/tools"
)

// Item 2l8. The operator's own refusal, verbatim from his screenshot, and the
// message-count refusal that appears 105 times in production chat s9 — the byte
// reader must take the first and leave the second to the message-limit reader.
func TestAByteRefusalIsReadFromWhatTheServerSaid2l8(t *testing.T) {
	err := errors.New(`chat stream HTTP 400: {"error": "prompt too large: 401628 bytes (limit 400000)"}`)
	limit, sentence, matched := byteLimitError(err)
	if !matched {
		t.Fatal("the operator's own refusal was not recognised as a size refusal")
	}
	if limit != 400000 {
		t.Fatalf("limit %d, want 400000", limit)
	}
	if !strings.Contains(sentence, "401628 bytes") {
		t.Fatalf("the stop sentence loses what the server said: %q", sentence)
	}
	if _, _, matched := byteLimitError(errors.New(`chat stream HTTP 400: {"error": "conversation too long: 61 messages (limit 60)"}`)); matched {
		t.Fatal("a message-count refusal was read as a byte refusal")
	}
	if _, _, matched := byteLimitError(errors.New("chat stream HTTP 500: server on fire")); matched {
		t.Fatal("a 500 was read as a size refusal")
	}
}

// Compaction leaves headroom rather than fitting to the byte, because the next
// turn adds a message of its own.
func TestTheByteTargetLeavesHeadroom2l8(t *testing.T) {
	if target := byteLimitTarget(400000); target != 380000 {
		t.Fatalf("target %d, want 380000", target)
	}
	if target := byteLimitTarget(400000); target >= 400000 {
		t.Fatal("the target does not leave headroom")
	}
	if target := byteLimitTarget(1); target != 1 {
		t.Fatalf("a tiny limit must stay usable, got %d", target)
	}
}

// The measurement itself: the serialized request in the bytes the server counts,
// which is the number the token budget cannot see.
func TestTheSerializedRequestIsMeasuredInBytes2l8(t *testing.T) {
	connection := &config.Connection{ID: "c", Model: "m"}
	small := llm.Request{Messages: []llm.Message{{Role: "user", Content: "hello"}}}
	large := llm.Request{Messages: []llm.Message{{Role: "user", Content: strings.Repeat("x", 100000)}}}
	smallBytes := llm.SerializedBytes(connection, small, true)
	largeBytes := llm.SerializedBytes(connection, large, true)
	if smallBytes <= 0 {
		t.Fatal("a request measured as zero bytes")
	}
	if largeBytes <= smallBytes+90000 {
		t.Fatalf("the measurement does not track the body: %d then %d", smallBytes, largeBytes)
	}
	// A body the token budget would call small is still over a 400,000-byte cap.
	if largeBytes < byteLimitTarget(100000) {
		t.Fatalf("measurement %d is implausible for a 100,000-character message", largeBytes)
	}
}

// byteLimitStub refuses any request body over limit bytes with the sentence the
// operator's server used, and otherwise streams one short answer.
type byteLimitStub struct {
	server *httptest.Server
	limit  int
	mu     sync.Mutex
	sizes  []int
	last   string
}

func newByteLimitStub(t *testing.T, limit int) *byteLimitStub {
	t.Helper()
	stub := &byteLimitStub{limit: limit}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		stub.mu.Lock()
		stub.sizes = append(stub.sizes, len(body))
		stub.last = string(body)
		stub.mu.Unlock()
		if len(body) > stub.limit {
			http.Error(w, fmt.Sprintf(`{"error":{"message":"prompt too large: %d bytes (limit %d)"}}`, len(body), stub.limit), http.StatusBadRequest)
			return
		}
		if strings.Contains(string(body), `"stream":true`) {
			writeStreamChunk(t, w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "The file ends with the closing section."}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 4}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "summary"}, "finish_reason": "stop"}}})
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func byteLimitRunner(t *testing.T, stub *byteLimitStub) (*Runner, *session.Session, *config.Connection) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Defaults(root)
	cfg.Context.Accounting = "estimated"
	connection := cfg.Connections[0]
	connection.ID, connection.BaseURL, connection.Model = "main", stub.server.URL, "fake"
	connection.Context.NCtx, connection.Context.ReserveOutput = 1_000_000, 4096
	connection.Capabilities.Streaming, connection.Capabilities.ToolCalls = true, true
	cfg.Connections = []config.Connection{connection}
	runner := NewRunner(events.NewBus(), tools.New(), &PromptRenderer{text: "system"}, func(id string) (*config.Connection, bool) { return &connection, id == connection.ID }, func() config.Config { return cfg })
	item := &session.Session{ID: "main", ConnectionID: connection.ID, Workspace: root, Runnable: true, ToolsEnabled: map[string]bool{}, ToolCalls: map[string]int{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	return runner, item, &connection
}

// Item 2o8 CHECK 1: a 3-message chat holding one 450 KB tool result, against a
// server that refuses over 400,000 bytes, continues: the retried request is under
// the target and carries the head-and-tail marker. Before 2o8 it stopped, because
// summary needs more than seven messages.
func TestOneOversizedToolResultIsTrimmedAndTheRunContinues2o8(t *testing.T) {
	stub := newByteLimitStub(t, 400_000)
	runner, item, _ := byteLimitRunner(t, stub)
	ok := true
	item.Append(events.Message{ID: "u1", Role: "user", Content: "What does the log end with?", Category: "history"})
	item.Append(events.Message{ID: "a1", Role: "assistant", Category: "history", ToolCalls: []events.ToolCall{{ID: "c1", Name: "shell", Arguments: `{"command":"type big.log"}`}}})
	item.Append(events.Message{ID: "t1", Role: "tool", Category: "results", ToolCallID: "c1", Name: "shell", OK: &ok, Content: "START " + strings.Repeat("log line of the build output\n", 450_000/29) + " END"})
	reason, detail, _ := runner.Run(context.Background(), item, "r1")
	if reason != "done" {
		t.Fatalf("run stopped: %s %s (sizes %v)", reason, detail, stub.sizes)
	}
	last := stub.sizes[len(stub.sizes)-1]
	if last > byteLimitTarget(400_000) || !strings.Contains(stub.last, "the read was cut short") || !strings.Contains(stub.last, "START ") || !strings.Contains(stub.last, " END") {
		t.Fatalf("retried request %d bytes (target %d); marker/head/tail present: %v %v %v", last, byteLimitTarget(400_000), strings.Contains(stub.last, "cut short"), strings.Contains(stub.last, "START "), strings.Contains(stub.last, " END"))
	}
}

// Item 2o8 CHECK 2: the summary request is normalized at the one boundary (no
// harness role reaches the server — the operator's stop was "Unexpected message
// role") and trimmed under the limit before it is sent, and the stub accepts it.
func TestTheSummaryRequestIsTrimmedAndNormalizedBeforeItIsSent2o8(t *testing.T) {
	stub := newByteLimitStub(t, 400_000)
	runner, item, connection := byteLimitRunner(t, stub)
	connection.Capabilities.ObservedByteLimit = 400_000
	ok := true
	for index := 0; index < 9; index++ {
		item.Append(events.Message{ID: fmt.Sprintf("u%d", index), Role: "user", Content: "keep going", Category: "history", Tokens: 100})
		item.Append(events.Message{ID: fmt.Sprintf("n%d", index), Role: llm.RoleHarness, Content: "note", Category: "history", Tokens: 100})
	}
	item.Append(events.Message{ID: "t1", Role: "tool", Category: "files", Name: "read_file", ToolCallID: "c1", OK: &ok, Tokens: 110_000, Content: strings.Repeat("x", 450_000)})
	if !runner.summarize(context.Background(), item, "r1", connection) {
		t.Fatalf("summary refused (sizes %v): %s", stub.sizes, stub.last[:min(200, len(stub.last))])
	}
	if size := stub.sizes[len(stub.sizes)-1]; size > 400_000 || strings.Contains(stub.last, `"role":"harness"`) {
		t.Fatalf("summary request %d bytes; harness role present: %v", size, strings.Contains(stub.last, `"role":"harness"`))
	}
}

// Item 2o8 CHECK 3: with the limit known, a 150 KB read is answered by the
// retry-smaller path, and the window it names re-reads under the cap.
func TestAResultOverAQuarterOfTheLimitIsReadInWindows2o8(t *testing.T) {
	stub := newByteLimitStub(t, 400_000)
	runner, _, _ := byteLimitRunner(t, stub)
	content := strings.Repeat("0123456789012345678901234567890123456789012345678\n", 3000) // 150,000 bytes, 3000 lines
	answer, ok, metadata := runner.byteCapResult("read_file", map[string]any{"path": "big.txt", "offset": 1, "limit": 3000}, content, true, nil, 400_000)
	retry, _ := metadata["retry_limit"].(int)
	if ok || !strings.Contains(answer, "Retry read_file with the same offset=1 and limit no greater than") || retry < 1 {
		t.Fatalf("answer=%q metadata=%v", answer, metadata)
	}
	if window := len(strings.Join(strings.SplitAfter(content, "\n")[:retry], "")); window > 100_000 {
		t.Fatalf("the named window of %d lines is %d bytes, over the 100,000-byte cap", retry, window)
	}
}

// Item 2o8 CHECK 4: the operator's own 408,109-byte case, replayed read-only from
// his journal when AGENTB_REPLAY_JOURNAL names it (it is never copied into the
// repository): the messages that were live at the refusal, against a stub whose
// limit leaves the same overage, continue instead of stopping.
func TestTheOperatorsByteLimitCaseContinues2o8(t *testing.T) {
	path := os.Getenv("AGENTB_REPLAY_JOURNAL")
	if path == "" {
		t.Skip("AGENTB_REPLAY_JOURNAL is not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	var messages []events.Message
	for _, line := range lines {
		if strings.Contains(line, "408109") {
			break
		}
		var event struct {
			Type string `json:"type"`
			Data struct {
				Message events.Message `json:"message"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(line), &event) == nil && event.Type == "message.appended" {
			messages = append(messages, event.Data.Message)
		}
	}
	if len(messages) < 100 {
		t.Fatalf("replayed %d messages", len(messages))
	}
	measure := newByteLimitStub(t, 1<<30)
	runner, item, connection := byteLimitRunner(t, measure)
	item.ReplaceMessages(messages)
	if _, err := llm.BuildMessageList("system", summaryHistory(runner, connection, item)); err != nil {
		t.Fatalf("the summary request of his chat is still not a valid message list: %v", err)
	}
	first := llm.SerializedBytes(connection, llm.Request{Messages: requestHistory(connection, item)}, true)
	limit := first * 400_000 / 408_109
	stub := newByteLimitStub(t, limit)
	connection.BaseURL = stub.server.URL
	reason, detail, _ := runner.Run(context.Background(), item, "replay")
	if strings.Contains(detail, "could not be made smaller") || reason == "model_error" {
		t.Fatalf("replayed case stopped: %s %s (limit %d, sizes %v)", reason, detail, limit, stub.sizes)
	}
	t.Logf("replayed %d messages; first request about %d bytes against limit %d; requests %v; reason %s", len(messages), first, limit, stub.sizes, reason)
}

func summaryHistory(runner *Runner, connection *config.Connection, item *session.Session) []llm.Message {
	return runner.summaryMessages(connection, item)[1:]
}

func requestHistory(connection *config.Connection, item *session.Session) []llm.Message {
	out := []llm.Message{}
	for _, message := range item.MessagesCopy() {
		out = append(out, requestMessage(connection, item, message))
	}
	return out
}
