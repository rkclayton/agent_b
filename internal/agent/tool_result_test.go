package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
)

type parallelReadFixture struct {
	active, maximum *atomic.Int32
	delay           time.Duration
	state           *string
	mu              *sync.Mutex
}

func (*parallelReadFixture) Name() string           { return "read_file" }
func (*parallelReadFixture) Description() string    { return "fixture" }
func (*parallelReadFixture) Schema() map[string]any { return map[string]any{"type": "object"} }
func (f *parallelReadFixture) Call(ctx context.Context, _ *session.Session, _ map[string]any) (string, error) {
	n := f.active.Add(1)
	for {
		old := f.maximum.Load()
		if n <= old || f.maximum.CompareAndSwap(old, n) {
			break
		}
	}
	defer f.active.Add(-1)
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(f.delay):
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.state, nil
}

type parallelWriteFixture struct {
	state *string
	mu    *sync.Mutex
}

func (*parallelWriteFixture) Name() string           { return "write_file" }
func (*parallelWriteFixture) Description() string    { return "fixture" }
func (*parallelWriteFixture) Schema() map[string]any { return map[string]any{"type": "object"} }
func (f *parallelWriteFixture) Call(_ context.Context, _ *session.Session, _ map[string]any) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	*f.state = "after"
	return "after", nil
}

func TestReadGroupsRunFourAtOnceAndChangesStayOrdered2rs(t *testing.T) {
	var active, maximum atomic.Int32
	var mu sync.Mutex
	state := "before"
	read := &parallelReadFixture{active: &active, maximum: &maximum, delay: 200 * time.Millisecond, state: &state, mu: &mu}
	registry := tools.New(read, &parallelWriteFixture{state: &state, mu: &mu})
	cfg := config.Defaults(t.TempDir())
	runner := NewRunner(events.NewBus(), registry, nil, func(string) (*config.Connection, bool) { return &cfg.Connections[0], true }, func() config.Config { return cfg })
	s := &session.Session{ID: "parallel", Workspace: t.TempDir(), ToolsEnabled: map[string]bool{"read_file": true, "write_file": true}, ToolCalls: map[string]int{}}
	x := &runState{r: runner, ctx: context.Background(), s: s, runID: "run", turn: 1}
	for i := 0; i < 8; i++ {
		x.results = append(x.results, runToolResult{call: events.ToolCall{ID: fmt.Sprint(i), Name: "read_file"}, durableCall: events.ToolCall{ID: fmt.Sprint(i), Name: "read_file"}, args: map[string]any{}})
	}
	start := time.Now()
	x.runRawTools()
	elapsed := time.Since(start)
	if elapsed >= 600*time.Millisecond || maximum.Load() != 4 {
		t.Fatalf("elapsed=%s max=%d", elapsed, maximum.Load())
	}
	x.results = []runToolResult{{call: events.ToolCall{ID: "a", Name: "read_file"}, args: map[string]any{}}, {call: events.ToolCall{ID: "b", Name: "write_file"}, args: map[string]any{}}, {call: events.ToolCall{ID: "c", Name: "read_file"}, args: map[string]any{}}}
	x.runRawTools()
	if x.results[0].content != "before" || x.results[2].content != "after" {
		t.Fatalf("results=%q,%q,%q", x.results[0].content, x.results[1].content, x.results[2].content)
	}
}

func TestStopCancelsEveryParallelRead2rs(t *testing.T) {
	var active, maximum atomic.Int32
	var mu sync.Mutex
	state := ""
	read := &parallelReadFixture{active: &active, maximum: &maximum, delay: time.Minute, state: &state, mu: &mu}
	registry := tools.New(read)
	cfg := config.Defaults(t.TempDir())
	runner := NewRunner(events.NewBus(), registry, nil, func(string) (*config.Connection, bool) { return &cfg.Connections[0], true }, func() config.Config { return cfg })
	ctx, cancel := context.WithCancel(context.Background())
	x := &runState{r: runner, ctx: ctx, s: &session.Session{ID: "stop", ToolsEnabled: map[string]bool{"read_file": true}}, runID: "run", turn: 1}
	for i := 0; i < 3; i++ {
		x.results = append(x.results, runToolResult{call: events.ToolCall{ID: fmt.Sprint(i), Name: "read_file"}, args: map[string]any{}})
	}
	done := make(chan struct{})
	go func() { x.runRawTools(); close(done) }()
	for active.Load() != 3 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("parallel calls survived Stop")
	}
	if active.Load() != 0 {
		t.Fatalf("active=%d", active.Load())
	}
}

