package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"harness/internal/config"
	contextmgr "harness/internal/context"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
)

type capturedBus struct {
	*events.Bus
	mu     sync.Mutex
	values []events.Event
}

func newCapturedBus() *capturedBus {
	value := &capturedBus{Bus: events.NewBus()}
	value.SetSink(func(event events.Event) error {
		value.mu.Lock()
		value.values = append(value.values, event)
		value.mu.Unlock()
		return nil
	})
	return value
}

func (b *capturedBus) Recent(sessionID string) []events.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := []events.Event{}
	for _, event := range b.values {
		if event.SessionID == sessionID {
			result = append(result, event)
		}
	}
	return result
}

func summaryAttempts(bus *capturedBus, sessionID string) []events.CompactionSummaryData {
	result := []events.CompactionSummaryData{}
	for _, event := range bus.Recent(sessionID) {
		if event.Type == events.CompactionSummary {
			result = append(result, event.Data.(events.CompactionSummaryData))
		}
	}
	return result
}

type shapeMessage struct {
	ID         string `json:"id"`
	Role       string `json:"role"`
	Category   string `json:"category"`
	Tokens     int    `json:"tokens"`
	Elided     bool   `json:"elided"`
	Name       string `json:"name"`
	ToolCallID string `json:"tool_call_id"`
	Turn       int    `json:"turn"`
	OK         *bool  `json:"ok"`
	ToolCalls  []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"tool_calls"`
}

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
		stub.sizes, stub.last = append(stub.sizes, len(body)), string(body)
		stub.mu.Unlock()
		if len(body) > stub.limit {
			http.Error(w, fmt.Sprintf(`{"error":{"message":"prompt too large: %d bytes (limit %d)"}}`, len(body), stub.limit), http.StatusBadRequest)
			return
		}
		if strings.Contains(string(body), `"stream":true`) {
			writeStreamChunk(t, w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "done"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 1}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "DONE: old\nNEXT: continue\nFILES CHANGED: none\nOPEN QUESTIONS: none"}, "finish_reason": "stop"}}})
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

// templateServer is a fake llama.cpp server whose chat template refuses what
// acme's refused (measured v0.69.0/W1): a message list that ends in two
// assistant messages. refuse forces that many refusals of any template call
// first. Chat calls are answered by respond, streamed or not as asked.
type templateServer struct {
	server      *httptest.Server
	refuse      atomic.Int32
	refusals    atomic.Int32
	templates   atomic.Int32
	mu          sync.Mutex
	requests    []string
	respond     func(body map[string]any, messages []map[string]any) map[string]any
	countPrompt bool
	tokenScale  int
}

const templateRefusal = `{"error":{"code":400,"message":"Cannot have 2 or more assistant messages at the end of the list.","type":"invalid_request_error"}}`

func newTemplateServer(t *testing.T, respond func(body map[string]any, messages []map[string]any) map[string]any) *templateServer {
	t.Helper()
	result := &templateServer{respond: respond}
	result.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/apply-template":
			result.templates.Add(1)
			var body struct {
				Messages []map[string]any `json:"messages"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			count := len(body.Messages)
			// Like acme's template (and the gate's fake): system messages may
			// lead the list, but not follow history.
			historyStarted := false
			for _, message := range body.Messages {
				if role := message["role"]; role == "system" || role == "developer" {
					if historyStarted {
						result.refusals.Add(1)
						w.WriteHeader(http.StatusInternalServerError)
						fmt.Fprint(w, "System message must be at the beginning.")
						return
					}
				} else {
					historyStarted = true
				}
			}
			forced := result.refuse.Load() > 0 && result.refuse.Add(-1) >= 0
			if forced || (count >= 2 && body.Messages[count-1]["role"] == "assistant" && body.Messages[count-2]["role"] == "assistant") {
				result.refusals.Add(1)
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, templateRefusal)
				return
			}
			encoded, _ := json.Marshal(body.Messages)
			_ = json.NewEncoder(w).Encode(map[string]string{"prompt": string(encoded)})
		case "/tokenize":
			var body struct {
				Content string `json:"content"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			scale := max(1, result.tokenScale)
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": make([]int, max(1, len([]rune(body.Content))/4*scale))})
		case "/v1/chat/completions":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			raw, _ := json.Marshal(body["messages"])
			result.mu.Lock()
			result.requests = append(result.requests, string(raw))
			result.mu.Unlock()
			var messages []map[string]any
			_ = json.Unmarshal(raw, &messages)
			reply := result.respond(body, messages)
			if stream, _ := body["stream"].(bool); !stream {
				message := map[string]any{"role": "assistant", "content": reply["content"]}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 10}})
				return
			}
			finish := "stop"
			delta := map[string]any{}
			delta["content"] = reply["content"]
			if calls, ok := reply["tool_calls"]; ok {
				delta["tool_calls"], finish = calls, "tool_calls"
			}
			writeStreamChunk(t, w, map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": result.promptTokens(body), "completion_tokens": 10}})
		default:
			http.NotFound(w, request)
		}
	}))
	t.Cleanup(result.server.Close)
	return result
}

func (s *templateServer) lastRequest() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		return ""
	}
	return s.requests[len(s.requests)-1]
}

func isSummaryRequest(messages []map[string]any) bool {
	if len(messages) == 0 {
		return false
	}
	text, _ := messages[len(messages)-1]["content"].(string)
	return strings.Contains(text, "Summarize the work so far") || strings.Contains(text, contextmgr.HandOffInstruction)
}

func toolCall(id, name, arguments string) map[string]any {
	return map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": arguments}}}}
}

// loadShape reads a content-free session shape (see s7_soft_line_replay_test.go)
// and fills each message with filler of its weight.
func loadShape(t *testing.T, name string) []events.Message {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", filepath.Base(name)))
	if err != nil {
		t.Fatal(err)
	}
	var shape []shapeMessage
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatal(err)
	}
	messages := make([]events.Message, 0, len(shape))
	for _, item := range shape {
		content := strings.Repeat("w", max(1, item.Tokens)*4)
		if item.Elided {
			content = "[elided: " + item.Name + " " + item.ID + "]"
		}
		message := events.Message{ID: item.ID, Role: item.Role, Category: item.Category, Tokens: item.Tokens, Elided: item.Elided, Name: item.Name, ToolCallID: item.ToolCallID, Turn: item.Turn, OK: item.OK, Content: content}
		if item.Role == "assistant" && len(item.ToolCalls) == 0 && item.Category != "summary" {
			message.Content = "answer " + message.Content
		}
		for _, call := range item.ToolCalls {
			message.ToolCalls = append(message.ToolCalls, events.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
		}
		messages = append(messages, message)
	}
	return messages
}

// templateRunner is truncationRunner's exact-accounting connection with the real
// read_file and find_files tools, so a replayed read returns real bytes.
func templateRunner(t *testing.T, server *templateServer) (*Runner, *session.Session, *capturedBus) {
	t.Helper()
	return templateRunnerReserve(t, server, 10240)
}

func templateRunnerReserve(t *testing.T, server *templateServer, reserve int) (*Runner, *session.Session, *capturedBus) {
	t.Helper()
	cfg := config.Defaults(t.TempDir())
	cfg.Context.Accounting = templateAccounting
	connection := cfg.Connections[0]
	connection.ID, connection.Label, connection.BaseURL, connection.Model = "main", "main", server.server.URL, "test-model"
	connection.RequestTimeoutS = 5
	connection.Context.NCtx = templateNCtx
	connection.Context.ReserveOutput = reserve
	connection.Capabilities.NCtx = templateNCtx
	connection.Capabilities.Streaming = true
	connection.Capabilities.ToolCalls = true
	connection.Capabilities.OverflowBehavior = "error"
	connection.Capabilities.Tokenize = true
	connection.Capabilities.ApplyTemplate = true
	connection.Capabilities.ApplyTemplateTools = true
	cfg.Connections = []config.Connection{connection}
	cfg.Agents = []config.Agent{{Name: "Main", B: "main", Toolset: config.FullToolset()}}
	bus := newCapturedBus()
	registry := tools.New(tools.NewReadFile(cfg.Tools.ReadFile), tools.NewGlob(cfg.Tools.FindFiles))
	runner := NewRunner(bus.Bus, registry, &PromptRenderer{text: "system {{workspace}} {{memory}} {{tools}}"}, cfg.Connection, func() config.Config { return cfg })
	enabled := map[string]bool{"read_file": true, "find_files": true}
	item := &session.Session{ID: "main", ConnectionID: "main", Workspace: t.TempDir(), Run: session.RunState{Status: "running"}, Runnable: true, ToolsEnabled: enabled, ToolCalls: map[string]int{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}, LastSeen: map[string]time.Time{}, Budget: events.Budget{NCtx: templateNCtx, Reserve: reserve}}
	return runner, item, bus
}

func reserveFrom(runner *Runner, messages []events.Message) {
	floor := int64(0)
	for _, message := range messages {
		var n int64
		if _, err := fmt.Sscanf(message.ID, "m-%d", &n); err == nil && n >= floor {
			floor = n + 1
		}
	}
	runner.ReserveIDs(floor)
}

func freshRequestFixture2sz(t *testing.T, respond func(map[string]any, []map[string]any) map[string]any, mid bool) (*Runner, *session.Session, *templateServer) {
	t.Helper()
	server := newTemplateServer(t, respond)
	runner, item, _ := templateRunner(t, server)
	messages := []events.Message{{ID: "old", Role: "assistant", Category: "history", Content: "old answer", Tokens: 20}, {ID: "task", Role: "user", Category: "history", Content: "CURRENT TASK VERBATIM", Tokens: 20}}
	if mid {
		messages = append(messages, events.Message{ID: "working", Role: "assistant", Category: "history", Content: "work underway", Tokens: 20})
	}
	item.ReplaceMessages(messages)
	item.SetRunPin("task")
	note := events.Message{ID: "handoff", Role: "harness", Category: "summary", Content: "Fresh-context hand-off:\nDONE: old work", Tokens: 5}
	item.ReplaceMessages([]events.Message{note, messages[1]})
	return runner, item, server
}

func TestFreshRequestEndsInVerbatimTask2sz(t *testing.T) {
	for _, mid := range []bool{false, true} {
		runner, item, server := freshRequestFixture2sz(t, func(map[string]any, []map[string]any) map[string]any { return map[string]any{"content": "done"} }, mid)
		if reason, detail, _ := runner.Run(context.Background(), item, "run"); reason != "done" {
			t.Fatalf("run: %s %q", reason, detail)
		}
		var request []map[string]any
		if err := json.Unmarshal([]byte(server.lastRequest()), &request); err != nil {
			t.Fatal(err)
		}
		last, prior := request[len(request)-1], request[len(request)-2]
		if last["role"] != "user" || last["content"] != "CURRENT TASK VERBATIM" || prior["role"] != "assistant" || !strings.Contains(fmt.Sprint(prior["content"]), "Fresh-context hand-off:") {
			t.Fatalf("mid=%t request=%v", mid, request)
		}
	}
}

func TestFreshRequestContinuesInsteadOfEchoingHandOff2sz(t *testing.T) {
	serverStep := 0
	runner, item, server := freshRequestFixture2sz(t, func(_ map[string]any, messages []map[string]any) map[string]any {
		last := messages[len(messages)-1]
		if last["role"] == "assistant" {
			return map[string]any{"content": last["content"]}
		}
		if serverStep == 0 {
			serverStep++
			return toolCall("next", "find_files", `{"pattern":"*.txt"}`)
		}
		return map[string]any{"content": "continued with the task"}
	}, true)
	if reason, detail, _ := runner.Run(context.Background(), item, "run"); reason != "done" {
		t.Fatalf("run: %s %q", reason, detail)
	}
	if len(server.requests) < 2 || item.MessagesCopy()[len(item.MessagesCopy())-1].Content != "continued with the task" {
		t.Fatalf("requests=%d messages=%+v", len(server.requests), item.MessagesCopy())
	}
}

func TestFreshRequestSurvivesRestartByteForByte2sz(t *testing.T) {
	respond := func(map[string]any, []map[string]any) map[string]any { return map[string]any{"content": "done"} }
	runner, item, server := freshRequestFixture2sz(t, respond, true)
	restored := item.MessagesCopy()
	if reason, _, _ := runner.Run(context.Background(), item, "live"); reason != "done" {
		t.Fatal(reason)
	}
	restartRunner, restartItem, _ := templateRunner(t, server)
	restartItem.Workspace = item.Workspace
	restartItem.ReplaceMessages(restored)
	restartItem.SetRunPin("task")
	if reason, _, _ := restartRunner.Run(context.Background(), restartItem, "restored"); reason != "done" {
		t.Fatal(reason)
	}
	if len(server.requests) != 2 || server.requests[0] != server.requests[1] {
		t.Fatalf("live and restored requests differ: %v", server.requests)
	}
}

// The walk's 3-long (v0.67.0/W9, main.jsonl seq 1506-1729): one long read, then
// the end-of-turn elide and summarize. Before item 2fd the summarize kept the
// previous answer (m-29) beside the note and every later request was refused.
// s9 r74's second summarize (v0.67.0/W4, s9 seq 7231): the retention boundary
// fell on m-123, an assistant answer, which stayed beside the note.
// A list already holding two adjacent assistant messages is folded before it is
// measured, so a chat that hit the 400 recovers on its next message.
func TestAdjacentAssistantsAreFoldedBeforeMeasurement(t *testing.T) {
	server := newTemplateServer(t, func(map[string]any, []map[string]any) map[string]any {
		return map[string]any{"content": "recovered"}
	})
	runner, item, _ := templateRunner(t, server)
	item.ReplaceMessages([]events.Message{
		{ID: "m-1", Role: "user", Category: "history", Content: "start", Tokens: 2},
		{ID: "m-2", Role: "assistant", Category: "summary", Content: "the note", Tokens: 2},
		{ID: "m-3", Role: "assistant", Category: "history", Content: "the kept answer", Tokens: 4},
	})
	reserveFrom(runner, item.MessagesCopy())
	if _, err := runner.AddUser(context.Background(), item, "go on"); err != nil {
		t.Fatal(err)
	}
	if reason, detail, _ := runner.Run(context.Background(), item, "r1"); reason != "done" {
		t.Fatalf("a broken chat must recover on its next message: %s %q", reason, detail)
	}
	if server.refusals.Load() != 0 {
		t.Fatalf("a list with adjacent assistants reached the template %d times", server.refusals.Load())
	}
	if request := server.lastRequest(); !strings.Contains(request, "the note\\n\\nthe kept answer") {
		t.Fatalf("the two assistant messages were not folded into one: %s", request)
	}
	if len(item.MessagesCopy()) != 5 {
		t.Fatalf("the session itself must keep both messages: %d", len(item.MessagesCopy()))
	}
}

func TestARefusedSystemAccountingTemplateDoesNotStopTheRun(t *testing.T) {
	for name, refusals := range map[string]int32{"one refusal": 1, "persistent refusal": 1000} {
		t.Run(name, func(t *testing.T) {
			server := newTemplateServer(t, func(map[string]any, []map[string]any) map[string]any {
				return map[string]any{"content": "ok"}
			})
			runner, item, bus := templateRunner(t, server)
			if _, err := runner.AddUser(context.Background(), item, "hello"); err != nil {
				t.Fatal(err)
			}
			server.refuse.Store(refusals)
			reason, detail, _ := runner.Run(context.Background(), item, "r1")
			// Accounting failures degrade the affected measurement instead of
			// consuming the run's malformed-history repair retry.
			assemblies := 0
			for _, event := range bus.Recent(item.ID) {
				if data, ok := event.Data.(map[string]any); ok && event.Type == events.Stage && data["stage"] == "assemble" && data["state"] == "enter" {
					assemblies++
				}
			}
			if reason != "done" || detail != "" || assemblies != 1 || server.refusals.Load() == 0 {
				t.Fatalf("accounting refusal stopped run: %s %q assemblies=%d refusals=%d", reason, detail, assemblies, server.refusals.Load())
			}
		})
	}
}

// Item 2fd rule 6: an image is sent with the turn it was attached in, and named
// afterwards; the model is told how to see it again.
func TestANativeImageIsAStubAfterItsTurn(t *testing.T) {
	server := newTemplateServer(t, func(map[string]any, []map[string]any) map[string]any {
		return map[string]any{"content": "seen"}
	})
	runner, item, _ := templateRunner(t, server)
	connection, _ := runner.connection(item.ConnectionID)
	connection.Capabilities.ImageInput = true
	connection.Capabilities.Vision = config.VisionReadsImages
	connection.ReadsImages = true
	cfg := runner.cfg()
	for index := range cfg.Connections {
		if cfg.Connections[index].ID == connection.ID {
			cfg.Connections[index] = *connection
		}
	}
	if err := os.MkdirAll(filepath.Join(item.Workspace, "attachments"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(item.Workspace, "attachments", "pixel.png"), []byte("PNG-BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	image := []events.Attachment{{Path: "attachments/pixel.png", Bytes: 9, SHA256: strings.Repeat("a", 64)}}
	requests := []string{}
	for turn, text := range []string{"describe this", "and now?", "one more"} {
		var attachments []events.Attachment
		if turn == 0 {
			attachments = image
		}
		if _, err := runner.AddUserAttachments(context.Background(), item, text, attachments); err != nil {
			t.Fatal(err)
		}
		if reason, detail, _ := runner.Run(context.Background(), item, fmt.Sprintf("r%d", turn+1)); reason != "done" {
			t.Fatalf("turn %d: %s %q", turn+1, reason, detail)
		}
		requests = append(requests, server.lastRequest())
	}
	if !strings.Contains(requests[0], "image_url") {
		t.Skipf("this connection did not send the image natively in its own turn: %.300s", requests[0])
	}
	if strings.Contains(requests[2], "image_url") || !strings.Contains(requests[2], "shown in an earlier turn and not re-sent") {
		t.Fatalf("turn 3's request must carry a stub for turn 1's image: %.600s", requests[2])
	}
}

// s7's 2026-09-18 20:01 restore, replayed with every 2fd rule (the 2ey shape,
// testdata/s7-2001-shape.json). The journal's newest user message before it was
// r2's m-3 (09-17), so restore's anchor is m-3, and m-3's own run owns the four
// large reads; they stay inline at restore and the soft-line pass takes them
// largest first before the first request.
func TestS7RestoreRunsTestWithUniqueIDs(t *testing.T) {
	server := newTemplateServer(t, func(_ map[string]any, messages []map[string]any) map[string]any {
		if isSummaryRequest(messages) {
			return map[string]any{"content": "INTENT: continue the rpg\nNEXT STEP: answer the operator"}
		}
		return map[string]any{"content": "test received"}
	})
	runner, item, bus := templateRunner(t, server)
	shape := loadShape(t, "s7-2001-shape.json")
	restored := contextmgr.StubResultsBefore(shape, "m-3", 16384)
	floor := int64(0)
	for _, message := range restored {
		if value, ok := contextmgrIDNumber(message.ID); ok && value > floor {
			floor = value
		}
	}
	restored, reminted, floor := contextmgr.RemintDuplicateIDs(restored, floor)
	seen := map[string]bool{}
	for _, message := range restored {
		if seen[message.ID] {
			t.Fatalf("restore left a duplicate id %s", message.ID)
		}
		seen[message.ID] = true
	}
	if len(reminted) != 12 {
		t.Fatalf("s7 holds twelve repeated ids; reminted %d", len(reminted))
	}
	item.ReplaceMessages(restored)
	runner.ReserveIDs(floor + 1)
	if _, err := runner.AddUser(context.Background(), item, "test"); err != nil {
		t.Fatal(err)
	}
	reason, detail, _ := runner.Run(context.Background(), item, "r-test")
	inline, stubs := 0, 0
	kept := []string{}
	anchor := -1
	for index, message := range restored {
		if message.ID == "m-3" {
			anchor = index
		}
	}
	for index, message := range item.MessagesCopy() {
		if message.Category == "files" {
			if message.Elided {
				stubs++
			} else {
				inline++
				// v0.70.2/W5 (2fd residual): name what keeps its bytes.
				kept = append(kept, fmt.Sprintf("%s(%s %d tokens, index %d, anchor m-3 at %d)", message.ID, message.Name, message.Tokens, index, anchor))
			}
		}
	}
	t.Logf("s7 20:01 inline files results: %v", kept)
	var firstRequest events.Budget
	for _, event := range bus.Recent(item.ID) {
		if event.Type == events.BudgetEvent && firstRequest.Ceiling == 0 {
			firstRequest = event.Data.(events.Budget)
		}
	}
	sent := -1
	for _, event := range bus.Recent(item.ID) {
		if event.Type == events.ModelRequest && sent < 0 {
			sent = event.Data.(map[string]any)["est_prompt_tokens"].(int)
		}
	}
	t.Logf("s7 20:01: reason=%s detail=%q files stubs=%d inline=%d; measured %d of ceiling %d before the soft-line pass, first request sent at %d; refusals=%d", reason, detail, stubs, inline, firstRequest.UsedEst, firstRequest.Ceiling, sent, server.refusals.Load())
	if soft := int(float64(firstRequest.Ceiling) * .75); sent < 0 || sent >= soft {
		t.Fatalf("the first request must go out under the soft line %d: %d", soft, sent)
	}
	if reason != "done" {
		t.Fatalf("\"test\" must run: %s %q", reason, detail)
	}
	if server.refusals.Load() != 0 {
		t.Fatalf("the template refused %d lists", server.refusals.Load())
	}
}

func contextmgrIDNumber(id string) (int64, bool) {
	var value int64
	if _, err := fmt.Sscanf(id, "m-%d", &value); err != nil {
		return 0, false
	}
	return value, true
}

// Item 2fg: a Stop cancels the run's context. The closing budget measurement
// runs on its own context, and a canceled request never reads as an outage —
// in the walk it did, and the false outage is what released the held queue.
func TestAStopIsNotAnOutage(t *testing.T) {
	canceled := &url.Error{Op: "Post", URL: "http://127.0.0.1:8080/apply-template", Err: context.Canceled}
	connection := &config.Connection{BaseURL: "http://127.0.0.1:8080"}
	if _, unavailable := modelUnavailable(connection, canceled); unavailable {
		t.Fatal("a canceled request was read as an unreachable model")
	}
	server := newTemplateServer(t, func(map[string]any, []map[string]any) map[string]any { return map[string]any{"content": "ok"} })
	runner, item, bus := templateRunner(t, server)
	if _, err := runner.AddUser(context.Background(), item, "hello"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner.Run(ctx, item, "r1")
	// The closing measurement runs after the stopped run returns.
	measured := false
	for deadline := time.Now().Add(3 * time.Second); !measured && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		for _, event := range bus.Recent(item.ID) {
			if event.Type == events.ModelUnreachable {
				t.Fatalf("a stopped run published model.unreachable: %v", event.Data)
			}
			measured = measured || event.Type == events.BudgetEvent
		}
	}
	if !measured {
		t.Fatal("the closing budget was not measured after the stop")
	}
}

// Item 2fg: after a stop the harness abort record (a system-role message) sits
// mid-history; every measurement lifts it to the front as the run's own
// assembly does, so the template never refuses a system message mid-list.
func TestTheAbortRecordLeadsEveryMeasurement(t *testing.T) {
	server := newTemplateServer(t, func(map[string]any, []map[string]any) map[string]any { return map[string]any{"content": "ok"} })
	runner, item, bus := templateRunner(t, server)
	connection, _ := runner.connection(item.ConnectionID)
	item.Append(events.Message{ID: "start", Role: "user", Category: "history", Content: "start"})
	item.Append(events.Message{ID: "abort", Role: "system", Category: "history", Content: harnessAbortRecordPrefix + "\nshell Start-Sleep was canceled"})
	item.Append(events.Message{ID: "after", Role: "user", Category: "history", Content: "You were stopped. Now just say CONTINUED."})
	if _, err := runner.measureSession(context.Background(), connection, item, nil, false); err != nil {
		t.Fatalf("a measurement with the abort record mid-history was refused: %v", err)
	}
	for _, event := range bus.Recent(item.ID) {
		if event.Type == events.Error {
			t.Fatalf("error: %v", event.Data)
		}
	}
}

// templateAccounting is the accounting templateRunner configures; a test that
// replays estimated accounting sets it for its own run.
var templateAccounting, templateNCtx = "exact", 32768

// promptTokens is what the fake server reports a request cost: 100, or with
// countPrompt set, the whole request at the fake tokenizer's four runes a token.
func (s *templateServer) promptTokens(body map[string]any) int {
	if !s.countPrompt {
		return 100
	}
	raw, _ := json.Marshal(body)
	return len([]rune(string(raw))) / 4
}
