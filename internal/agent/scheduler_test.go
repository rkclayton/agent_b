package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/llm"
	"harness/internal/session"
	"harness/internal/tools"
)

func TestActiveRunMessagesQueueAtZeroDepthAndDispatchInOrderAfterRunEnd(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"))
	}))
	defer model.Close()
	workspace := t.TempDir()
	cfg := config.Defaults(workspace)
	cfg.Context.Accounting = "estimated"
	cfg.Run.QueueDepth = 0
	cfg.Run.MaxConcurrent = 1
	connection := cfg.Connections[0]
	connection.BaseURL = model.URL
	connection.RequestTimeoutS = 2
	connection.Context.NCtx = 32768
	connection.Context.ReserveOutput = 8192
	connection.Capabilities.Streaming = true
	connection.Capabilities.ToolCalls = true
	connection.Capabilities.OverflowBehavior = "error"
	connection.Capabilities.Tokenize = false
	cfg.Connections[0] = connection
	bus := events.NewBus()
	var eventMu sync.Mutex
	var recorded []events.Event
	bus.SetSink(func(event events.Event) error {
		eventMu.Lock()
		recorded = append(recorded, event)
		eventMu.Unlock()
		return nil
	})
	writers, err := events.NewWriters(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer writers.Close()
	connectionLookup := func(id string) (*config.Connection, bool) { return &connection, id == connection.ID }
	registry := session.NewRegistry(bus, writers, connectionLookup, 0, func() config.Config { return cfg })
	item, err := registry.Create("main", connection.ID, workspace)
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(bus, tools.New(), &PromptRenderer{text: "system"}, connectionLookup, func() config.Config { return cfg })
	scheduler := NewScheduler(runner, registry, bus, func() config.Config { return cfg })
	item.SetRun(session.RunState{Status: "running", RunID: "r0"})
	scheduler.active[item.ID] = &activeRun{}
	first, err := scheduler.Submit(context.Background(), item.ID, "first queued")
	if err != nil || !first.Queued || first.Position != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := scheduler.Submit(context.Background(), item.ID, "second queued")
	if err != nil || !second.Queued || second.Position != 2 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	scheduler.finish(queuedRun{s: item, runID: "r0"}, "done", "", 1)
	deadline := time.Now().Add(5 * time.Second)
	for (scheduler.Active(item.ID) || item.Snapshot().QueuedMessages != 0) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if scheduler.Active(item.ID) || item.Snapshot().QueuedMessages != 0 {
		t.Fatalf("queue did not drain: run=%+v queued=%d", item.Snapshot().Run, item.Snapshot().QueuedMessages)
	}
	messages := item.MessagesCopy()
	var users []string
	for _, message := range messages {
		if message.Role == "user" {
			users = append(users, message.Content)
		}
	}
	if len(users) != 2 || users[0] != "first queued" || users[1] != "second queued" {
		t.Fatalf("user dispatch order=%v", users)
	}
	eventMu.Lock()
	defer eventMu.Unlock()
	queued := 0
	started := 0
	for _, event := range recorded {
		if event.Type == events.MessageQueued {
			data, _ := event.Data.(map[string]any)
			if position, _ := data["position"].(int); position > 0 {
				queued++
			}
		}
		if event.Type == events.RunStarted {
			started++
		}
	}
	if queued != 2 {
		t.Fatalf("message.queued count=%d", queued)
	}
	if started != 1 {
		t.Fatalf("queued messages started %d runs, want one", started)
	}
}

func TestConnectionLimitUsesReportedCapacityUnlessOverridden2v4(t *testing.T) {
	workspace := t.TempDir()
	cfg := config.Defaults(workspace)
	cfg.Connections[0].Capabilities.ParallelCapacity = 4
	scheduler := &Scheduler{cfg: func() config.Config { return cfg }}
	if got := scheduler.connectionLimit(cfg.Connections[0].ID); got != 4 {
		t.Fatalf("reported limit=%d want=4", got)
	}
	cfg.Connections[0].MaxConcurrent = 2
	if got := scheduler.connectionLimit(cfg.Connections[0].ID); got != 2 {
		t.Fatalf("saved override=%d want=2", got)
	}
	cfg.Connections[0].MaxConcurrent = 0
	cfg.Connections[0].Capabilities.ParallelCapacity = 0
	if got := scheduler.connectionLimit(cfg.Connections[0].ID); got != 1 {
		t.Fatalf("unknown fallback=%d want=1", got)
	}
}