func TestHookProcess2rn(t *testing.T) {
	mode := os.Getenv("AGENTB_HOOK_HELPER")
	if mode == "" {
		return
	}
	var input map[string]any
	_ = json.NewDecoder(os.Stdin).Decode(&input)
	if input["event"] == "" || input["chat_id"] == "" {
		os.Exit(9)
	}
	if input["event"] == "run_end" && input["stop_reason"] == nil {
		os.Exit(9)
	}
	if input["event"] != "run_end" && (input["tool"] == nil || input["arguments"] == nil) {
		os.Exit(9)
	}
	if input["event"] == "after_tool" && input["success"] == nil {
		os.Exit(9)
	}
	switch mode {
	case "refuse":
		fmt.Fprint(os.Stderr, strings.Repeat("blocked", 100))
		os.Exit(2)
	case "after":
		fmt.Print(strings.Repeat("x", 2105))
	case "hang":
		time.Sleep(5 * time.Second)
	}
	os.Exit(0)
}

func hookFixtureCommand(mode string) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`$env:AGENTB_HOOK_HELPER='%s'; & '%s' '-test.run=TestHookProcess2rn'; exit $LASTEXITCODE`, mode, strings.ReplaceAll(os.Args[0], "'", "''"))
	}
	return fmt.Sprintf(`AGENTB_HOOK_HELPER=%s %q -test.run=TestHookProcess2rn`, mode, os.Args[0])
}

func TestBeforeToolHookRefusesOrAllowsShell2rn(t *testing.T) {
	for _, mode := range []string{"refuse", "allow"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(root, "ran.txt")
			cfg := config.Defaults(root)
			cfg.Context.Accounting, cfg.Shell.ServiceAccount.Enabled = "estimated", false
			if runtime.GOOS != "windows" {
				cfg.Shell.Command = []string{"sh", "-c"}
			}
			cfg.Hooks = []config.Hook{{Event: "before_tool", Tool: "shell", Command: hookFixtureCommand(mode), TimeoutS: 3}, {Event: "run_end", Command: hookFixtureCommand("allow"), TimeoutS: 3}}
			command := fmt.Sprintf(`Set-Content -LiteralPath '%s' -Value ran`, strings.ReplaceAll(marker, "'", "''"))
			if runtime.GOOS != "windows" {
				command = fmt.Sprintf(`printf ran > %q`, marker)
			}
			calls := 0
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				var body struct {
					Messages []struct{ Role, Content string } `json:"messages"`
				}
				_ = json.NewDecoder(request.Body).Decode(&body)
				calls++
				if calls == 1 {
					arguments, _ := json.Marshal(map[string]any{"command": command})
					writeStreamChunk(t, w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "shell-1", "type": "function", "function": map[string]any{"name": "shell", "arguments": string(arguments)}}}}, "finish_reason": "tool_calls"}}})
					return
				}
				if mode == "refuse" && !strings.Contains(body.Messages[len(body.Messages)-1].Content, "hook refused shell: blocked") {
					t.Errorf("next request=%#v", body.Messages)
				}
				writeStreamChunk(t, w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "done"}, "finish_reason": "stop"}}})
			}))
			defer model.Close()
			connection := cfg.Connections[0]
			connection.ID, connection.BaseURL, connection.Model = "main", model.URL, "fake"
			connection.Context.NCtx, connection.Context.ReserveOutput = 32768, 8192
			connection.Capabilities.Streaming, connection.Capabilities.ToolCalls = true, true
			cfg.Connections = []config.Connection{connection}
			runner := NewRunner(events.NewBus(), tools.New(tools.NewShell(cfg.Shell)), &PromptRenderer{text: "system"}, func(string) (*config.Connection, bool) { return &connection, true }, func() config.Config { return cfg })
			item := &session.Session{ID: "hook", ConnectionID: "main", Workspace: root, Runnable: true, ToolsEnabled: map[string]bool{"shell": true}, ToolCalls: map[string]int{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
			item.Append(events.Message{ID: "u", Role: "user", Category: "history", Content: "run"})
			if reason, _, _ := runner.Run(context.Background(), item, "run"); reason != "done" {
				t.Fatalf("reason=%s", reason)
			}
			_, err := os.Stat(marker)
			if (mode == "allow") != (err == nil) {
				t.Fatalf("mode=%s marker error=%v", mode, err)
			}
		})
	}
}

