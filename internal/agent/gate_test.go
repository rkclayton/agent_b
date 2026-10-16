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
	"harness/internal/events"
	"harness/internal/session"
	toolpkg "harness/internal/tools"
)

func skillApprovalFixture2vh(t *testing.T) (*Runner, *session.Session, <-chan events.Event, func()) {
	t.Helper()
	workspace, profile := t.TempDir(), t.TempDir()
	skillsRoot := filepath.Join(profile, "skills")
	if err := os.MkdirAll(skillsRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults(workspace)
	cfg.Approval.Mode = config.ApprovalModeOff
	cfg.Shell.ServiceAccount.Enabled = false
	shell := toolpkg.NewShell(cfg.Shell)
	shell.Configure(cfg)
	bus := events.NewBus()
	coordinator := toolpkg.NewFileCoordinator(session.NewWorkspaceRegistry(), func(id string) string { return id }, bus)
	registry := toolpkg.New(toolpkg.NewWriteFile(coordinator), toolpkg.NewEditFile(coordinator), shell)
	runner := NewRunner(bus, registry, nil, nil, func() config.Config { return cfg })
	eventCh, unsubscribe := bus.Subscribe()
	item := &session.Session{ID: "skill-write", Workspace: workspace, SkillsRoot: skillsRoot, Run: session.RunState{Status: "running"}, ToolsEnabled: map[string]bool{"write_file": true, "edit_file": true, "shell": true}, ToolCalls: map[string]int{}, LastSeen: map[string]time.Time{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	return runner, item, eventCh, unsubscribe
}

func TestSkillWriteAlwaysRaisesCardAndHonorsDecision2vh(t *testing.T) {
	runner, item, eventCh, unsubscribe := skillApprovalFixture2vh(t)
	defer unsubscribe()
	target := filepath.Join(item.SkillsRoot, "fixture", "SKILL.md")
	args := map[string]any{"path": target, "content": "# Fixture skill\n"}
	done := make(chan toolpkg.CallOutcome, 1)
	go func() { done <- runner.executeTool(context.Background(), item, "run", "approved", "write_file", args) }()
	required := nextApprovalEvent(t, eventCh).Data.(map[string]any)
	cardArgs := required["args"].(map[string]any)
	if cardArgs["path"] != target || cardArgs["content"] != args["content"] || cardArgs["skill"] != "fixture" {
		t.Fatalf("new-skill card=%#v", required)
	}
	if err := runner.gate.Decide(item.ID, "approved", "approve"); err != nil {
		t.Fatal(err)
	}
	if outcome := <-done; !outcome.OK {
		t.Fatalf("approved outcome=%+v", outcome)
	}
	if body, err := os.ReadFile(target); err != nil || string(body) != args["content"] {
		t.Fatalf("approved file=%q err=%v", body, err)
	}

	denied := filepath.Join(item.SkillsRoot, "denied", "SKILL.md")
	go func() {
		done <- runner.executeTool(context.Background(), item, "run", "denied", "write_file", map[string]any{"path": denied, "content": "denied"})
	}()
	nextApprovalEvent(t, eventCh)
	if err := runner.gate.Decide(item.ID, "denied", "deny"); err != nil {
		t.Fatal(err)
	}
	if outcome := <-done; outcome.OK || outcome.Content != "error: call denied by user" {
		t.Fatalf("denied outcome=%+v", outcome)
	}
	if _, err := os.Stat(denied); !os.IsNotExist(err) {
		t.Fatalf("denied file exists: %v", err)
	}
}

func TestSkillEditCardShowsChangeAndApprovedEditLands2vh(t *testing.T) {
	runner, item, eventCh, unsubscribe := skillApprovalFixture2vh(t)
	defer unsubscribe()
	target := filepath.Join(item.SkillsRoot, "fixture", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan toolpkg.CallOutcome, 1)
	go func() {
		done <- runner.executeTool(context.Background(), item, "run", "edit", "edit_file", map[string]any{"path": target, "old_string": "before", "new_string": "after"})
	}()
	card := nextApprovalEvent(t, eventCh).Data.(map[string]any)["args"].(map[string]any)
	if card["old_string"] != "before" || card["new_string"] != "after" || card["skill"] != "fixture" {
		t.Fatalf("edit card=%#v", card)
	}
	if err := runner.gate.Decide(item.ID, "edit", "approve"); err != nil {
		t.Fatal(err)
	}
	if outcome := <-done; !outcome.OK {
		t.Fatalf("edit outcome=%+v", outcome)
	}
	if body, _ := os.ReadFile(target); string(body) != "after\n" {
		t.Fatalf("edit body=%q", body)
	}
}

func TestSkillShellWriteRaisesSameCardWhenApprovalModeIsOff2vh(t *testing.T) {
	runner, item, eventCh, unsubscribe := skillApprovalFixture2vh(t)
	defer unsubscribe()
	target := filepath.Join(item.SkillsRoot, "fixture", "SKILL.md")
	command := `Set-Content -LiteralPath "` + target + `" -Value changed`
	done := make(chan toolpkg.CallOutcome, 1)
	go func() {
		done <- runner.executeTool(context.Background(), item, "run", "shell", "shell", map[string]any{"command": command})
	}()
	data := nextApprovalEvent(t, eventCh).Data.(map[string]any)
	card := data["args"].(map[string]any)
	if data["boundary_escape"] != false || card["command"] != command || card["skill"] != "fixture" {
		t.Fatalf("shell card=%#v", data)
	}
	if err := runner.gate.Decide(item.ID, "shell", "deny"); err != nil {
		t.Fatal(err)
	}
	if outcome := <-done; outcome.OK || outcome.Content != "error: call denied by user" {
		t.Fatalf("shell outcome=%+v", outcome)
	}
}

func TestConnectorChangeAlwaysUsesApprovalAndHotReloadsForRead(t *testing.T) {
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s", r.Method)
		}
		_, _ = w.Write([]byte(`{"tools":[]}`))
	}))
	defer broker.Close()
	bus := events.NewBus()
	eventCh, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	cfg := config.Defaults(t.TempDir())
	cfg.Approval.Mode = config.ApprovalModeOff
	serviceTool := toolpkg.NewCallService(nil)
	writes := 0
	serviceTool.SetConnectorWriter(func(change toolpkg.ConnectorChange) error {
		writes++
		cfg.Services[change.Name] = change.Service
		serviceTool.Configure(cfg)
		return nil
	})
	runner := NewRunner(bus, toolpkg.New(serviceTool), nil, nil, func() config.Config { return cfg })
	s := &session.Session{ID: "connector", ToolsEnabled: map[string]bool{"call_service": true}, Run: session.RunState{Status: "running"}}
	valid := map[string]any{"connector": map[string]any{"operation": "add", "entry": map[string]any{"name": "broker", "url": broker.URL, "kind": "http", "auth": "none", "allowed_methods": []any{"POST"}}}}

	done := make(chan toolpkg.CallOutcome, 1)
	bad := map[string]any{"connector": map[string]any{"operation": "add", "entry": map[string]any{"name": "broker", "url": broker.URL, "kind": "http", "auth": "pasted-secret", "allowed_methods": []any{"POST"}}}}
	go func() { done <- runner.executeTool(context.Background(), s, "run", "bad", "call_service", bad) }()
	badEvent := <-eventCh
	badArgs := badEvent.Data.(map[string]any)["args"].(map[string]any)
	if !strings.Contains(badArgs["validation_error"].(string), "auth") {
		t.Fatalf("bad card=%#v", badArgs)
	}
	if encoded, _ := json.Marshal(badArgs); strings.Contains(string(encoded), "pasted-secret") {
		t.Fatalf("bad card retained pasted secret: %s", encoded)
	}
	if err := runner.Gate().Decide(s.ID, "bad", "once"); err != nil {
		t.Fatal(err)
	}
	<-eventCh
	if outcome := <-done; outcome.OK || writes != 0 {
		t.Fatalf("bad outcome=%+v writes=%d", outcome, writes)
	}

	go func() { done <- runner.executeTool(context.Background(), s, "run", "reject", "call_service", valid) }()
	if event := <-eventCh; event.Type != events.ApprovalRequired {
		t.Fatalf("event=%s", event.Type)
	}
	if err := runner.Gate().Decide(s.ID, "reject", "deny"); err != nil {
		t.Fatal(err)
	}
	<-eventCh
	if outcome := <-done; outcome.OK || writes != 0 {
		t.Fatalf("rejected outcome=%+v writes=%d", outcome, writes)
	}

	go func() { done <- runner.executeTool(context.Background(), s, "run", "approve", "call_service", valid) }()
	<-eventCh
	if err := runner.Gate().Decide(s.ID, "approve", "once"); err != nil {
		t.Fatal(err)
	}
	<-eventCh
	if outcome := <-done; !outcome.OK || writes != 1 {
		t.Fatalf("approved outcome=%+v writes=%d", outcome, writes)
	}
	read := runner.executeTool(context.Background(), s, "run", "read", "call_service", map[string]any{"service": "broker", "method": "POST", "path": "rpc", "body": map[string]any{"method": "tools/list"}})
	if !read.OK || !strings.Contains(read.Content, `"tools"`) {
		t.Fatalf("read=%+v", read)
	}
}