func TestQueuedMessagesJoinAfterCompletedToolResultsAtNextBoundary2tl(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer model.Close()
	_, _, item, scheduler := schedulerFixture(t, model.URL)
	item.Append(events.Message{ID: "task", Role: "user", Content: "original", Category: "history"})
	item.Append(events.Message{ID: "assistant", Role: "assistant", Category: "history", ToolCalls: []events.ToolCall{{ID: "call", Name: "read_file"}}})
	item.Append(events.Message{ID: "result", Role: "tool", ToolCallID: "call", Name: "read_file", Content: "complete", Category: "results"})
	item.SetRun(session.RunState{Status: "running", RunID: "r0"})
	scheduler.active[item.ID] = &activeRun{runID: "r0"}
	for _, text := range []string{"A", "A", "B"} {
		if _, err := scheduler.Submit(context.Background(), item.ID, text); err != nil {
			t.Fatal(err)
		}
	}
	scheduler.takeQueuedAtBoundary(item, "r0")
	messages := item.MessagesCopy()
	got := make([]string, 0, len(messages))
	for _, message := range messages {
		got = append(got, message.Role+":"+message.Content)
	}
	want := []string{"user:original", "assistant:", "tool:complete", "user:A", "user:B"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("boundary order=%v want %v", got, want)
	}
	if item.Snapshot().QueuedMessages != 0 {
		t.Fatalf("queued=%d", item.Snapshot().QueuedMessages)
	}
}

func TestCanonicalTerminalReasonsAreClosed(t *testing.T) {
	want := map[string]string{
		"done": "done", "reply_empty_reasoning_shown": "reply-empty-reasoning-shown",
		"aborted_mid_model": "cancelled-by-operator",
		"model_error":       "model-error", "tool_errors": "tool-errors",
		// Item 2lw (c): cycle was pinned here AS "harness-error", which is the
		// defect that item names -- a run the cycle guard stopped is not a harness
		// fault, and calling it one sends somebody looking for a bug that is not
		// there. The four that fell through now have words of their own.
		"cycle": "cycle", "mailbox_stop": "stopped-by-mailbox",
		"reply_empty": "reply-empty", "connection_not_runnable": "connection-not-runnable",
		// And the reason rel-1.20.0/W1 found live and undeclared.
		"malformed_turn": "model-error",
	}
	for input, expected := range want {
		if got := canonicalTerminalReason(input); got != expected {
			t.Errorf("canonicalTerminalReason(%q)=%q want %q", input, got, expected)
		}
	}
}

func TestStopReturnsQueuedMessagesAndNextSubmitSendsOnlyNewWords(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"))
	}))
	defer model.Close()
	_, connection, item, scheduler := schedulerFixture(t, model.URL)
	item.SetRun(session.RunState{Status: "running", RunID: "r0"})
	done := make(chan struct{})
	close(done)
	scheduler.active[item.ID] = &activeRun{cancel: func() {}, runID: "r0", done: done}
	if result, err := scheduler.Submit(context.Background(), item.ID, "first queued"); err != nil || result.Position != 1 {
		t.Fatalf("first=%+v err=%v", result, err)
	}
	if result, err := scheduler.Submit(context.Background(), item.ID, "second queued"); err != nil || result.Position != 2 {
		t.Fatalf("second=%+v err=%v", result, err)
	}
	scheduler.Stop(item.ID, false)
	scheduler.finish(queuedRun{s: item, runID: "r0"}, "user_stop", "", 1)
	if snapshot := item.Snapshot(); snapshot.Run.Status != "idle" || snapshot.QueuedMessages != 0 {
		t.Fatalf("returned snapshot=%+v", snapshot)
	}
	if len(item.MessagesCopy()) != 0 {
		t.Fatalf("held messages dispatched=%+v", item.MessagesCopy())
	}
	if _, err := scheduler.Submit(context.Background(), item.ID, "resume queue"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for (scheduler.Active(item.ID) || item.Snapshot().QueuedMessages != 0) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	var users []string
	for _, message := range item.MessagesCopy() {
		if message.Role == "user" {
			users = append(users, message.Content)
		}
	}
	if len(users) != 1 || users[0] != "resume queue" {
		t.Fatalf("dispatch order=%v connection=%s", users, connection.ID)
	}
}

