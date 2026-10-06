package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/memory"
	"harness/internal/session"
	"harness/internal/tools"
	"harness/internal/workspace"
)

// Durable-memory and working-directory guidance have to be in the shipped
// prompt and byte-stable across requests within a session.
func TestShippedPromptCarriesMemoryAndResolutionGuidanceAndIsByteStable(t *testing.T) {
	shipped, err := os.ReadFile(filepath.Join("..", "..", "prompts", "system.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(shipped)

	for _, want := range []string{
		"Remember only a correction the user gave, a preference they stated, or a fact about their project you had to discover — avoidance of your own tools is never remembered. Recall first; one note per fact; give it a scope.",
		"Rules: resolve a named plan or repo from the user's words and work there; ask when more than one could match; nothing named → this chat's folder.",
	} {
		if strings.Count(text, want) != 1 {
			t.Errorf("shipped prompt does not carry this sentence exactly once:\n%s", want)
		}
	}

	// The working-directory rule precedes durable-memory guidance and notes.
	resolutionAt := strings.Index(text, "Rules: resolve a named plan")
	rememberAt := strings.Index(text, "Remember only a correction")
	memoryAt := strings.LastIndex(text, "{{memory}}")
	if !(resolutionAt >= 0 && resolutionAt < rememberAt && rememberAt < memoryAt) {
		t.Fatalf("sentence order is wrong: resolution=%d remember=%d memory=%d", resolutionAt, rememberAt, memoryAt)
	}

	root := t.TempDir()
	template := filepath.Join(root, "system.md")
	if err := os.WriteFile(template, shipped, 0o600); err != nil {
		t.Fatal(err)
	}
	renderer, err := LoadTemplate(template)
	if err != nil {
		t.Fatal(err)
	}
	item := &session.Session{Workspace: filepath.Join(root, "repo"), PlansRoot: filepath.Join(root, "plans")}
	connection := &config.Connection{}
	first := renderer.Render(connection, item, []string{"read_file", "remember", "recall"}, "")
	for attempt := 0; attempt < 5; attempt++ {
		if again := renderer.Render(connection, item, []string{"read_file", "remember", "recall"}, ""); again != first {
			t.Fatalf("prompt is not byte-stable across requests on attempt %d", attempt+1)
		}
	}
	for _, want := range []string{"Remember only a correction", "nothing named → this chat's folder"} {
		if !strings.Contains(first, want) {
			t.Errorf("rendered prompt lost %q", want)
		}
	}
	for _, removed := range []string{"repository allow-list", "File tools may use scratch and every listed plan repo", "offer to register it as a plan", "never search the user's connection"} {
		if strings.Contains(first, removed) {
			t.Errorf("rendered prompt retained removed reach guidance %q", removed)
		}
	}
}

// The prompt must not contradict the tool descriptions it sits beside.
func TestMemorySentenceAgreesWithTheRememberAndRecallDescriptions(t *testing.T) {
	shipped, err := os.ReadFile(filepath.Join("..", "..", "prompts", "system.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(shipped)
	if !strings.Contains(text, "Recall first") {
		t.Error("the sentence does not send the model to recall before writing, which is what the tool text already says")
	}
	if strings.Contains(text, "delete") || strings.Contains(text, "prune it by hand") {
		t.Error("the prompt implies the harness removes notes; it never does")
	}
}

func TestAgentAvoidanceNoteDoesNotPreventTheExistingOutsideFolderCard2p3(t *testing.T) {
	badNote := "STAY IN WORKSPACE: do not search outside it because the approval wedges"
	wantPath := filepath.Join(t.TempDir(), "redirected-downloads")
	priorResolver := resolveNamedPath
	resolveNamedPath = func(value string) string {
		if value == "my Downloads" {
			return wantPath
		}
		return priorResolver(value)
	}
	defer func() { resolveNamedPath = priorResolver }()
	requestBody := make(chan string, 1)
	var requests atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		encoded, _ := json.Marshal(body)
		if requests.Add(1) == 1 {
			requestBody <- string(encoded)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests.Load() > 1 {
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "done"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 1}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		delta := map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": "list-downloads", "type": "function",
			"function": map[string]any{"name": "list_dir", "arguments": `{"path":"my Downloads"}`},
		}}}
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": "tool_calls"}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer model.Close()

	root := t.TempDir()
	cfg := config.Defaults(root)
	cfg.Context.Accounting = "estimated"
	connection := cfg.Connections[0]
	connection.ID, connection.BaseURL = "main", model.URL
	connection.Context.NCtx, connection.Context.ReserveOutput = 32768, 8192
	connection.Capabilities.Streaming, connection.Capabilities.ToolCalls = true, true
	connection.Capabilities.OverflowBehavior = "error"
	cfg.Connections = []config.Connection{connection}
	bus := events.NewBus()
	eventCh, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	tool := &overrideTestTool{name: "list_dir"}
	runner := NewRunner(bus, tools.New(tool), &PromptRenderer{text: "system\n{{memory}}"}, func(id string) (*config.Connection, bool) { return &connection, id == connection.ID }, func() config.Config { return cfg })
	item := &session.Session{
		ID: "colleague", ConnectionID: connection.ID, Workspace: root, Runnable: true,
		Run: session.RunState{Status: "running", MaxTurns: 2}, ToolsEnabled: map[string]bool{"list_dir": true},
		ToolCalls: map[string]int{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{},
		AgentMemoryBlock: "Notes about how this agent works with the user:\n- " + badNote + "  [about-agent: yes]",
	}
	if _, err := runner.AddUser(context.Background(), item, "how many folders are in my Downloads"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _, _, _ = runner.Run(context.Background(), item, "run-memory"); close(done) }()
	select {
	case body := <-requestBody:
		if strings.Contains(body, badNote) {
			t.Fatal("the existing avoidance note reached the model")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the model stub received no request")
	}
	for {
		select {
		case event := <-eventCh:
			if event.Type != events.ApprovalRequired {
				continue
			}
			data := event.Data.(map[string]any)
			if data["name"] != "list_dir.operator_override" {
				t.Fatalf("unexpected card: %+v", data)
			}
			if data["args"].(map[string]any)["path"] != wantPath {
				t.Fatalf("card path=%q want=%q", data["args"].(map[string]any)["path"], wantPath)
			}
			if err := runner.Gate().Decide(item.ID, data["call_id"].(string), "approve"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("run did not finish after the redirected tool call")
			}
			if tool.lastArgs["path"] != wantPath {
				t.Fatalf("tool path=%q want=%q", tool.lastArgs["path"], wantPath)
			}
			return
		case <-time.After(5 * time.Second):
			t.Fatal("the existing outside-folder card was not raised")
		}
	}
}

// Item 2pz CHECK 3: what a chat sends -- the shipped prompt and all model tools'
// descriptions -- never calls the person the operator.
func TestAChatRequestNeverSaysOperator2pz(t *testing.T) {
	renderer, err := LoadTemplate(filepath.Join("..", "..", "prompts", "system.md"))
	if err != nil {
		t.Fatal(err)
	}
	body := make(chan string, 1)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body <- string(raw)
	}))
	defer model.Close()
	root, cfg, bus := t.TempDir(), config.Defaults(t.TempDir()), events.NewBus()
	connection := config.Connection{ID: "main", BaseURL: model.URL, Model: "m", Context: config.Context{NCtx: 131072, ReserveOutput: 8192}}
	connection.Capabilities.Streaming, connection.Capabilities.ToolCalls = true, true
	files, notes := tools.NewFileCoordinator(nil, nil, bus), memory.New(root, func() config.Config { return cfg }, nil)
	shell, fetch := tools.NewShell(cfg.Shell), tools.NewFetch(cfg.Tools.Fetch)
	registry := tools.New(tools.NewReadFile(cfg.Tools.ReadFile), tools.NewListDir(cfg.Tools.ListDir), tools.NewWriteFile(files), tools.NewEditFile(files),
		tools.NewSearch(tools.NewGrep(cfg.Tools.Grep, cfg.Tools.ListDir), tools.NewGlob(cfg.Tools.FindFiles)), shell, tools.NewRemember(notes, bus), tools.NewRecall(notes),
		fetch, tools.NewWebSearch(fetch, cfg.Tools.WebSearch), tools.NewRunScript(shell), tools.NewCallService(cfg.Services), tools.NewDelegate())
	item := &session.Session{ID: "word", ConnectionID: "main", Workspace: root, Runnable: true, Run: session.RunState{Status: "running", MaxTurns: 1}, ToolsEnabled: map[string]bool{},
		ToolCalls: map[string]int{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	for name := range registry.AllSchemas() {
		item.ToolsEnabled[name] = true
	}
	runner := NewRunner(bus, registry, renderer, func(string) (*config.Connection, bool) { return &connection, true }, func() config.Config { return cfg })
	_, _ = runner.AddUser(context.Background(), item, "what is in this folder?")
	go func() { _, _, _ = runner.Run(context.Background(), item, "run-word") }()
	sent := <-body
	if !strings.Contains(sent, `"delegate"`) || regexp.MustCompile(`(?i)(^|[^\w-])operator('s)?([^\w-]|$)`).MatchString(sent) {
		t.Fatalf("the request lacks the tools or says operator: %s", sent)
	}
}

// Item 2q0 CHECKS 2 and 3: repository instructions changed between two runs of one chat
// reach the second run's request, and a file over the bound is a pointer, never a cut copy.
func TestRepositoryInstructionsAreCurrentAndWholeOnEveryRun2q0(t *testing.T) {
	stub := newByteLimitStub(t, 1<<30)
	runner, item, _ := byteLimitRunner(t, stub)
	runner.prompt = &PromptRenderer{text: "system\n{{project}}"}
	file := filepath.Join(item.Workspace, "AGENTS.md")
	run := func(text string) string {
		if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		item.Append(events.Message{ID: fmt.Sprintf("u%d", len(stub.sizes)), Role: "user", Content: "go", Category: "history"})
		if reason, detail, _ := runner.Run(context.Background(), item, fmt.Sprintf("r%d", len(stub.sizes))); reason != "done" {
			t.Fatalf("run: %s %s", reason, detail)
		}
		return stub.last
	}
	if err := os.WriteFile(file, []byte("Rule one."), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := workspace.LoadInstructions(item.Workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	item.ProjectFiles, item.ProjectBlock = loaded.Files, loaded.Block
	if first := run("Rule one."); !strings.Contains(first, "Rule one.") {
		t.Fatal("the first run lacks the instructions")
	}
	if second := run("Rule one. Rule two, written between runs."); !strings.Contains(second, "Rule two, written between runs.") {
		t.Fatal("the second run carries the stale instructions")
	}
	big := "BEGIN " + strings.Repeat("a rule of the repository. ", workspace.InstructionLimit/26+10)
	if third := run(big); strings.Contains(third, "BEGIN a rule") || !strings.Contains(third, "over the 16384-byte limit") {
		t.Fatalf("over the bound the request carries a partial copy or no pointer: %.300s", third[strings.Index(third, "REPOSITORY"):])
	}
}