func TestMCPApprovalCardShowsFullTargetAndPromptBytesStayStable2rm(t *testing.T) {
	for _, test := range []struct {
		name, target string
		entry        map[string]any
	}{
		{name: "address", target: "https://mcp.example.test/rpc", entry: map[string]any{"name": "remote", "url": "https://mcp.example.test/rpc", "kind": "mcp", "auth": "none"}},
		{name: "command", target: "fixture-server --stdio --label full-target", entry: map[string]any{"name": "local", "command": "fixture-server --stdio --label full-target", "kind": "mcp", "auth": "none"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			bus := events.NewBus()
			eventsSeen, unsubscribe := bus.Subscribe()
			defer unsubscribe()
			cfg := config.Defaults(t.TempDir())
			serviceTool := toolpkg.NewCallService(nil)
			serviceTool.SetConnectorWriter(func(toolpkg.ConnectorChange) error { return nil })
			runner := NewRunner(bus, toolpkg.New(serviceTool), nil, nil, func() config.Config { return cfg })
			s := &session.Session{ID: "mcp-card-" + test.name, ToolsEnabled: map[string]bool{"call_service": true}, Run: session.RunState{Status: "running"}}
			done := make(chan toolpkg.CallOutcome, 1)
			go func() {
				done <- runner.executeTool(context.Background(), s, "run", "card", "call_service", map[string]any{"connector": map[string]any{"operation": "add", "entry": test.entry}})
			}()
			event := <-eventsSeen
			encoded, _ := json.Marshal(event.Data)
			if event.Type != events.ApprovalRequired || !strings.Contains(string(encoded), test.target) {
				t.Fatalf("approval card=%s data=%s", event.Type, encoded)
			}
			if err := runner.Gate().Decide(s.ID, "card", "deny"); err != nil {
				t.Fatal(err)
			}
			<-eventsSeen
			<-done
		})
	}

	renderer := &PromptRenderer{text: "system tools={{tools}}"}
	connection := &config.Connection{}
	s := &session.Session{Workspace: "fixture"}
	httpRegistry := toolpkg.New(toolpkg.NewCallService(map[string]config.Service{"service": {Kind: "http", BaseURL: "https://example.test", Auth: "none", AllowedMethods: []string{"GET"}, TimeoutS: 5, MaxBodyKB: 64}}))
	mcpRegistry := toolpkg.New(toolpkg.NewCallService(map[string]config.Service{"service": {Kind: "mcp", BaseURL: "https://example.test", Auth: "none", AllowedMethods: []string{"POST"}, TimeoutS: 5, MaxBodyKB: 64}}))
	httpPrompt := renderer.Render(connection, s, httpRegistry.Names(map[string]bool{"call_service": true}), "")
	mcpPrompt := renderer.Render(connection, s, mcpRegistry.Names(map[string]bool{"call_service": true}), "")
	if httpPrompt != mcpPrompt {
		t.Fatalf("system prompt changed: http=%q mcp=%q", httpPrompt, mcpPrompt)
	}
}