func TestModelUnreachableHoldsQueueDeduplicatesAndProbeReleases(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"))
	}))
	defer model.Close()
	_, connection, item, scheduler := schedulerFixture(t, model.URL)
	item.SetRun(session.RunState{Status: "running", RunID: "r0"})
	scheduler.active[item.ID] = &activeRun{}
	first, err := scheduler.Submit(context.Background(), item.ID, "same pending bytes")
	if err != nil || first.Position != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := scheduler.Submit(context.Background(), item.ID, "same pending bytes")
	if err != nil || second.Position != 1 || item.Snapshot().QueuedMessages != 1 {
		t.Fatalf("dedupe second=%+v queued=%d err=%v", second, item.Snapshot().QueuedMessages, err)
	}
	scheduler.finish(queuedRun{s: item, runID: "r0"}, "model_unreachable", "dial tcp timeout", 1)
	if snapshot := item.Snapshot(); snapshot.Run.Status != "idle" || snapshot.QueuedMessages != 1 {
		t.Fatalf("waiting for model=%+v", snapshot)
	}
	if scheduler.Active(item.ID) {
		t.Fatal("unreachable queue dispatched before probe")
	}
	scheduler.ReleaseModel(connection.ID)
	deadline := time.Now().Add(3 * time.Second)
	for (scheduler.Active(item.ID) || item.Snapshot().QueuedMessages != 0) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if scheduler.Active(item.ID) || item.Snapshot().QueuedMessages != 0 {
		t.Fatalf("queue not released: %+v", item.Snapshot())
	}
}

func TestLongRunSlowAccountingCompletesEstimatedAndCompacts(t *testing.T) {
	var slow atomic.Bool
	var chatCalls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/apply-template":
			var body struct {
				Messages []llm.Message `json:"messages"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			var prompt strings.Builder
			for _, message := range body.Messages {
				prompt.WriteString(messageText(message.Content))
				prompt.WriteString(message.ReasoningContent)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"prompt": prompt.String()})
		case "/tokenize":
			var body struct {
				Content string `json:"content"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if len(body.Content) > 1000 && strings.Contains(body.Content, "busy") && slow.CompareAndSwap(true, false) {
				time.Sleep(3200 * time.Millisecond)
			}
			tokens := make([]int, max(1, len(body.Content)/4))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": tokens})
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			if chatCalls.Add(1) == 1 {
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-missing\",\"type\":\"function\",\"function\":{\"name\":\"missing\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":3000,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"))
				return
			}
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"))
		default:
			http.NotFound(w, request)
		}
	}))
	defer model.Close()
	_, connection, item, scheduler := schedulerFixture(t, model.URL)
	connection.RequestTimeoutS = 3
	connection.Context.NCtx, connection.Context.ReserveOutput = 9216, 1024
	connection.Capabilities.Tokenize = true
	connection.Capabilities.ApplyTemplate = true
	connection.Capabilities.ApplyTemplateTools = true
	scheduler.runner.cfg = func() config.Config {
		cfg := config.Defaults(item.Workspace)
		cfg.Context.Accounting = "exact"
		cfg.Connections[0] = *connection
		return cfg
	}
	for index := 0; index < 8; index++ {
		callID := fmt.Sprintf("old-call-%d", index)
		item.Append(events.Message{ID: fmt.Sprintf("old-assistant-%d", index), Role: "assistant", Category: "history", Tokens: 10, Turn: index + 1, ToolCalls: []events.ToolCall{{ID: callID, Name: "read_file", Arguments: `{"path":"old.txt","offset":1,"limit":1000}`}}})
		item.Append(events.Message{ID: fmt.Sprintf("old-result-%d", index), Role: "tool", Name: "read_file", ToolCallID: callID, Category: "files", Content: strings.Repeat("old result ", 400), Tokens: 1300, Turn: index + 1})
	}
	eventsSeen, unsubscribe := scheduler.bus.Subscribe()
	defer unsubscribe()
	slow.Store(true)
	started := time.Now()
	if _, err := scheduler.Submit(context.Background(), item.ID, "busy"); err != nil {
		t.Fatal(err)
	}
	var sawBusy, sawEstimated, sawCompaction bool
	deadline := time.After(12 * time.Second)
	for {
		select {
		case event := <-eventsSeen:
			switch event.Type {
			case events.ModelBusy:
				sawBusy = true
			case events.BudgetEvent:
				if budget, ok := event.Data.(events.Budget); ok && budget.Estimated {
					sawEstimated = true
				}
			case events.Compaction:
				sawCompaction = true
			case events.RunStopped:
				goto stopped
			}
		case <-deadline:
			t.Fatalf("slow-accounting run timed out: %+v", item.Snapshot().Run)
		}
	}

