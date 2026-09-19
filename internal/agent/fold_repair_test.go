package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// templateServer is a fake llama.cpp server whose chat template refuses what
// HomePC's refused (measured v0.69.0/W1): a message list that ends in two
// assistant messages. refuse forces that many refusals of any template call
// first. Chat calls are answered by respond, streamed or not as asked.
type templateServer struct {
	server    *httptest.Server
	refuse    atomic.Int32
	refusals  atomic.Int32
	templates atomic.Int32
	mu        sync.Mutex
	requests  []string
	respond   func(body map[string]any, messages []map[string]any) map[string]any
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
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": make([]int, max(1, len([]rune(body.Content))/4))})
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
			if calls, ok := reply["tool_calls"]; ok {
				delta["tool_calls"], finish = calls, "tool_calls"
			} else {
				delta["content"] = reply["content"]
			}
			writeStreamChunk(t, w, map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 10}})
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
	return strings.Contains(text, "Summarize the work so far")
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

// templateRunner is truncationRunner's exact-accounting profile with the real
// read_file and find_files tools, so a replayed read returns real bytes.
func templateRunner(t *testing.T, server *templateServer) (*Runner, *session.Session, *capturedBus) {
	t.Helper()
	cfg := config.Defaults(t.TempDir())
	cfg.Context.Accounting = "exact"
	profile := cfg.Servers[0]
	profile.ID, profile.Label, profile.BaseURL, profile.Model = "main", "main", server.server.URL, "test-model"
	profile.RequestTimeoutS = 5
	profile.Context.NCtx = 32768
	profile.Context.ReserveOutput = 10240
	profile.Capabilities.NCtx = 32768
	profile.Capabilities.Streaming = true
	profile.Capabilities.ToolCalls = true
	profile.Capabilities.OverflowBehavior = "error"
	profile.Capabilities.Tokenize = true
	profile.Capabilities.ApplyTemplate = true
	profile.Capabilities.ApplyTemplateTools = true
	cfg.Servers = []config.Profile{profile}
	cfg.Agents = []config.Agent{{Name: "Main", B: "main", Toolset: config.FullToolset()}}
	bus := newCapturedBus()
	registry := tools.New(tools.NewReadFile(cfg.Tools.ReadFile), tools.NewGlob(cfg.Tools.FindFiles))
	runner := NewRunner(bus.Bus, registry, &PromptRenderer{text: "system {{workspace}} {{memory}} {{tools}}"}, cfg.Profile, func() config.Config { return cfg })
	enabled := map[string]bool{"read_file": true, "find_files": true}
	item := &session.Session{ID: "main", ServerID: "main", Workspace: t.TempDir(), Run: session.RunState{Status: "running", MaxTurns: cfg.Run.MaxTurns}, Runnable: true, ToolsEnabled: enabled, ToolCalls: map[string]int{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}, LastSeen: map[string]time.Time{}, Budget: events.Budget{NCtx: 32768, Reserve: 10240}}
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

func assertNoAdjacentAssistants(t *testing.T, messages []events.Message) {
	t.Helper()
	for index := 1; index < len(messages); index++ {
		if messages[index].Role == "assistant" && messages[index-1].Role == "assistant" && len(messages[index-1].ToolCalls) == 0 {
			t.Fatalf("%s and %s are adjacent assistant messages", messages[index-1].ID, messages[index].ID)
		}
	}
}

// The walk's 3-long (v0.67.0/W9, main.jsonl seq 1506-1729): one long read, then
// the end-of-turn elide and summarize. Before item 2fd the summarize kept the
// previous answer (m-29) beside the note and every later request was refused.
func TestWalk3LongCompletesAnswersNextAndSurvivesRestart(t *testing.T) {
	step := 0
	server := newTemplateServer(t, func(body map[string]any, messages []map[string]any) map[string]any {
		if isSummaryRequest(messages) {
			return map[string]any{"content": "INTENT: read long-input.txt to its end\nUSER MESSAGES: (see the span)\nFILES: long-input.txt read from line 1\nNEXT STEP: continue from the next window"}
		}
		last, _ := messages[len(messages)-1]["content"].(string)
		switch {
		case strings.Contains(last, "Read long-input.txt"):
			step = 1
			return toolCall("call-find", "find_files", `{"pattern":"long-input.txt"}`)
		case step == 1:
			step = 2
			return toolCall("call-read", "read_file", `{"path":"long-input.txt","line":1,"lines":2000}`)
		default:
			step = 0
			return map[string]any{"content": "done: " + last[:min(20, len(last))]}
		}
	})
	runner, item, bus := templateRunner(t, server)
	lines := make([]string, 0, 2000)
	for index := 0; index < 2000; index++ {
		lines = append(lines, fmt.Sprintf("line %04d %s", index+1, strings.Repeat("lorem ipsum ", 9)))
	}
	if err := os.WriteFile(filepath.Join(item.Workspace, "long-input.txt"), []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	shape := loadShape(t, "walk-3long-shape.json")
	history := shape[:29] // m-1 … m-29: the chat before the long read's user message
	item.ReplaceMessages(history)
	reserveFrom(runner, shape)

	_, err := runner.AddUser(context.Background(), item, "Read long-input.txt completely, window by window, until you reach the end.")
	if err != nil {
		t.Fatal(err)
	}
	reason, detail, _ := runner.Run(context.Background(), item, "r5")
	summarized, foldedAnswer := false, false
	for _, event := range bus.Recent(item.ID) {
		if event.Type == events.Compaction && event.Data.(map[string]any)["kind"] == "summarize" {
			summarized = true
			for _, id := range event.Data.(map[string]any)["affected_ids"].([]string) {
				foldedAnswer = foldedAnswer || id == "m-29"
			}
		}
	}
	t.Logf("3-long: reason=%s detail=%q summarized=%t refusals=%d templates=%d", reason, detail, summarized, server.refusals.Load(), server.templates.Load())
	if reason != "done" || !summarized {
		t.Fatalf("the long read must compact and finish: reason=%s detail=%q summarized=%t", reason, detail, summarized)
	}
	// The walk folded m-2 … m-28 and kept m-29 beside the note.
	if !foldedAnswer {
		t.Fatal("the summarize must fold m-29, the answer that sat beside the note")
	}
	if server.refusals.Load() != 0 {
		t.Fatalf("the template refused %d lists", server.refusals.Load())
	}
	assertNoAdjacentAssistants(t, item.MessagesCopy())

	// The next message runs.
	if _, err := runner.AddUser(context.Background(), item, "Open the next thing."); err != nil {
		t.Fatal(err)
	}
	if reason, detail, _ := runner.Run(context.Background(), item, "r6"); reason != "done" {
		t.Fatalf("the next message did not run: %s %q", reason, detail)
	}

	// Restart and reopen: restore anchors on the newest user message and the
	// chat still answers.
	second, err := runner.AddUser(context.Background(), item, "Count to three.")
	if err != nil {
		t.Fatal(err)
	}
	if reason, _, _ := runner.Run(context.Background(), item, "r7"); reason != "done" {
		t.Fatalf("third message: %s", reason)
	}
	restored := contextmgr.StubResultsBefore(item.MessagesCopy(), second.ID, 16384)
	restartRunner, restartItem, _ := templateRunner(t, server)
	restartItem.Workspace = item.Workspace
	restartItem.ReplaceMessages(restored)
	reserveFrom(restartRunner, restored)
	if _, err := restartRunner.AddUser(context.Background(), restartItem, "Still there?"); err != nil {
		t.Fatal(err)
	}
	if reason, detail, _ := restartRunner.Run(context.Background(), restartItem, "r8"); reason != "done" {
		t.Fatalf("after a restart the chat must still answer: %s %q", reason, detail)
	}
	if server.refusals.Load() != 0 {
		t.Fatalf("the template refused %d lists across the restart", server.refusals.Load())
	}
}

// s9 r74's second summarize (v0.67.0/W4, s9 seq 7231): the retention boundary
// fell on m-123, an assistant answer, which stayed beside the note.
func TestS9SummarizeSpanEndsOnAUserMessage(t *testing.T) {
	messages := loadShape(t, "s9-7231-shape.json")
	foldEnd, ok := contextmgr.SummarizeSpan(messages, "m-124")
	if !ok {
		t.Fatal("s9 had a span to fold")
	}
	if messages[foldEnd].Role != "user" || messages[foldEnd].ID != "m-124" {
		t.Fatalf("the fold must end on the running turn's user message, ended at %s (%s)", messages[foldEnd].ID, messages[foldEnd].Role)
	}
	bus := newCapturedBus()
	s := &session.Session{ID: "s9"}
	s.ReplaceMessages(messages)
	s.SetRunPin("m-124")
	note := events.Message{ID: "m-130", Role: "assistant", Category: "summary", Content: "note", Tokens: 700}
	if !contextmgr.New(bus.Bus).Summarize(s, "r74", note, events.CompactionSummaryData{Trigger: "summary_pct"}) {
		t.Fatal("s9's summarize was rejected")
	}
	after := s.MessagesCopy()
	assertNoAdjacentAssistants(t, after)
	ids := []string{}
	for _, message := range after {
		ids = append(ids, message.ID)
	}
	if strings.Join(ids, " ") != "m-70 m-130 m-124 m-125 m-126 m-128 m-129" {
		t.Fatalf("s9 after the summarize: %v", ids)
	}
}

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

func TestARefusedTemplateIsRetriedOnceThenStopsWithTheReason(t *testing.T) {
	for name, refusals := range map[string]int32{"first refusal retried": 1, "second refusal stops": 1000} {
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
			if refusals == 1 {
				if reason != "done" || server.refusals.Load() != 1 {
					t.Fatalf("one refusal must be retried once and succeed: %s %q refusals=%d", reason, detail, server.refusals.Load())
				}
				return
			}
			// The run assembles twice — the first try and the one retry — and
			// stops; the post-stop budget publication measures once more.
			assemblies := 0
			for _, event := range bus.Recent(item.ID) {
				if data, ok := event.Data.(map[string]any); ok && event.Type == events.Stage && data["stage"] == "assemble" && data["state"] == "enter" {
					assemblies++
				}
			}
			if reason != "model_error" || !strings.Contains(detail, "Cannot have 2 or more assistant messages") || assemblies != 2 {
				t.Fatalf("a second refusal must stop with the reason: %s %q assemblies=%d", reason, detail, assemblies)
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
	profile, _ := runner.profile(item.ServerID)
	profile.Capabilities.ImageInput = true
	profile.Capabilities.Vision = config.VisionReadsImages
	cfg := runner.cfg()
	for index := range cfg.Servers {
		if cfg.Servers[index].ID == profile.ID {
			cfg.Servers[index] = *profile
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
		t.Skipf("this profile did not send the image natively in its own turn: %.300s", requests[0])
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
	for _, message := range item.MessagesCopy() {
		if message.Category == "files" {
			if message.Elided {
				stubs++
			} else {
				inline++
			}
		}
	}
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