func TestGateRequiredMatrix(t *testing.T) {
	tests := []struct {
		mode string
		want map[string]bool
	}{
		{mode: config.ApprovalModeOff, want: map[string]bool{}},
		{mode: config.ApprovalModeBoundaryOnly, want: map[string]bool{}},
		{mode: config.ApprovalModeMutating, want: map[string]bool{"write_file": true, "edit_file": true, "shell": true, "run_script": true, "process": true}},
		{mode: config.ApprovalModeAll, want: map[string]bool{"read_file": true, "write_file": true, "edit_file": true, "shell": true, "run_script": true, "process": true, "fetch_url": true}},
	}
	tools := []string{"read_file", "write_file", "edit_file", "shell", "run_script", "process", "fetch_url"}
	for _, test := range tests {
		t.Run(test.mode, func(t *testing.T) {
			cfg := config.Defaults(t.TempDir())
			cfg.Approval.Mode = test.mode
			gate := NewGate(nil, func() config.Config { return cfg })
			for _, name := range tools {
				if got := gate.required(name); got != test.want[name] {
					t.Errorf("required(%q)=%v, want %v", name, got, test.want[name])
				}
			}
		})
	}
}

func TestProcessStartAsksExactlyWhenShellAsks(t *testing.T) {
	for _, name := range []string{"shell", "process"} {
		t.Run(name, func(t *testing.T) {
			cfg := config.Defaults(t.TempDir())
			cfg.Approval.Mode = config.ApprovalModeMutating
			shell := toolpkg.NewShell(cfg.Shell)
			registry := toolpkg.New(shell, toolpkg.NewProcess(shell))
			runner := NewRunner(events.NewBus(), registry, nil, nil, func() config.Config { return cfg })
			stream, stop := runner.bus.Subscribe()
			defer stop()
			item := &session.Session{ID: name, Workspace: cfg.Workspace, ToolsEnabled: map[string]bool{name: true}, Run: session.RunState{Status: "running", RunID: "run"}}
			args := map[string]any{"command": "Write-Output ok"}
			if name == "process" {
				args["action"] = "start"
			}
			done := make(chan toolpkg.CallOutcome, 1)
			go func() { done <- runner.executeTool(context.Background(), item, "run", "call", name, args) }()
			event := <-stream
			if event.Type != events.ApprovalRequired {
				t.Fatalf("event=%s", event.Type)
			}
			if err := runner.Gate().Decide(item.ID, "call", "deny"); err != nil {
				t.Fatal(err)
			}
			<-stream
			if outcome := <-done; outcome.OK || !strings.Contains(outcome.Content, "denied") {
				t.Fatalf("outcome=%+v", outcome)
			}
		})
	}
}