stopped:
	if elapsed := time.Since(started); elapsed < 3*time.Second {
		t.Fatalf("slow /tokenize elapsed=%s, want over 3s", elapsed)
	}
	if snapshot := item.Snapshot(); snapshot.Run.LastStopReason != "done" || !sawBusy || !sawEstimated || !sawCompaction || snapshot.CompactionCount == 0 {
		t.Fatalf("run=%+v busy=%t estimated=%t compaction=%t count=%d", snapshot.Run, sawBusy, sawEstimated, sawCompaction, snapshot.CompactionCount)
	}
}

func TestAccountingDialFailureRetriesThenStopsAsModelUnreachable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	_, connection, item, scheduler := schedulerFixture(t, "http://"+address)
	connection.Capabilities.Tokenize = true
	connection.Capabilities.ApplyTemplate = true
	connection.Capabilities.ApplyTemplateTools = true
	scheduler.runner.cfg = func() config.Config {
		cfg := config.Defaults(item.Workspace)
		cfg.Context.Accounting = "exact"
		cfg.Connections[0] = *connection
		return cfg
	}
	scheduler.runner.modelRetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	start := time.Now()
	if _, err := scheduler.Submit(context.Background(), item.ID, "unreachable"); err != nil {
		t.Fatal(err)
	}
	for scheduler.Active(item.ID) && time.Since(start) < 3*time.Second {
		time.Sleep(time.Millisecond)
	}
	if elapsed := time.Since(start); scheduler.Active(item.ID) || elapsed >= 3*time.Second {
		t.Fatalf("elapsed=%s active=%t", elapsed, scheduler.Active(item.ID))
	}
	if reason := item.Snapshot().Run.LastStopReason; reason != "model_unreachable" {
		t.Fatalf("reason=%q", reason)
	}
}

func TestStopCancelsBlackholedApplyTemplateInUnderOneSecond(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	blackhole := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/apply-template" {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-request.Context().Done():
			case <-release:
			}
			return
		}
		if request.URL.Path == "/tokenize" {
			_, _ = w.Write([]byte(`{"tokens":[1]}`))
			return
		}
		http.NotFound(w, request)
	}))
	defer blackhole.Close()
	defer close(release)
	_, connection, item, scheduler := schedulerFixture(t, blackhole.URL)
	connection.Capabilities.Tokenize = true
	connection.Capabilities.ApplyTemplate = true
	connection.Capabilities.ApplyTemplateTools = true
	scheduler.runner.cfg = func() config.Config {
		cfg := config.Defaults(item.Workspace)
		cfg.Context.Accounting = "exact"
		cfg.Connections[0] = *connection
		return cfg
	}
	if _, err := scheduler.Submit(context.Background(), item.ID, "blocked request"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("apply-template was not reached")
	}
	started := time.Now()
	scheduler.Stop(item.ID, false)
	for scheduler.Active(item.ID) && time.Since(started) < time.Second {
		time.Sleep(time.Millisecond)
	}
	if elapsed := time.Since(started); scheduler.Active(item.ID) || elapsed >= time.Second {
		t.Fatalf("stop elapsed=%s active=%t", elapsed, scheduler.Active(item.ID))
	}
	if reason := item.Snapshot().Run.LastStopReason; reason != "aborted_mid_run" {
		t.Fatalf("stop reason=%q", reason)
	}
}

func TestStopDetachesUncooperativeRunAtBound(t *testing.T) {
	_, _, item, scheduler := schedulerFixture(t, "http://127.0.0.1:1")
	item.SetRun(session.RunState{Status: "running", RunID: "r1"})
	scheduler.runner.beginFlight(item.ID, "r1")
	scheduler.runner.setFlightStage(item.ID, "r1", 3, "call_model")
	scheduler.active[item.ID] = &activeRun{cancel: func() {}, runID: "r1", done: make(chan struct{})}
	eventStream, unsubscribe := scheduler.bus.Subscribe()
	defer unsubscribe()
	started := time.Now()
	scheduler.Stop(item.ID, false)
	if elapsed := time.Since(started); elapsed < cancellationBound || elapsed > cancellationBound+time.Second {
		t.Fatalf("stop elapsed=%s bound=%s", elapsed, cancellationBound)
	}
	if scheduler.Active(item.ID) || item.Snapshot().Run.LastStopReason != "aborted_mid_model" {
		t.Fatalf("active=%t run=%+v", scheduler.Active(item.ID), item.Snapshot().Run)
	}
	want := map[string]bool{events.RunStopping: false, events.RunAborted: false, events.MessageAppended: false, events.RunStopped: false}
	for len(want) > 0 {
		select {
		case event := <-eventStream:
			if _, ok := want[event.Type]; ok {
				delete(want, event.Type)
			}
		case <-time.After(time.Second):
			t.Fatalf("missing events=%v", want)
		}
	}
	messages := item.MessagesCopy()
	if len(messages) != 1 || messages[0].Role != llm.RoleHarness || !strings.Contains(messages[0].Content, "HARNESS ABORT RECORD") {
		t.Fatalf("abort messages=%#v", messages)
	}
}