func TestHookOutputTimeoutIsolationJournalAndNoHookCost2rn(t *testing.T) {
	cfg := config.Defaults(t.TempDir())
	if runtime.GOOS != "windows" {
		cfg.Shell.Command = []string{"sh", "-c"}
	}
	bus := events.NewBus()
	runner := &Runner{bus: bus, cfg: func() config.Config { return cfg }}
	s := &session.Session{ID: "chat"}
	after := runner.runHooks(context.Background(), s, "run", []config.Hook{{Event: "after_tool", Tool: "*", Command: hookFixtureCommand("after")}}, hookInput{event: "after_tool", tool: "shell", arguments: map[string]any{"secret": "never-journal"}, success: true})
	if len(after.output) != 1 || !strings.HasPrefix(after.output[0], "[hook output; cut to 2,000 characters]\n") || len([]rune(strings.TrimPrefix(after.output[0], "[hook output; cut to 2,000 characters]\n"))) != 2000 {
		t.Fatalf("after=%#v", after)
	}
	seen, cancel := bus.Subscribe()
	defer cancel()
	start := time.Now()
	runner.runHooks(context.Background(), s, "run", []config.Hook{{Event: "before_tool", Tool: "*", Command: hookFixtureCommand("hang"), TimeoutS: 1}}, hookInput{event: "before_tool", tool: "shell", arguments: map[string]any{}})
	if time.Since(start) > 3*time.Second {
		t.Fatal("timed-out hook was not killed")
	}
	event := <-seen
	encoded, _ := json.Marshal(event.Data)
	if event.Type != events.HookRan || strings.Contains(string(encoded), "never-journal") || !strings.Contains(string(encoded), `"timed_out":true`) {
		t.Fatalf("journal=%s %s", event.Type, encoded)
	}
	if allocs := testing.AllocsPerRun(1000, func() { runner.runHooks(context.Background(), s, "run", nil, hookInput{}) }); allocs != 0 {
		t.Fatalf("no-hook allocations=%g", allocs)
	}
	started := time.Now()
	for range 1000 {
		runner.runHooks(context.Background(), s, "run", nil, hookInput{})
	}
	t.Logf("1,000 no-hook calls=%s", time.Since(started))
	for _, relative := range []string{"AGENTS.md", "skill/SKILL.md", "plan/work.md"} {
		t.Run(relative, func(t *testing.T) {
			path := filepath.Join(cfg.Workspace, filepath.FromSlash(relative))
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			_ = os.WriteFile(path, []byte(`hooks: [{"event":"before_tool","command":"exit 2"}]`), 0o600)
			if got := runner.runHooks(context.Background(), s, "run", nil, hookInput{event: "before_tool", tool: "shell"}); got.refusal != "" || len(got.output) != 0 {
				t.Fatalf("repository content defined a hook: %#v", got)
			}
		})
	}
}

func TestNonzeroShellExitProducesFailedToolResultEvent(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	cfg.Shell.ServiceAccount.Enabled = false
	command := "printf ui-stderr-marker >&2; exit 9"
	if runtime.GOOS == "windows" {
		command = "[Console]::Error.WriteLine('ui-stderr-marker'); exit 9"
	} else {
		cfg.Shell.Command = []string{"sh", "-c"}
	}
	runner := &Runner{bus: events.NewBus(), tools: tools.New(tools.NewShell(cfg.Shell)), cfg: func() config.Config { return cfg }}
	runner.gate = NewGate(runner.bus, runner.cfg)
	item := &session.Session{ID: "test", Workspace: root, ToolsEnabled: map[string]bool{"shell": true}}
	outcome := runner.executeTool(context.Background(), item, "run", "call", "shell", map[string]any{"command": command})
	if outcome.OK || !strings.Contains(outcome.Content, "exit=9") || !strings.Contains(outcome.Content, "ui-stderr-marker") {
		t.Fatalf("model outcome=%+v", outcome)
	}
	data := toolResultEventData(1, "call", "shell", outcome.Content, outcome.OK, outcome.OperatorContext, outcome.Untrusted, 1, 1, outcome.Metadata)
	if data["ok"] != false || !strings.Contains(data["preview"].(string), "exit=9") {
		t.Fatalf("UI event data=%#v", data)
	}
}