func TestCallServiceApprovalPolicy2rh(t *testing.T) {
	var calls atomic.Int32
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer service.Close()
	makeRunner := func(mode string, confirm, unattended bool) *Runner {
		cfg := config.Defaults(t.TempDir())
		cfg.Approval.Mode = mode
		_ = unattended
		cfg.Services["depot"] = config.Service{BaseURL: service.URL, Auth: "none", AllowedMethods: []string{"GET", "POST"}, TimeoutS: 5, MaxBodyKB: 64, RequireConfirmation: confirm}
		return NewRunner(events.NewBus(), toolpkg.New(toolpkg.NewCallService(cfg.Services)), nil, nil, func() config.Config { return cfg })
	}
	runCard := func(mode, method, decision string, confirm bool) toolpkg.CallOutcome {
		runner := makeRunner(mode, confirm, false)
		stream, stop := runner.bus.Subscribe()
		defer stop()
		item := &session.Session{ID: method + decision, Role: "b", ToolsEnabled: map[string]bool{"call_service": true}, Run: session.RunState{Status: "running"}}
		done := make(chan toolpkg.CallOutcome, 1)
		go func() {
			done <- runner.executeTool(context.Background(), item, "run", "call", "call_service", map[string]any{"service": "depot", "method": method, "path": "check"})
		}()
		event := <-stream
		data := event.Data.(map[string]any)
		if event.Type != events.ApprovalRequired || data["name"] != "call_service" || data["args"].(map[string]any)["service"] != "depot" || calls.Load() != 0 {
			t.Fatalf("event=%+v calls=%d", event, calls.Load())
		}
		if human := data["human"].(events.HumanNotice); !strings.Contains(human.Happened, "Service depot "+method+" check") {
			t.Fatalf("human=%+v", human)
		}
		if err := runner.Gate().Decide(item.ID, "call", decision); err != nil {
			t.Fatal(err)
		}
		<-stream
		return <-done
	}
	if outcome := runCard(config.ApprovalModeBoundaryOnly, "GET", "once", true); !outcome.OK || calls.Load() != 1 {
		t.Fatalf("approved=%+v calls=%d", outcome, calls.Load())
	}
	calls.Store(0)
	if outcome := runCard(config.ApprovalModeBoundaryOnly, "GET", "deny", true); outcome.OK || !strings.Contains(outcome.Content, "denied") || calls.Load() != 0 {
		t.Fatalf("denied=%+v calls=%d", outcome, calls.Load())
	}
	if outcome := runCard(config.ApprovalModeMutating, "POST", "once", false); !outcome.OK {
		t.Fatalf("post=%+v", outcome)
	}
	calls.Store(0)
	runner := makeRunner(config.ApprovalModeMutating, false, false)
	item := &session.Session{ID: "get", Role: "b", ToolsEnabled: map[string]bool{"call_service": true}}
	if outcome := runner.executeTool(context.Background(), item, "run", "get", "call_service", map[string]any{"service": "depot", "method": "GET", "path": "check"}); !outcome.OK || calls.Load() != 1 {
		t.Fatalf("get=%+v calls=%d", outcome, calls.Load())
	}
	calls.Store(0)
	if outcome := runCard(config.ApprovalModeAll, "GET", "once", false); !outcome.OK {
		t.Fatalf("all=%+v", outcome)
	}
	for _, unattended := range []*session.Session{{ID: "worker", Role: "c", Unattended: true}, {ID: "scheduled", Role: "b", Origin: "scheduled", Unattended: true}} {
		calls.Store(0)
		runner := makeRunner(config.ApprovalModeBoundaryOnly, true, unattended.Role == "c")
		unattended.ToolsEnabled = map[string]bool{"call_service": true}
		outcome := runner.executeTool(context.Background(), unattended, "run", "call", "call_service", map[string]any{"service": "depot", "method": "GET", "path": "check"})
		if outcome.OK || calls.Load() != 0 || len(unattended.BoundaryHits()) != 1 {
			t.Fatalf("%s outcome=%+v calls=%d hits=%v", unattended.ID, outcome, calls.Load(), unattended.BoundaryHits())
		}
	}
}