func schedulerFixture(t *testing.T, modelURL string) (config.Config, *config.Connection, *session.Session, *Scheduler) {
	return schedulerFixtureAccounting(t, modelURL, "estimated")
}

func schedulerFixtureAccounting(t *testing.T, modelURL, accounting string) (config.Config, *config.Connection, *session.Session, *Scheduler) {
	t.Helper()
	workspace := t.TempDir()
	cfg := config.Defaults(workspace)
	cfg.Context.Accounting = accounting
	cfg.Run.MaxConcurrent = 1
	connection := cfg.Connections[0]
	// Item 2nq (b): the literal "model" is not a model any more — a connection
	// carrying it is not runnable, which is the whole point — so this fixture names one.
	connection.BaseURL, connection.Model, connection.RequestTimeoutS = modelURL, "test-model", 30
	connection.Context.NCtx, connection.Context.ReserveOutput = 32768, 8192
	connection.Capabilities.Streaming, connection.Capabilities.ToolCalls, connection.Capabilities.OverflowBehavior = true, true, "error"
	connection.Capabilities.Tokenize = accounting == "exact"
	connection.Capabilities.ApplyTemplate = accounting == "exact"
	connection.Capabilities.ApplyTemplateTools = accounting == "exact"
	cfg.Connections[0] = connection
	bus := events.NewBus()
	writers, err := events.NewWriters(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	lookup := func(id string) (*config.Connection, bool) { return &connection, id == connection.ID }
	registry := session.NewRegistry(bus, writers, lookup, 0, func() config.Config { return cfg })
	item, err := registry.Create("main", connection.ID, workspace)
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(bus, tools.New(), &PromptRenderer{text: "system {{tools}} {{memory}}"}, lookup, func() config.Config { return cfg })
	return cfg, &connection, item, NewScheduler(runner, registry, bus, func() config.Config { return cfg })
}

func TestTransientModelFailureRetriesAndStopCancelsTheWait2ub(t *testing.T) {
	var requests atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) < 4 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer model.Close()
	_, _, item, scheduler := schedulerFixture(t, model.URL)
	scheduler.runner.modelRetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	stream, unsubscribe := scheduler.bus.Subscribe()
	defer unsubscribe()
	if _, err := scheduler.Submit(context.Background(), item.ID, "ride it out"); err != nil {
		t.Fatal(err)
	}
	retries := 0
	for scheduler.Active(item.ID) {
		select {
		case event := <-stream:
			if event.Type == events.ModelBusy && strings.Contains(fmt.Sprint(event.Data), "trying again") {
				retries++
			}
		case <-time.After(2 * time.Second):
			t.Fatal("retrying run timed out")
		}
	}
	if requests.Load() != 4 || retries != 3 || item.Snapshot().Run.LastStopReason != "done" {
		t.Fatalf("requests=%d retries=%d run=%+v", requests.Load(), retries, item.Snapshot().Run)
	}

	requests.Store(0)
	scheduler.runner.modelRetryDelays = []time.Duration{time.Second, time.Second, time.Second}
	if _, err := scheduler.Submit(context.Background(), item.ID, "stop the wait"); err != nil {
		t.Fatal(err)
	}
	for {
		event := <-stream
		if event.Type == events.ModelBusy && strings.Contains(fmt.Sprint(event.Data), "trying again") {
			break
		}
	}
	started := time.Now()
	scheduler.Stop(item.ID, false)
	if time.Since(started) >= time.Second || scheduler.Active(item.ID) {
		t.Fatalf("stop took %s active=%t", time.Since(started), scheduler.Active(item.ID))
	}
}

