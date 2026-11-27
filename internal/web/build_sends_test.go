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
// messageReplayFixture is a server with a real scheduler on a model stub that counts
// requests, and a plan registered for a fresh repository.
func messageReplayFixture(t *testing.T, prepare func(repo string)) (*Server, *session.Registry, *agent.Scheduler, *atomic.Int32) {
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
	cfg.Agents = []config.Agent{{Name: "Build", B: "fake", Toolset: config.FullToolset()}}
	bus := events.NewBus()
	writers, err := events.NewWriters(filepath.Join(data, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	server := newTestServer(t, &cfg, filepath.Join(data, "harness.json"), repo, RuntimeRoots{Application: repo, Data: data, Workspace: repo}, bus)
	registry := session.NewRegistry(bus, writers, server.Connection, 0, server.ConfigSnapshot)
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
	return server, registry, scheduler, requests
}

func TestMessageReopensAClosedChatInTheSameRequest2rd(t *testing.T) {
	server, registry, scheduler, _ := messageReplayFixture(t, func(string) {})
	send := func(name string, phone bool) {
		item, err := registry.Create(name, "build", "")
		if err != nil {
			t.Fatal(err)
		}
		if err = registry.Close(item.ID); err != nil {
			t.Fatal(err)
		}
		body := `{"session_id":"` + item.ID + `","text":"continue this chat"}`
		if phone {
			answer := dispatch(t, server, `{"v":1,"kind":"request","id":"00112233445566778899aabbccddeeff","route":"message","body":`+body+`}`)
			if answer.Status != http.StatusAccepted {
				t.Fatalf("phone message = %d %s", answer.Status, answer.Body)
			}
		} else {
			answer := httptest.NewRecorder()
			server.message(answer, httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader(body)))
			if answer.Code != http.StatusAccepted {
				t.Fatalf("page message = %d %s", answer.Code, answer.Body.String())
			}
		}
		if item.IsClosed() {
			t.Fatal("the accepted message left the chat closed")
		}
		for deadline := time.Now().Add(5 * time.Second); scheduler.Active(item.ID) && time.Now().Before(deadline); {
			time.Sleep(10 * time.Millisecond)
		}
		messages := item.MessagesCopy()
		if len(messages) == 0 || messages[0].Content != "continue this chat" {
			t.Fatalf("message was not accepted in the reopened chat: %+v", messages)
		}
	}
	send("page closed", false)
	send("phone closed", true)
	missing := httptest.NewRecorder()
	server.message(missing, httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader(`{"session_id":"missing","text":"continue"}`)))
	if missing.Code != http.StatusBadRequest || !strings.Contains(missing.Body.String(), "session not found") {
		t.Fatalf("unreopenable chat = %d %s", missing.Code, missing.Body.String())
	}
}

// Item 2q0 CHECK 1: a repository whose INBOX.md holds a planner's queue is registered and
// its chat opens idle with one line; no drafting request reaches the model.