func TestCanceledApprovalPublishesDismissedDecision(t *testing.T) {
	bus := events.NewBus()
	eventCh, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	cfg := config.Defaults(t.TempDir())
	gate := NewGate(bus, func() config.Config { return cfg })
	s := &session.Session{ID: "session", Run: session.RunState{Status: "running"}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := gate.WaitBoundaryDecision(ctx, s, "run", "call", "read_file.operator_override", map[string]any{"path": "outside.txt"})
		done <- err
	}()
	if event := <-eventCh; event.Type != events.ApprovalRequired {
		t.Fatalf("first event=%s", event.Type)
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("canceled wait returned nil")
	}
	decided := <-eventCh
	if decided.Type != events.ApprovalDecided || decided.Data.(map[string]any)["decision"] != "dismissed" {
		t.Fatalf("decision=%#v", decided)
	}
}

func TestAskPausesForTypedOrChoiceAnswerAndNeverPausesUnattended2rj(t *testing.T) {
	for _, answer := range []string{"typed answer", "Second"} {
		bus := events.NewBus()
		stream, stop := bus.Subscribe()
		cfg := config.Defaults(t.TempDir())
		runner := NewRunner(bus, toolpkg.New(toolpkg.NewAsk()), nil, nil, func() config.Config { return cfg })
		s := &session.Session{ID: strings.ReplaceAll(answer, " ", "-"), Role: "b", ToolsEnabled: map[string]bool{"ask": true}, Run: session.RunState{Status: "running"}}
		done := make(chan toolpkg.CallOutcome, 1)
		go func() {
			done <- runner.executeTool(context.Background(), s, "run", "ask-1", "ask", map[string]any{"question": "Pick one", "choices": []any{"First", "Second"}})
		}()
		event := <-stream
		data := event.Data.(map[string]any)
		if event.Type != events.ApprovalRequired || data["kind"] != "question" || data["question"] != "Pick one" {
			t.Fatalf("question event=%+v", event)
		}
		if err := runner.Gate().AnswerQuestion(s.ID, "ask-1", answer); err != nil {
			t.Fatal(err)
		}
		<-stream
		if outcome := <-done; !outcome.OK || outcome.Content != answer {
			t.Fatalf("answer=%q outcome=%+v", answer, outcome)
		}
		stop()
	}
	for _, s := range []*session.Session{{ID: "worker", Role: "c", Unattended: true}, {ID: "scheduled", Role: "b", Origin: "scheduled", Unattended: true}} {
		cfg := config.Defaults(t.TempDir())
		runner := NewRunner(events.NewBus(), toolpkg.New(toolpkg.NewAsk()), nil, nil, func() config.Config { return cfg })
		s.ToolsEnabled = map[string]bool{"ask": true}
		outcome := runner.executeTool(context.Background(), s, "run", "ask-1", "ask", map[string]any{"question": "Pick one"})
		if !outcome.OK || outcome.Content != "No one is here to answer. Decide yourself and say what you decided." || len(s.BoundaryHits()) != 1 {
			t.Fatalf("%s outcome=%+v hits=%v", s.ID, outcome, s.BoundaryHits())
		}
	}
}

