package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

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
		"Remember only a correction the user gave, a preference they stated, or a fact about their project you had to discover. Saved notes stay in recall, not this prompt. Recall first; one note per fact; give it a scope.",
		"Rules: work in the repo or folder the user names; ask when more than one could match; nothing named → this chat's folder.",
	} {
		if strings.Count(text, want) != 1 {
			t.Errorf("shipped prompt does not carry this sentence exactly once:\n%s", want)
		}
	}

	// The working-directory rule precedes durable-memory guidance and notes.
	resolutionAt := strings.Index(text, "Rules: work in the repo or folder")
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

func TestPromptCarriesOnlyTheSavedNoteCount2vi(t *testing.T) {
	renderer := &PromptRenderer{text: "system\n{{memory}}"}
	item := &session.Session{Workspace: t.TempDir(), MemoryBlock: "Notes from earlier sessions in this folder:\n- private fixture fact"}
	got := renderer.RenderSkillParts(&config.Connection{}, item, []string{"recall"}, "", "", item.WorkspaceMemory())
	const want = "This folder has 1 saved notes; recall reads them."
	if !strings.Contains(got, want) || strings.Contains(got, "private fixture fact") {
		t.Fatalf("prompt=%q, want count line only", got)
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
		tools.NewSearch(tools.NewGrep(cfg.Tools.Grep, cfg.Tools.ListDir), tools.NewGlob(cfg.Tools.FindFiles)), shell, tools.NewRemember(notes, bus), tools.NewRecall(notes, nil),
		fetch, tools.NewWebSearch(fetch, cfg.Tools.WebSearch), tools.NewRunScript(shell), tools.NewCallService(cfg.Services), tools.NewDelegate())
	item := &session.Session{ID: "word", ConnectionID: "main", Workspace: root, Runnable: true, Run: session.RunState{Status: "running"}, ToolsEnabled: map[string]bool{},
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
	// Item 2wd CHECK 3: the earlier-turns line names recall, and chat_history is gone.
	if !strings.Contains(sent, "use recall to search or read them") || strings.Contains(sent, "chat_history") {
		t.Fatalf("the earlier-turns line does not name recall alone: %s", sent)
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
	if third := run(big); !strings.Contains(third, "BEGIN a rule") || !strings.Contains(third, "cut at 2000 tokens; read the rest at") {
		t.Fatalf("over the bound the request lacks its counted prefix or pointer: %.300s", third[strings.Index(third, "REPOSITORY"):])
	}
}

func TestTrustedOutsideProjectRulesAndNotesReachTheNextRecordedRequest2tt(t *testing.T) {
	stub := newByteLimitStub(t, 1<<30)
	runner, item, _ := byteLimitRunner(t, stub)
	runner.prompt = &PromptRenderer{text: "system\n{{project}}\n{{memory}}"}
	repo := filepath.Join(t.TempDir(), "fixture-project")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("outside project fixture rule"), 0o600); err != nil {
		t.Fatal(err)
	}
	item.ProjectRouting = true
	item.ProjectAllowed = func(path string) bool { return session.SamePath(path, repo) }
	item.ProjectTouch = func(string) {
		loaded, err := workspace.LoadInstructions(repo, "")
		if err != nil {
			t.Fatal(err)
		}
		item.ProjectBlock, item.ProjectFiles, item.ProjectRoots = loaded.Block, loaded.Files, []string{repo}
		item.ProjectMemoryBlocks = []string{"outside project fixture note"}
	}
	item.Append(events.Message{ID: "u1", Role: "user", Content: "work in " + repo, Category: "history"})
	if reason, detail, _ := runner.Run(context.Background(), item, "r1"); reason != "done" {
		t.Fatalf("run=%s %s", reason, detail)
	}
	if !strings.Contains(stub.last, "outside project fixture rule") || strings.Contains(stub.last, "outside project fixture note") || !strings.Contains(stub.last, "This folder has 0 saved notes; recall reads them.") {
		t.Fatalf("request lacks project context: %s", stub.last)
	}
}
