package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
)

func TestSessionAgentReassignment(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	cfg.Connections[0] = runnableTestConnection("first")
	second := runnableTestConnection("second")
	second.Context.ReserveOutput = 2048
	cfg.Connections = append(cfg.Connections, second, config.Connection{ID: "incomplete", Label: "Incomplete"})
	cfg.Agents = []config.Agent{{Name: "First", B: "first", Toolset: config.FullToolset()}, {Name: "Second", B: "second", Toolset: config.FullToolset()}, {Name: "Incomplete", B: "incomplete", Toolset: config.FullToolset()}}

	bus := events.NewBus()
	eventStream, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	writers, err := events.NewWriters(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })

	server := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, bus)
	registry := session.NewRegistry(bus, writers, server.Connection, cfg.Run.MaxTurns, server.ConfigSnapshot)
	server.SetRegistry(registry)
	item, err := registry.Create("main", "first", root)
	if err != nil {
		t.Fatal(err)
	}
	item.Append(events.Message{ID: "m1", Role: "user", Content: "keep me"})

	response := postSessionUpdate(t, server, `{"agent_id":"second"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	var body struct {
		Session session.Snapshot `json:"session"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Session.ConnectionID != "second" || !body.Session.Runnable {
		t.Fatalf("unexpected session: %+v", body.Session)
	}
	if len(body.Session.Messages) != 1 || body.Session.Messages[0].Content != "keep me" {
		t.Fatalf("messages were not preserved: %+v", body.Session.Messages)
	}
	if body.Session.Workspace != root {
		t.Fatalf("workspace changed: %q", body.Session.Workspace)
	}
	if body.Session.Budget.NCtx != 32768 || body.Session.Budget.Reserve != 2048 {
		t.Fatalf("budget was not reset for selected connection: %+v", body.Session.Budget)
	}
	recent := drainTestEvents(eventStream, item.ID)
	if recent[len(recent)-1].Type != events.SessionUpdated {
		t.Fatalf("last event=%q, want %q", recent[len(recent)-1].Type, events.SessionUpdated)
	}

	item.SetRun(session.RunState{Status: "running"})
	response = postSessionUpdate(t, server, `{"agent_id":"first"}`)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "session is running") {
		t.Fatalf("running status=%d body=%s", response.Code, response.Body)
	}
	if snapshot := item.Snapshot(); snapshot.ConnectionID != "second" {
		t.Fatalf("running update changed server to %q", snapshot.ConnectionID)
	}

	item.SetRun(session.RunState{Status: "idle"})
	response = postSessionUpdate(t, server, `{"agent_id":"incomplete"}`)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "base_url is empty") {
		t.Fatalf("incomplete status=%d body=%s", response.Code, response.Body)
	}
}

func TestMissingConnectionCanRebindToSameLabel(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	original := runnableTestConnection("removed")
	original.Label = "My model"
	cfg.Connections = []config.Connection{original}
	cfg.Agents = []config.Agent{{Name: "Agent_b", B: "removed", Toolset: config.FullToolset()}}
	bus := events.NewBus()
	writers, err := events.NewWriters(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	server := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: root}, bus)
	registry := session.NewRegistry(bus, writers, server.Connection, cfg.Run.MaxTurns, server.ConfigSnapshot)
	server.SetRegistry(registry)
	item, err := registry.Create("kept chat", "agent-b", root)
	if err != nil {
		t.Fatal(err)
	}
	replacement := runnableTestConnection("replacement")
	replacement.Label = "My model"
	server.mu.Lock()
	server.cfg.Connections = []config.Connection{replacement}
	server.mu.Unlock()
	registry.RefreshRunnable()
	if got := item.Snapshot(); got.Runnable || got.NotRunnableReason != "connection not found" {
		t.Fatalf("before=%+v", got)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/sessions/"+item.ID+"/rebind", strings.NewReader(`{}`))
	authorizeMutation(request, server)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	if got := item.Snapshot(); got.ConnectionID != "replacement" || !got.Runnable {
		t.Fatalf("after=%+v", got)
	}
}

func TestDropLastMessageEndpoint(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	cfg.Connections[0] = runnableTestConnection("main")
	cfg.Agents = []config.Agent{{Name: "Main", B: "main", Toolset: config.FullToolset()}}
	bus := events.NewBus()
	writers, err := events.NewWriters(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	server := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: root}, bus)
	registry := session.NewRegistry(bus, writers, server.Connection, cfg.Run.MaxTurns, server.ConfigSnapshot)
	server.SetRegistry(registry)
	item, err := registry.Create("main", "main", root)
	if err != nil {
		t.Fatal(err)
	}
	item.Append(events.Message{ID: "m1", Role: "user", Content: "first"})
	item.Append(events.Message{ID: "m2", Role: "assistant", Content: "second"})

	request := httptest.NewRequest(http.MethodPost, "/api/sessions/main/messages/drop-last", strings.NewReader(`{}`))
	authorizeMutation(request, server)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"message_id":"m2"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if messages := item.MessagesCopy(); len(messages) != 1 || messages[0].ID != "m1" {
		t.Fatalf("messages=%#v", messages)
	}

	item.SetRun(session.RunState{Status: "running"})
	request = httptest.NewRequest(http.MethodPost, "/api/sessions/main/messages/drop-last", strings.NewReader(`{}`))
	authorizeMutation(request, server)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("running status=%d body=%s", response.Code, response.Body.String())
	}
}

func runnableTestConnection(id string) config.Connection {
	connection := config.Defaults(".").Connections[0]
	connection.ID, connection.Label, connection.Model = id, id, "model"
	connection.Capabilities.NCtx = 32768
	connection.Context.NCtx = 32768
	connection.Capabilities.ToolCalls = true
	connection.Capabilities.Streaming = true
	connection.Capabilities.OverflowBehavior = "error"
	return connection
}

func postSessionUpdate(t *testing.T, server *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/sessions/main", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	authorizeMutation(request, server)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}
