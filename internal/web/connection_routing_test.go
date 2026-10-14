package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"harness/internal/agent"
	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
)

// The gate observes the request, not only configuration: the chat's agent owns
// the endpoint, opening prompt and toolset even when its stored connection is stale.

type countingEndpoint struct {
	server *httptest.Server
	hits   atomic.Int32
	body   atomic.Value
}

func newCountingEndpoint(t *testing.T, name string) *countingEndpoint {
	t.Helper()
	endpoint := &countingEndpoint{}
	endpoint.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint.hits.Add(1)
		data, _ := io.ReadAll(r.Body)
		endpoint.body.Store(string(data))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": "from " + name}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1},
		})
	}))
	t.Cleanup(endpoint.server.Close)
	t.Cleanup(endpoint.server.CloseClientConnections)
	return endpoint
}

// Two chats on different agents, each agent bound to a different connection,
// both run and each lands on its own endpoint.
func TestEachChatsRunLandsOnItsOwnConnection2mi(t *testing.T) {
	alpha, beta := newCountingEndpoint(t, "alpha"), newCountingEndpoint(t, "beta")

	root := t.TempDir()
	cfg := config.Defaults(root)
	first := runnableTestConnection("alpha")
	first.Label, first.BaseURL, first.Model, first.RequestTimeoutS = "Alpha", alpha.server.URL, "fake", 5
	second := runnableTestConnection("beta")
	second.Label, second.BaseURL, second.Model, second.RequestTimeoutS, second.AgentProfile = "Beta", beta.server.URL, "fake", 5, "helper"
	cfg.Connections = []config.Connection{first, second}
	cfg.Agents = []config.Agent{
		{Name: "agent_b", Prompt: "alpha profile", Toolset: []string{"read_file"}},
		{Name: "Helper", Prompt: "beta profile", Toolset: []string{"list_dir"}},
	}
	path := filepath.Join(root, "harness.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}

	bus := events.NewBus()
	writers, err := events.NewWriters(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	bus.SetSink(writers.Write)
	server := New(&cfg, path, root, RuntimeRoots{Application: root, Data: root, Workspace: root}, bus)
	registry := session.NewRegistry(bus, writers, server.Connection, cfg.Run.MaxTurns, server.ConfigSnapshot)
	server.SetRegistry(registry)

	onAlpha, err := registry.Create("on-alpha", "agent_b", root)
	if err != nil {
		t.Fatal(err)
	}
	onBeta, err := registry.Create("on-beta", "helper", root)
	if err != nil {
		t.Fatal(err)
	}
	if got := onAlpha.Snapshot(); got.ConnectionID != "alpha" || got.AgentID != "agent_b" {
		t.Fatalf("alpha chat route=%q profile=%q", got.ConnectionID, got.AgentID)
	}
	if got := onBeta.Snapshot(); got.ConnectionID != "beta" || got.AgentID != "helper" || got.PromptAddendum != "beta profile" {
		t.Fatalf("beta chat route=%q profile=%q prompt=%q", got.ConnectionID, got.AgentID, got.PromptAddendum)
	}
	// The agent is authoritative. Even stale connection state is repaired from
	// that agent before the request leaves the process.
	onBeta.ConnectionID = "alpha"

	promptPath := filepath.Join(root, "system.md")
	if err := os.WriteFile(promptPath, []byte("system {{tools}} {{memory}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	renderer, err := agent.LoadTemplate(promptPath)
	if err != nil {
		t.Fatal(err)
	}
	toolset := tools.New()
	toolset.Configure(cfg)
	runner := agent.NewRunner(bus, toolset, renderer, server.Connection, server.ConfigSnapshot)
	scheduler := agent.NewScheduler(runner, registry, bus, server.ConfigSnapshot)
	server.SetRuntime(scheduler, runner, renderer)

	if _, err := scheduler.Submit(context.Background(), onAlpha.ID, "hello alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Submit(context.Background(), onBeta.ID, "hello beta"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return alpha.hits.Load() > 0 && beta.hits.Load() > 0 }, "both chats' runs did not reach a server")
	waitFor(t, func() bool { return !scheduler.Active(onAlpha.ID) && !scheduler.Active(onBeta.ID) }, "chat runs did not finish")

	// THE ASSERTION IS WHICH ENDPOINT RECEIVED THE REQUEST.
	if alpha.hits.Load() == 0 || beta.hits.Load() == 0 {
		t.Fatalf("each chat's run must land on its own connection: alpha=%d beta=%d", alpha.hits.Load(), beta.hits.Load())
	}
	if body, _ := alpha.body.Load().(string); !strings.Contains(body, "alpha profile") || strings.Contains(body, "beta profile") {
		t.Fatalf("alpha request did not carry its agent: %s", body)
	}
	if body, _ := beta.body.Load().(string); !strings.Contains(body, "beta profile") || strings.Contains(body, "alpha profile") {
		t.Fatalf("beta request did not carry its agent: %s", body)
	}
	if err := registry.SetConnection(onAlpha.ID, "beta"); err != nil {
		t.Fatal(err)
	}
	if got := onAlpha.Snapshot(); got.ConnectionID != "beta" || got.AgentID != "helper" || got.PromptAddendum != "beta profile" || !onAlpha.ToolEnabled("list_dir") || onAlpha.ToolEnabled("read_file") {
		t.Fatalf("switched chat did not take the connection profile: %+v", got)
	}
	if got := onBeta.Snapshot(); got.ConnectionID != "beta" || got.AgentID != "helper" {
		t.Fatalf("other chat changed: %+v", got)
	}
	beforeMissing := beta.hits.Load()
	server.mu.Lock()
	server.cfg.Connections = server.cfg.Connections[:1]
	server.mu.Unlock()
	registry.RefreshRunnable()
	if _, err := scheduler.Submit(context.Background(), onBeta.ID, "must not fall back"); err == nil || !strings.Contains(err.Error(), "connection not found") {
		t.Fatalf("missing connection submission error=%v", err)
	}
	if beta.hits.Load() != beforeMissing || alpha.hits.Load() != 1 {
		t.Fatalf("missing connection dialled a server: alpha=%d beta=%d", alpha.hits.Load(), beta.hits.Load())
	}
}