func TestLatestTodoListAppearsOnceInTheNextModelContext2rj(t *testing.T) {
	records := []events.Message{
		{ID: "old", Role: "assistant", ToolCalls: []events.ToolCall{{ID: "one", Name: "todo", Arguments: `{"items":[{"text":"old","status":"done"}]}`}}},
		{ID: "new", Role: "assistant", ToolCalls: []events.ToolCall{{ID: "two", Name: "todo", Arguments: `{"items":[{"text":"current","status":"doing"}]}`}}},
	}
	filtered := latestTodoOnly(records)
	if filtered[0].ToolCalls[0].Arguments != `{}` || !strings.Contains(filtered[1].ToolCalls[0].Arguments, "current") {
		t.Fatalf("filtered=%+v", filtered)
	}
	if !strings.Contains(records[0].ToolCalls[0].Arguments, "old") {
		t.Fatal("durable records were mutated")
	}
}

func TestNewApprovalSupersedesExistingSessionDecision(t *testing.T) {
	bus := events.NewBus()
	eventCh, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	cfg := config.Defaults(t.TempDir())
	gate := NewGate(bus, func() config.Config { return cfg })
	s := &session.Session{ID: "session", Run: session.RunState{Status: "running"}}
	var waits sync.WaitGroup
	waits.Add(2)
	type result struct {
		decision string
		err      error
	}
	results := make(chan result, 2)
	go func() {
		defer waits.Done()
		decision, err := gate.WaitBoundaryDecision(context.Background(), s, "run", "first", "read_file.operator_override", map[string]any{})
		results <- result{decision: decision, err: err}
	}()
	if event := <-eventCh; event.Type != events.ApprovalRequired {
		t.Fatalf("first event=%s", event.Type)
	}
	go func() {
		defer waits.Done()
		decision, err := gate.WaitBoundaryDecision(context.Background(), s, "run", "second", "shell.operator_override", map[string]any{})
		results <- result{decision: decision, err: err}
	}()
	superseded, required := <-eventCh, <-eventCh
	if superseded.Type != events.ApprovalDecided || superseded.Data.(map[string]any)["decision"] != "superseded" || required.Type != events.ApprovalRequired || required.Data.(map[string]any)["call_id"] != "second" {
		t.Fatalf("events=%#v %#v", superseded, required)
	}
	if err := gate.Decide(s.ID, "second", "deny"); err != nil {
		t.Fatal(err)
	}
	<-eventCh
	waits.Wait()
	close(results)
	values := map[string]result{}
	for value := range results {
		values[value.decision] = value
	}
	if values["superseded"].err == nil || values["deny"].err != nil {
		t.Fatalf("decisions=%v", values)
	}
}

func TestFileBoundaryAcceptsRunAndSessionButNotOperatorMode(t *testing.T) {
	for _, decision := range []string{"run", "session"} {
		t.Run(decision, func(t *testing.T) {
			bus := events.NewBus()
			eventCh, unsubscribe := bus.Subscribe()
			defer unsubscribe()
			cfg := config.Defaults(t.TempDir())
			gate := NewGate(bus, func() config.Config { return cfg })
			s := &session.Session{ID: "session", Run: session.RunState{Status: "running"}}
			done := make(chan string, 1)
			go func() {
				value, _ := gate.WaitBoundaryDecision(context.Background(), s, "run", "call", "write_file.operator_override", map[string]any{})
				done <- value
			}()
			<-eventCh
			if err := gate.Decide(s.ID, "call", "operator_mode"); err == nil {
				t.Fatal("file approval accepted operator mode")
			}
			if err := gate.Decide(s.ID, "call", decision); err != nil {
				t.Fatal(err)
			}
			if got := <-done; got != decision {
				t.Fatalf("decision=%q", got)
			}
		})
	}
}

