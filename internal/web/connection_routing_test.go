package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"harness/internal/agent"
	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
)

// Item 2mi: THE GATE OBSERVES TRAFFIC, NOT CONFIGURATION.
//
// Written before the thing it protects, on purpose. A gate that reads
// configuration would have passed through the entire defect this exists for:
// every config-layer read looked correct while the chat switcher was setting
// the AGENT's connection rather than the chat's, and only watching where the
// request landed settles it.
//
// rel-1.24.0/W2 read the run loop first, as the item requires, and the
// classification is:
//
//	FOLLOW THE SESSION'S ConnectionID
//	  internal/agent/run.go:119   the turn itself
//	  internal/agent/run.go:222   the continuation call
//	  internal/agent/run.go:334   the retry call
//	  internal/agent/run.go:147   a delegate child, through the CHILD's session
//
//	FOLLOW AN AGENT ROLE
//	  internal/agent/compaction_summary.go   agent.C, the compaction summary
//	  internal/agent/progress_aux.go         agent.C, the auxiliary progress model
//	  internal/agent/scheduler.go:354        agent.C, presence only — arms a
//	                                         detector, sends nothing
//	  internal/web/agent_connection.go       SETS agent.B — the header switcher
//
// So a chat's own turns follow the chat, and only the two auxiliary models
// follow the agent. The header's menu writes agent.B, which no run-time request
// path reads.
//
// AND THE GATE PASSES TODAY, which the order did not expect and is worth saying
// plainly. registry.SetConnection rebinds a live, healthy session and the next
// run lands on the new endpoint — the mechanism underneath is already per-chat.
// What does not exist is a way for the operator to REACH it: SetConnection is
// called only from the rebind recovery path, which refuses when the connection
// is not missing. That is 2mh, and this gate is what will hold it honest.

type countingEndpoint struct {
	server *httptest.Server
	hits   atomic.Int32
}

func newCountingEndpoint(t *testing.T, name string) *countingEndpoint {
	t.Helper()
	endpoint := &countingEndpoint{}
	endpoint.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint.hits.Add(1)
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
	second.Label, second.BaseURL, second.Model, second.RequestTimeoutS = "Beta", beta.server.URL, "fake", 5
	cfg.Connections = []config.Connection{first, second}
	cfg.Agents = []config.Agent{
		{Name: "agent_b", B: "alpha", Toolset: config.FullToolset()},
		{Name: "Helper", B: "beta", Toolset: config.FullToolset()},
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
	// Each chat reads its connection from its own agent at message time.

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

	// THE ASSERTION IS WHICH ENDPOINT RECEIVED THE REQUEST.
	if alpha.hits.Load() == 0 || beta.hits.Load() == 0 {
		t.Fatalf("each chat's run must land on its own connection: alpha=%d beta=%d", alpha.hits.Load(), beta.hits.Load())
	}
}
