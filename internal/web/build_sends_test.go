package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"harness/internal/agent"
	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
)

// v0.70.1 overrule: "Build plan now? → yes" sends the fixed opening request
// as the planning chat's first message, and nothing else; a chat already
// under way is only opened.
// buildPlanFixture is a server with a real scheduler on a model stub that counts
// requests, and a plan registered for a fresh repository.
func buildPlanFixture(t *testing.T, prepare func(repo string)) (*Server, *session.Registry, *agent.Scheduler, session.Plan, *atomic.Int32) {
	t.Helper()
	requests := &atomic.Int32{}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		encoded, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "Reading the repository."}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 5}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", encoded)
	}))
	t.Cleanup(model.Close)
	repo, data := t.TempDir(), t.TempDir()
	prepare(repo)
	cfg := config.Defaults(repo)
	cfg.Context.Accounting = "estimated"
	p := runnableTestConnection("fake")
	p.BaseURL = model.URL
	cfg.Connections = []config.Connection{p}
	cfg.Agents = []config.Agent{{Name: "Build", B: "fake", D: "fake", Toolset: config.FullToolset()}}
	bus := events.NewBus()
	writers, err := events.NewWriters(filepath.Join(data, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	server := New(&cfg, filepath.Join(data, "harness.json"), repo, RuntimeRoots{Application: repo, Data: data, Workspace: repo}, bus)
	registry := session.NewRegistry(bus, writers, server.Connection, cfg.Run.MaxTurns, server.ConfigSnapshot)
	server.SetRegistry(registry)
	promptPath := filepath.Join(data, "system.md")
	if err := os.WriteFile(promptPath, []byte("system"), 0o600); err != nil {
		t.Fatal(err)
	}
	renderer, err := agent.LoadTemplate(promptPath)
	if err != nil {
		t.Fatal(err)
	}
	runner := agent.NewRunner(bus, tools.New(), renderer, server.Connection, server.ConfigSnapshot)
	scheduler := agent.NewScheduler(runner, registry, bus, server.ConfigSnapshot)
	server.SetRuntime(scheduler, runner, renderer)
	plan, _, err := registry.EnsurePlan(repo)
	if err != nil {
		t.Fatal(err)
	}
	return server, registry, scheduler, plan, requests
}

func TestBuildPlanYesSendsTheOpeningRequestOnce(t *testing.T) {
	server, registry, scheduler, plan, _ := buildPlanFixture(t, func(string) {})
	build := func() map[string]any {
		response := httptest.NewRecorder()
		server.buildPlan(response, httptest.NewRequest(http.MethodPost, "/api/plans/build", strings.NewReader(`{"plan_id":"`+plan.ID+`","agent_id":"build"}`)))
		var body map[string]any
		_ = json.Unmarshal(response.Body.Bytes(), &body)
		if response.Code >= 300 {
			t.Fatalf("build: %d %s", response.Code, response.Body)
		}
		return body
	}
	first := build()
	if first["sent"] != true {
		t.Fatalf("Yes did not send the opening request: %v", first)
	}
	item, _ := registry.Get(first["session_id"].(string))
	for deadline := time.Now().Add(5 * time.Second); scheduler.Active(item.ID) && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	users := []string{}
	for _, message := range item.MessagesCopy() {
		if message.Role == "user" {
			users = append(users, message.Content)
		}
	}
	if len(users) != 1 || users[0] != planBuildDraft {
		t.Fatalf("the planning chat's user messages = %q, want exactly the opening request", users)
	}
	again := build()
	if again["sent"] != false || again["reused"] != true {
		t.Fatalf("a chat under way was sent the request again: %v", again)
	}
}

// Item 2q0 CHECK 1: a repository whose INBOX.md holds a planner's queue is registered and
// its chat opens idle with one line; no drafting request reaches the model.
func TestAddingARepoWithAPlannerQueueWaitsForGo2q0(t *testing.T) {
	server, registry, scheduler, plan, requests := buildPlanFixture(t, func(repo string) {
		if err := os.WriteFile(filepath.Join(repo, "INBOX.md"), []byte("# PUBLISH THEN EXECUTE\r\n\r\n## rel-1 — an order\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	response := httptest.NewRecorder()
	server.buildPlan(response, httptest.NewRequest(http.MethodPost, "/api/plans/build", strings.NewReader(`{"plan_id":"`+plan.ID+`","agent_id":"build","brief":{"purpose":"to take over from codex"}}`)))
	var body map[string]any
	_ = json.Unmarshal(response.Body.Bytes(), &body)
	item, ok := registry.Get(fmt.Sprint(body["session_id"]))
	if response.Code >= 300 || !ok || body["sent"] != false || body["draft"] != "" {
		t.Fatalf("build: %d %s", response.Code, response.Body)
	}
	time.Sleep(100 * time.Millisecond)
	messages := item.MessagesCopy()
	if requests.Load() != 0 || scheduler.Active(item.ID) || len(messages) != 1 || messages[0].Role != "assistant" || messages[0].Content != "This repository has a planner; say go to run its queue." {
		t.Fatalf("requests=%d messages=%+v", requests.Load(), messages)
	}
}
