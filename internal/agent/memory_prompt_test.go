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
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
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
		"Remember only a correction the operator gave, a preference they stated, or a fact about their project you had to discover — avoidance of your own tools is never remembered. Recall first; one note per fact; give it a scope.",
		"Rules: resolve a named plan or repo from the operator's words and work there; ask when more than one could match; nothing named → this chat's folder.",
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
	for _, removed := range []string{"repository allow-list", "File tools may use scratch and every listed plan repo", "offer to register it as a plan", "never search the operator's connection"} {
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
	requestBody := make(chan string, 1)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		encoded, _ := json.Marshal(body)
		requestBody <- string(encoded)
		w.Header().Set("Content-Type", "text/event-stream")
		delta := map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": "list-downloads", "type": "function",
			"function": map[string]any{"name": "list_dir", "arguments": `{"path":"C:\\outside\\Downloads"}`},
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
		AgentMemoryBlock: "Notes about how this agent works with the operator:\n- " + badNote + "  [about-agent: yes]",
	}
	if _, err := runner.AddUser(context.Background(), item, "how many folders are in my Downloads"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _, _, _ = runner.Run(ctx, item, "run-memory"); close(done) }()
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
			cancel()
			if err := runner.Gate().Decide(item.ID, data["call_id"].(string), "deny"); err != nil {
				t.Fatal(err)
			}
			<-done
			return
		case <-time.After(5 * time.Second):
			t.Fatal("the existing outside-folder card was not raised")
		}
	}
}