func TestPolicyApprovalEventIsNotBoundaryEscape(t *testing.T) {
	bus := events.NewBus()
	eventCh, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	cfg := config.Defaults(t.TempDir())
	cfg.Approval.Mode = config.ApprovalModeMutating
	gate := NewGate(bus, func() config.Config { return cfg })
	store := cfg.Workspace + "/standing-grants.json"
	gate.SetStandingGrantStore(store)
	s := &session.Session{ID: "session", Run: session.RunState{Status: "running"}}
	done := make(chan bool, 1)
	go func() {
		approved, _ := gate.Wait(context.Background(), s, "run", "call", "write_file", map[string]any{"path": "file.txt"})
		done <- approved
	}()
	select {
	case event := <-eventCh:
		data, ok := event.Data.(map[string]any)
		if event.Type != events.ApprovalRequired || !ok || data["boundary_escape"] != false || data["standing_grant"] == nil {
			t.Fatalf("policy event=%#v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for policy approval")
	}
	if err := gate.Decide(s.ID, "call", "approve"); err != nil {
		t.Fatal(err)
	}
	select {
	case approved := <-done:
		if !approved {
			t.Fatal("policy approval did not approve")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for policy decision")
	}
	<-eventCh
	for i := 0; i < 3; i++ {
		if decision, _ := gate.WaitPolicyDecision(context.Background(), s, "run", "reuse", "write_file", map[string]any{"path": "file.txt"}); decision != "approve" {
			t.Fatal("standing grant did not answer reuse")
		}
	}
	gate = NewGate(bus, func() config.Config { return cfg })
	gate.SetStandingGrantStore(store)
	if decision, _ := gate.WaitPolicyDecision(context.Background(), &session.Session{ID: "new"}, "run", "restart", "write_file", map[string]any{"path": "file.txt"}); decision != "approve" {
		t.Fatal("grant did not survive restart and new chat")
	}
	ask := func(call, path string) {
		result := make(chan string, 1)
		go func() {
			decision, _ := gate.WaitPolicyDecision(context.Background(), s, "run", call, "write_file", map[string]any{"path": path})
			result <- decision
		}()
		if event := <-eventCh; event.Type != events.ApprovalRequired {
			t.Fatal("approval not raised")
		}
		if err := gate.Decide(s.ID, call, "deny"); err != nil {
			t.Fatal(err)
		}
		if <-result != "deny" {
			t.Fatal("denial lost")
		}
		<-eventCh
	}
	ask("adjacent", "sibling.txt")
	if err := gate.RevokeStandingGrant("folder:file.txt"); err != nil {
		t.Fatal(err)
	}
	ask("revoked", "file.txt")
}

func TestNonShellPolicyAcceptsChatScopeButNotRunOrOperatorMode(t *testing.T) {
	bus := events.NewBus()
	eventsCh, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	cfg := config.Defaults(t.TempDir())
	cfg.Approval.Mode = config.ApprovalModeAll
	gate := NewGate(bus, func() config.Config { return cfg })
	s := &session.Session{ID: "session", Run: session.RunState{Status: "running"}}
	done := make(chan bool, 1)
	go func() {
		approved, _ := gate.Wait(context.Background(), s, "run", "call", "write_file", map[string]any{"path": "file.txt"})
		done <- approved
	}()
	select {
	case <-eventsCh:
	case <-time.After(2 * time.Second):
		t.Fatal("approval not published")
	}
	called := false
	if err := gate.DecideWith(s.ID, "call", "operator_mode", func() { called = true }); err == nil {
		t.Fatal("operator_mode accepted for non-shell approval")
	}
	if called {
		t.Fatal("operator callback ran for non-shell approval")
	}
	if err := gate.Decide(s.ID, "call", "run"); err == nil {
		t.Fatal("run accepted for non-shell approval")
	}
	if err := gate.Decide(s.ID, "call", "session"); err != nil {
		t.Fatal(err)
	}
	if approved := <-done; !approved {
		t.Fatal("chat-scoped policy decision did not approve")
	}
}

func TestPolicyChatGrantLapsesWithSession(t *testing.T) {
	runner := &Runner{}
	runner.grantPolicyChat("session", "write_file")
	if !runner.hasPolicyChatGrant("session", "write_file") || runner.hasPolicyChatGrant("session", "read_file") {
		t.Fatal("policy chat grant did not remain tool-specific")
	}
	runner.LapseSessionGrants("session")
	if runner.hasPolicyChatGrant("session", "write_file") {
		t.Fatal("policy chat grant survived session close")
	}
}

func TestCycleDecisionPausesAndAcceptsOnlyContinueOrStop(t *testing.T) {
	bus := events.NewBus()
	eventsCh, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	cfg := config.Defaults(t.TempDir())
	cfg.Approval.Mode = config.ApprovalModeMutating
	gate := NewGate(bus, func() config.Config { return cfg })
	s := &session.Session{ID: "session", Run: session.RunState{Status: "running"}}
	done := make(chan string, 1)
	go func() {
		decision, _ := gate.WaitCycleDecision(context.Background(), s, "run", "cycle-card", map[string]any{"tool": "list_dir"})
		done <- decision
	}()
	event := <-eventsCh
	data := event.Data.(map[string]any)
	if event.Type != events.ApprovalRequired || data["kind"] != "cycle" || s.Snapshot().Run.Status != "paused" {
		t.Fatalf("event=%+v run=%+v", event, s.Snapshot().Run)
	}
	if err := gate.Decide(s.ID, "cycle-card", "session"); err == nil {
		t.Fatal("cycle accepted grant decision")
	}
	if err := gate.Decide(s.ID, "cycle-card", "continue"); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got != "continue" {
		t.Fatalf("decision=%q", got)
	}
}

func TestBoundaryOnlyRunRecordsCycleNoticeAndContinues2pi(t *testing.T) {
	var requests atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		turn := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		delta := map[string]any{"content": "done"}
		finish := "stop"
		if turn <= 2 {
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("repeat-%d", turn), "type": "function", "function": map[string]any{"name": "list_dir", "arguments": `{"path":"same"}`}}}}
			finish = "tool_calls"
		}
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 1}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer model.Close()

	cfg := config.Defaults(t.TempDir())
	// This test isolates the boundary-only automatic-continue path; threshold
	// behavior is covered independently at the default five and stored values.
	cfg.Run.CycleWindow = 2
	cfg.Context.Accounting = "estimated"
	connection := cfg.Connections[0]
	connection.ID, connection.BaseURL = "main", model.URL
	connection.Context.NCtx, connection.Context.ReserveOutput = 32768, 8192
	connection.Capabilities.Streaming, connection.Capabilities.ToolCalls = true, true
	connection.Capabilities.OverflowBehavior = "error"
	bus := events.NewBus()
	eventCh, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	runner := NewRunner(bus, toolpkg.New(delegateFixtureTool{name: "list_dir"}), &PromptRenderer{text: "system"}, func(id string) (*config.Connection, bool) { return &connection, id == connection.ID }, func() config.Config { return cfg })
	item := &session.Session{ID: "cycle-notice", ConnectionID: connection.ID, Workspace: t.TempDir(), Runnable: true, Run: session.RunState{Status: "running"}, ToolsEnabled: map[string]bool{"list_dir": true}, ToolCalls: map[string]int{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	if _, err := runner.AddUser(context.Background(), item, "repeat once"); err != nil {
		t.Fatal(err)
	}
	reason, detail, _ := runner.Run(context.Background(), item, "run-cycle")
	if reason != "done" {
		t.Fatalf("reason=%q detail=%q", reason, detail)
	}
	found := false
	for _, message := range item.MessagesCopy() {
		found = found || message.Content == "stopped a loop after 2 turns, continuing differently"
	}
	if !found {
		t.Fatal("cycle notice missing")
	}
	for draining := true; draining; {
		select {
		case event := <-eventCh:
			if event.Type == events.ApprovalRequired {
				t.Fatalf("card=%+v", event)
			}
		default:
			draining = false
		}
	}
}

func TestPendingApprovalCanBeAnsweredFromMailboxCallback(t *testing.T) {
	bus := events.NewBus()
	eventsCh, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	cfg := config.Defaults(t.TempDir())
	gate := NewGate(bus, func() config.Config { return cfg })
	gate.SetMailboxDecision(func(sessionID string) (string, error) {
		if sessionID != "session" {
			t.Fatalf("session=%q", sessionID)
		}
		return "session", nil
	})
	s := &session.Session{ID: "session", Run: session.RunState{Status: "running"}}
	done := make(chan string, 1)
	go func() {
		decision, _ := gate.WaitBoundaryDecision(context.Background(), s, "run", "mailbox-call", "read_file.operator_override", map[string]any{})
		done <- decision
	}()
	if event := <-eventsCh; event.Type != events.ApprovalRequired {
		t.Fatalf("first=%s", event.Type)
	}
	select {
	case decision := <-done:
		if decision != "session" {
			t.Fatalf("decision=%q", decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mailbox answer did not resume approval")
	}
}