func TestBrowserScreenshotResultNamesOneModelAttachment2qt(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "screenshots", "page.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, smallPNG(t), 0o600); err != nil {
		t.Fatal(err)
	}
	metadata := browserScreenshotMetadata(&session.Session{Workspace: workspace}, "shell", "browser screenshot: screenshots/page.png\n")
	file, _ := metadata["file"].(map[string]any)
	if file["path"] != "screenshots/page.png" || file["bytes"].(int64) <= 0 {
		t.Fatalf("metadata=%#v", metadata)
	}
	if browserScreenshotMetadata(&session.Session{Workspace: workspace}, "shell", "browser screenshot: ../page.png") != nil {
		t.Fatal("outside screenshot accepted")
	}
}

func TestRememberRefusesImmediateToolResultEchoAndAcceptsDurableFact2kt(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	cfg.Shell.ServiceAccount.Enabled = false
	remember := &unprovisionedTool{name: "remember"}
	runner := &Runner{bus: events.NewBus(), tools: tools.New(remember), cfg: func() config.Config { return cfg }}
	runner.gate = NewGate(runner.bus, runner.cfg)
	item := &session.Session{ID: "memory", Workspace: root, ToolsEnabled: map[string]bool{"remember": true}}
	item.Append(events.Message{Role: "tool", Name: "shell", Content: "PING google.com completed with exit=0"})
	echo := runner.executeTool(context.Background(), item, "run", "echo", "remember", map[string]any{"note": "Pinged google.com with exit=0"})
	if echo.OK || !strings.Contains(echo.Content, "immediately preceding tool result") || remember.normal != 0 {
		t.Fatalf("echo=%+v calls=%d", echo, remember.normal)
	}
	item.Append(events.Message{Role: "user", Content: "Remember that I prefer concise release reports."})
	durable := runner.executeTool(context.Background(), item, "run", "durable", "remember", map[string]any{"note": "The operator prefers concise release reports."})
	if !durable.OK || remember.normal != 1 {
		t.Fatalf("durable=%+v calls=%d", durable, remember.normal)
	}
}

func TestProducedFileMetadataTracksOnlyJailedFileTools(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "reports", "done.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("done"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &session.Session{Workspace: workspace}
	for _, name := range []string{"write_file", "edit_file"} {
		metadata := producedFileMetadata(s, name, map[string]any{"path": "reports/done.txt"})
		file, _ := metadata["file"].(map[string]any)
		if file["path"] != "reports/done.txt" || file["bytes"] != int64(4) {
			t.Fatalf("%s metadata=%#v", name, metadata)
		}
	}
	planRoot := t.TempDir()
	planDir := filepath.Join(planRoot, "stable")
	if err := os.MkdirAll(filepath.Join(planDir, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "reports", "done.txt"), []byte("plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &session.Session{Role: "d", Workspace: workspace, PlansRoot: planRoot, PlanID: "stable", PlanDir: planDir}
	dMetadata := producedFileMetadata(d, "write_file", map[string]any{"path": "reports/done.txt"})
	dFile, _ := dMetadata["file"].(map[string]any)
	if dFile["path"] != "reports/done.txt" || dFile["bytes"] != int64(4) {
		t.Fatalf("d metadata=%#v", dMetadata)
	}
	if metadata := producedFileMetadata(s, "run_script", map[string]any{"path": "reports/done.txt"}); metadata != nil {
		t.Fatalf("run_script unexpectedly tracked: %#v", metadata)
	}
	if metadata := producedFileMetadata(s, "write_file", map[string]any{"path": filepath.Join(t.TempDir(), "outside.txt")}); metadata != nil {
		t.Fatalf("outside path unexpectedly tracked: %#v", metadata)
	}
}

func TestSandboxExecutionTargetIsToolResultMetadata(t *testing.T) {
	workspace := t.TempDir()
	cfg := config.Defaults(workspace)
	cfg.Sandbox.Enabled = true
	s := &session.Session{Workspace: workspace}
	for _, test := range []struct {
		name string
		args map[string]any
	}{{"shell", map[string]any{"command": "uname -s"}}, {"run_script", map[string]any{"language": "bash", "source": "uname -s"}}} {
		metadata := sandboxResultMetadata(cfg, s, test.name, test.args)
		data := toolResultEventData(1, "call", test.name, "target", true, true, false, 1, 1, metadata)
		if target, ok := data["target"].(string); !ok || !strings.HasPrefix(target, "sandbox agentb-") {
			t.Fatalf("%s data=%#v", test.name, data)
		}
	}
}