func TestConnectionRefusedForSixSecondsThenAnswers2ub(t *testing.T) {
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	_ = reserved.Close()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"recovered\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	})}
	ready := make(chan struct{})
	go func() {
		time.Sleep(6 * time.Second)
		listener, listenErr := net.Listen("tcp", address)
		if listenErr != nil {
			close(ready)
			return
		}
		close(ready)
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() { _ = server.Close() })
	_, _, item, scheduler := schedulerFixture(t, "http://"+address)
	started := time.Now()
	if _, err := scheduler.Submit(context.Background(), item.ID, "wait for recovery"); err != nil {
		t.Fatal(err)
	}
	waitSchedulerIdle(t, scheduler, item.ID)
	<-ready
	if item.Snapshot().Run.LastStopReason != "done" || time.Since(started) < 6*time.Second {
		t.Fatalf("elapsed=%s run=%+v", time.Since(started), item.Snapshot().Run)
	}
}

func TestConnectionLostAfterFirstAnswerKeepsTheUserMessage2ub(t *testing.T) {
	var requests atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call\",\"type\":\"function\",\"function\":{\"name\":\"not_offered\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("stand-in cannot close connection")
			return
		}
		connection, _, err := hijacker.Hijack()
		if err == nil {
			_ = connection.Close()
		}
	}))
	defer model.Close()
	_, _, item, scheduler := schedulerFixture(t, model.URL)
	scheduler.runner.modelRetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	if _, err := scheduler.Submit(context.Background(), item.ID, "keep after answer"); err != nil {
		t.Fatal(err)
	}
	waitSchedulerIdle(t, scheduler, item.ID)
	if requests.Load() != 5 || !containsUser(item.MessagesCopy(), "keep after answer") || item.Snapshot().Run.LastStopReason != "model_error" {
		t.Fatalf("requests=%d messages=%#v run=%+v", requests.Load(), item.MessagesCopy(), item.Snapshot().Run)
	}
}

