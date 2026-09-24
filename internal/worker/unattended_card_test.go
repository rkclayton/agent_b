package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"harness/internal/agent"
	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
)

type schedulerSeam struct{ scheduler *agent.Scheduler }

func (s schedulerSeam) Submit(ctx context.Context, sessionID, text string) (string, error) {
	result, err := s.scheduler.Submit(ctx, sessionID, text)
	return result.RunID, err
}

func (s schedulerSeam) Stop(sessionID string, all bool) int {
	return len(s.scheduler.Stop(sessionID, all))
}

// With the service split disabled, a process-readable outside path stays a
// plain workspace refusal and never raises the obsolete identity card. The
// repeating model is eventually held by the ordinary cycle decision.
func TestUnattendedDisabledSplitDoesNotRaiseIdentityCard(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := map[string]any{"index": 0, "id": "read-outside", "type": "function", "function": map[string]any{"name": "read_file", "arguments": fmt.Sprintf(`{"path":%q}`, outside)}}
		encoded, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{call}}, "finish_reason": "tool_calls"}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 5}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", encoded)
	}))
	defer model.Close()

	repo, data := t.TempDir(), t.TempDir()
	cfg := config.Defaults(repo)
	cfg.Context.Accounting = "estimated"
	cfg.Approval.Mode = config.ApprovalModeBoundaryOnly
	cfg.Shell.ServiceAccount.Enabled = false
	connection := cfg.Connections[0]
	connection.ID, connection.BaseURL, connection.RequestTimeoutS, connection.Model = "fake", model.URL, 5, "fake-model"
	connection.Context.NCtx, connection.Context.ReserveOutput = 32768, 4096
	connection.Capabilities.Streaming, connection.Capabilities.ToolCalls = true, true
	connection.Capabilities.OverflowBehavior, connection.Capabilities.Tokenize = "error", false
	cfg.Connections = []config.Connection{connection}
	cfg.Agents = []config.Agent{{Name: "Worker", B: "fake", Toolset: config.FullToolset()}}
	current := func() config.Config { return cfg }
	lookup := func(id string) (*config.Connection, bool) { return &cfg.Connections[0], id == "fake" }
	bus := events.NewBus()
	writers, err := events.NewWriters(filepath.Join(data, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	defer writers.Close()
	registry := session.NewRegistry(bus, writers, lookup, cfg.Run.MaxTurns, current)
	registry.SetPlansRoot(filepath.Join(data, "plans"))
	plan, _, err := registry.EnsurePlan(repo)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := registry.CreateRole("worker", config.AgentID("Worker"), "", "c", plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	identity := tools.NewFileIdentity(nil)
	identity.Configure(cfg)
	toolset := tools.New(identity.Wrap(tools.NewReadFile(cfg.Tools.ReadFile)))
	toolset.Configure(cfg)
	promptPath := filepath.Join(data, "system.md")
	if err := os.WriteFile(promptPath, []byte("system"), 0o600); err != nil {
		t.Fatal(err)
	}
	renderer, err := agent.LoadTemplate(promptPath)
	if err != nil {
		t.Fatal(err)
	}
	runner := agent.NewRunner(bus, toolset, renderer, lookup, current)
	scheduler := agent.NewScheduler(runner, registry, bus, current)

	stream, release := bus.Subscribe()
	defer release()
	carded := make(chan string, 1)
	go func() {
		for event := range stream {
			if event.Type == events.ApprovalRequired && event.SessionID == worker.ID {
				if data, ok := event.Data.(map[string]any); ok {
					if name, _ := data["name"].(string); name != "" {
						select {
						case carded <- name:
						default:
						}
					}
				}
			}
		}
	}()

	driver := New(bus, schedulerSeam{scheduler}, nil)
	driver.deadline = 600 * time.Millisecond
	outcome := driver.runItem(context.Background(), worker, Item{Text: "read the notes", ID: "1"})
	if outcome.Marker != "!" || outcome.Reason != "waited for approval" {
		t.Fatalf("outcome = %+v, want [!] waited for approval", outcome)
	}
	select {
	case name := <-carded:
		if name == "read_file.operator_override" {
			t.Fatalf("disabled split raised an identity card: %q", name)
		}
	case <-time.After(time.Second):
		t.Fatal("no card was raised")
	}
	for deadline := time.Now().Add(5 * time.Second); scheduler.Active(worker.ID) && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if scheduler.Active(worker.ID) || worker.Snapshot().Run.Status == "paused" {
		t.Fatalf("the expired item's run was left waiting: %+v", worker.Snapshot().Run)
	}
}