func TestFirstRefusalReturnsDesktopMessageButNeverPhoneWorkerOrHelper2ub(t *testing.T) {
	var healthy atomic.Bool
	var bodiesMu sync.Mutex
	var bodies []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodiesMu.Lock()
		bodies = append(bodies, string(raw))
		bodiesMu.Unlock()
		if !healthy.Load() {
			http.Error(w, "wrong endpoint", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer model.Close()
	_, _, item, scheduler := schedulerFixture(t, model.URL)
	scheduler.runner.modelRetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	if _, err := scheduler.Submit(context.Background(), item.ID, "desktop original"); err != nil {
		t.Fatal(err)
	}
	waitSchedulerIdle(t, scheduler, item.ID)
	if requests := len(bodies); requests != 1 {
		t.Fatalf("404 requests=%d", requests)
	}
	if containsUser(item.MessagesCopy(), "desktop original") || !strings.Contains(item.Snapshot().Run.LastStopDetail, "your message is back in the box") {
		t.Fatalf("messages=%#v run=%+v", item.MessagesCopy(), item.Snapshot().Run)
	}
	healthy.Store(true)
	if _, err := scheduler.Submit(context.Background(), item.ID, "desktop original"); err != nil {
		t.Fatal(err)
	}
	waitSchedulerIdle(t, scheduler, item.ID)
	bodiesMu.Lock()
	joined := strings.Join(bodies, "\n")
	bodiesMu.Unlock()
	if strings.Count(joined, "desktop original") != 2 || !strings.Contains(joined, "prior run ended with model error") {
		t.Fatalf("requests=%s", joined)
	}

	for _, tc := range []struct{ name, role, source, origin string }{{"phone", "b", "phone", ""}, {"worker", "d", "", ""}, {"helper", "e", "", ""}, {"scheduled", "b", "", "scheduled"}} {
		_, _, protected, protectedScheduler := schedulerFixture(t, model.URL)
		protected.Role, protected.Origin = tc.role, tc.origin
		message := events.Message{ID: "protected-message", Role: "user", Category: "history", Content: tc.name + " original"}
		protected.Append(message)
		protectedScheduler.active[protected.ID] = &activeRun{runID: "protected", userMessageID: message.ID, source: tc.source}
		if protectedScheduler.returnActiveMessage(protected, "protected") || !containsUser(protected.MessagesCopy(), tc.name+" original") {
			t.Errorf("%s message moved: %#v", tc.name, protected.MessagesCopy())
		}
	}
}

func waitSchedulerIdle(t *testing.T, scheduler *Scheduler, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for scheduler.Active(sessionID) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if scheduler.Active(sessionID) {
		t.Fatal("scheduler did not become idle")
	}
}

func containsUser(messages []events.Message, text string) bool {
	for _, message := range messages {
		if message.Role == "user" && message.Content == text {
			return true
		}
	}
	return false
}

func TestRunPublishesWaitingLoadingAndReadingFromALlamaStandIn2rw(t *testing.T) {
	var statusRequests atomic.Int32
	release := make(chan struct{})
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			statusRequests.Add(1)
			fmt.Fprint(w, `{"data":[{"id":"test-model","status":{"value":"loading"}}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		<-release
		fmt.Fprint(w, "data: {\"prompt_progress\":{\"total\":14000,\"cache\":1200,\"processed\":2900}}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer model.Close()
	_, connection, item, scheduler := schedulerFixture(t, model.URL)
	connection.Capabilities.Server = "llama.cpp"
	stream, unsubscribe := scheduler.bus.Subscribe()
	defer unsubscribe()
	if _, err := scheduler.Submit(context.Background(), item.ID, "hello"); err != nil {
		t.Fatal(err)
	}
	waiting, loading, reading := false, false, false
	deadline := time.After(3 * time.Second)
	for !loading {
		select {
		case event := <-stream:
			data, _ := event.Data.(map[string]any)
			waiting = waiting || event.Type == events.ModelBusy && data["detail"] == "connected; waiting for model response"
			loading = event.Type == events.ModelBusy && data["detail"] == "loading model"
		case <-deadline:
			t.Fatal("waiting/loading events did not arrive")
		}
	}
	close(release)
	for !reading {
		select {
		case event := <-stream:
			reading = event.Type == events.ModelProgress
		case <-deadline:
			t.Fatal("reading event did not arrive")
		}
	}
	if !waiting || statusRequests.Load() != 1 {
		t.Fatalf("waiting=%t status requests=%d", waiting, statusRequests.Load())
	}
}

func TestAMessageSentWhileStoppingIsReturnedAndNeverSentLater(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"))
	}))
	defer model.Close()
	_, connection, item, scheduler := schedulerFixture(t, model.URL)
	item.SetRun(session.RunState{Status: "running", RunID: "r0"})
	done := make(chan struct{})
	close(done)
	scheduler.active[item.ID] = &activeRun{cancel: func() {}, runID: "r0", done: done}
	scheduler.Stop(item.ID, false)
	if result, err := scheduler.Submit(context.Background(), item.ID, "sent while stopping"); err != nil || !result.Queued {
		t.Fatalf("submit while stopping=%+v err=%v", result, err)
	}
	scheduler.finish(queuedRun{s: item, runID: "r0"}, "aborted_mid_tool", "tool execution canceled", 1)
	if snapshot := item.Snapshot(); snapshot.Run.Status != "idle" || snapshot.QueuedMessages != 0 {
		t.Fatalf("the message sent while stopping must be returned: %+v", snapshot.Run)
	}
	scheduler.ReleaseModel(connection.ID)
	time.Sleep(50 * time.Millisecond)
	if scheduler.Active(item.ID) || len(item.MessagesCopy()) != 0 {
		t.Fatalf("a reachability release started the held message: %+v", item.MessagesCopy())
	}
	scheduler.mu.Lock()
	other := &activeRun{cancel: func() {}, runID: "r-other", done: make(chan struct{})}
	scheduler.active["another-chat"] = other
	scheduler.drainLocked()
	delete(scheduler.active, "another-chat")
	scheduler.drainLocked()
	scheduler.mu.Unlock()
	scheduler.Stop(item.ID, false)
	scheduler.Stop("", true)
	time.Sleep(1500 * time.Millisecond)
	if scheduler.Active(item.ID) || len(item.MessagesCopy()) != 0 || item.Snapshot().Run.Status != "idle" {
		t.Fatalf("returned words re-entered the run: run %+v, messages %+v", item.Snapshot().Run, item.MessagesCopy())
	}
	if _, err := scheduler.Submit(context.Background(), item.ID, "go on"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for (scheduler.Active(item.ID) || item.Snapshot().QueuedMessages != 0) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	users := []string{}
	for _, message := range item.MessagesCopy() {
		if message.Role == "user" {
			users = append(users, message.Content)
		}
	}
	if strings.Join(users, "|") != "go on" {
		t.Fatalf("the returned message was sent later: %v", users)
	}
}

// Item 2fc: runs are admitted per model connection. A connection serves one run at a
// time unless its max_concurrent is raised; a run waiting on a busy connection
// names the role ahead of it, and another connection's run is not held back.
func TestRunsAreAdmittedPerConnectionAndNameTheRoleAhead(t *testing.T) {
	workspace := t.TempDir()
	cfg := config.Defaults(workspace)
	cfg.Run.MaxConcurrent = 4
	first := cfg.Connections[0]
	second := first
	second.ID, second.Label = "second", "second"
	cfg.Connections = []config.Connection{first, second}
	bus := events.NewBus()
	writers, err := events.NewWriters(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer writers.Close()
	lookup := func(id string) (*config.Connection, bool) {
		for index := range cfg.Connections {
			if cfg.Connections[index].ID == id {
				return &cfg.Connections[index], true
			}
		}
		return nil, false
	}
	registry := session.NewRegistry(bus, writers, lookup, 0, func() config.Config { return cfg })
	planner, _ := registry.Create("planner", first.ID, workspace)
	chat, _ := registry.Create("chat", first.ID, workspace)
	other, _ := registry.Create("other", second.ID, workspace)
	if planner == nil || chat == nil || other == nil {
		t.Fatal("sessions were not created")
	}
	planner.Role = "d"
	scheduler := NewScheduler(NewRunner(bus, tools.New(), &PromptRenderer{text: "system"}, lookup, func() config.Config { return cfg }), registry, bus, func() config.Config { return cfg })
	scheduler.active[planner.ID] = &activeRun{}

	scheduler.mu.Lock()
	sameConnection, otherConnection := scheduler.admitLocked(chat), scheduler.admitLocked(other)
	data := scheduler.queuedDataLocked(chat, "r9", 1)
	scheduler.mu.Unlock()
	if sameConnection || !otherConnection {
		t.Fatalf("admission: same connection=%t other connection=%t", sameConnection, otherConnection)
	}
	if data["behind"] != "agent_d" {
		t.Fatalf("run.queued data = %v, want behind agent_d", data)
	}
	if busy, role := scheduler.ConnectionBusy(first.ID); !busy || role != "agent_d" {
		t.Fatalf("ConnectionBusy = %t %q", busy, role)
	}
	cfg.Connections[0].MaxConcurrent = 2
	scheduler.mu.Lock()
	raised := scheduler.admitLocked(chat)
	scheduler.mu.Unlock()
	if !raised {
		t.Fatal("a raised connection limit still refused the second run")
	}
	cfg.Run.MaxConcurrent = 1
	scheduler.mu.Lock()
	global := scheduler.admitLocked(other)
	scheduler.mu.Unlock()
	if global {
		t.Fatal("the global limit no longer caps the total")
	}
}

func TestDefaultAdmitsFourConnectionsAndQueuesTheFifth2sg(t *testing.T) {
	workspace := t.TempDir()
	cfg := config.Defaults(workspace)
	first := cfg.Connections[0]
	cfg.Connections = nil
	for index := 0; index < 5; index++ {
		connection := first
		connection.ID, connection.Label = fmt.Sprintf("server-%d", index), fmt.Sprintf("Server %d", index)
		cfg.Connections = append(cfg.Connections, connection)
	}
	bus := events.NewBus()
	writers, err := events.NewWriters(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer writers.Close()
	lookup := func(id string) (*config.Connection, bool) {
		for index := range cfg.Connections {
			if cfg.Connections[index].ID == id {
				return &cfg.Connections[index], true
			}
		}
		return nil, false
	}
	registry := session.NewRegistry(bus, writers, lookup, 0, func() config.Config { return cfg })
	scheduler := NewScheduler(NewRunner(bus, tools.New(), &PromptRenderer{text: "system"}, lookup, func() config.Config { return cfg }), registry, bus, func() config.Config { return cfg })
	for index := 0; index < 4; index++ {
		item, createErr := registry.Create(fmt.Sprintf("chat-%d", index), cfg.Connections[index].ID, workspace)
		if createErr != nil {
			t.Fatal(createErr)
		}
		scheduler.mu.Lock()
		admitted := scheduler.admitLocked(item)
		if admitted {
			scheduler.active[item.ID] = &activeRun{}
		}
		scheduler.mu.Unlock()
		if !admitted {
			t.Fatalf("chat %d was not admitted", index+1)
		}
	}
	fifth, err := registry.Create("chat-4", cfg.Connections[4].ID, workspace)
	if err != nil {
		t.Fatal(err)
	}
	scheduler.mu.Lock()
	admitted := scheduler.admitLocked(fifth)
	scheduler.mu.Unlock()
	if admitted {
		t.Fatal("the fifth run was admitted past the global default")
	}
}
