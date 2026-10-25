package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/notifications"
	"harness/internal/session"
)

func TestSessionAgentReassignment(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	cfg.Connections[0] = runnableTestConnection("first")
	second := runnableTestConnection("second")
	second.Context.ReserveOutput = 2048
	cfg.Connections = append(cfg.Connections, second, config.Connection{ID: "incomplete", Label: "Incomplete"})
	cfg.Agents = []config.Agent{{Name: "First", B: "first", Prompt: "first prompt", Toolset: []string{"read_file"}}, {Name: "Second", B: "second", Prompt: "second prompt", Toolset: []string{"list_dir"}}, {Name: "Same connection", B: "second", Prompt: "same connection prompt", Toolset: []string{"write_file"}}, {Name: "Incomplete", B: "incomplete", Toolset: config.FullToolset()}}

	bus := events.NewBus()
	eventStream, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	writers, err := events.NewWriters(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })

	server := newTestServer(t, &cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, bus)
	registry := session.NewRegistry(bus, writers, server.Connection, 0, server.ConfigSnapshot)
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
	if got := item.Snapshot(); got.AgentID != "second" || got.PromptAddendum != "second prompt" || !item.ToolEnabled("list_dir") || item.ToolEnabled("read_file") {
		t.Fatalf("agent did not own prompt and tools: %+v", got)
	}
	response = postSessionUpdate(t, server, `{"agent_id":"same-connection"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("same-connection status=%d body=%s", response.Code, response.Body)
	}
	if got := item.Snapshot(); got.AgentID != "same-connection" || got.ConnectionID != "second" || got.PromptAddendum != "same connection prompt" || !item.ToolEnabled("write_file") || item.ToolEnabled("list_dir") {
		t.Fatalf("same connection did not change agent-owned request state: %+v", got)
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
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"status":"pending"`) {
		t.Fatalf("running status=%d body=%s", response.Code, response.Body)
	}
	if snapshot := item.Snapshot(); snapshot.AgentID != "same-connection" || snapshot.ConnectionID != "second" {
		t.Fatalf("running update changed server to %q", snapshot.ConnectionID)
	}

	item.SetRun(session.RunState{Status: "idle"})
	server.applyPendingSessionAgents()
	if snapshot := item.Snapshot(); snapshot.AgentID != "first" || snapshot.ConnectionID != "first" {
		t.Fatalf("idle legacy connection update=%+v", snapshot)
	}
	server.mu.Lock()
	server.cfg.Agents[0].B = "second"
	server.mu.Unlock()
	if err := registry.ApplyAgentBinding("first"); err != nil {
		t.Fatal(err)
	}
	if snapshot := item.Snapshot(); snapshot.AgentID != "first" || snapshot.ConnectionID != "second" || snapshot.PromptAddendum != "first prompt" {
		t.Fatalf("chat did not follow its agent binding: %+v", snapshot)
	}
	response = postSessionUpdate(t, server, `{"agent_id":"incomplete"}`)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "base_url is empty") {
		t.Fatalf("incomplete status=%d body=%s", response.Code, response.Body)
	}
}

func TestSessionRouteStoresChatNoticeMode2re(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	cfg.Connections[0] = runnableTestConnection("main")
	bus := events.NewBus()
	writers, err := events.NewWriters(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	server := newTestServer(t, &cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: root}, bus)
	registry := session.NewRegistry(bus, writers, server.Connection, 0, server.ConfigSnapshot)
	server.SetRegistry(registry)
	if _, err := registry.Create("main", "agent-b", root); err != nil {
		t.Fatal(err)
	}
	manager := notifications.New(bus, nil, "http://127.0.0.1:8790")
	stored := ""
	manager.SetChatPolicy(map[string]notifications.ChatNoticeState{"main": {Notify: "auto"}}, func(_ string, mode string, _ time.Time) error { stored = mode; return nil })
	server.SetNotifications(manager, nil)
	if response := postSessionUpdate(t, server, `{"notify":"off"}`); response.Code != http.StatusOK || stored != "off" {
		t.Fatalf("off status=%d stored=%q body=%s", response.Code, stored, response.Body)
	}
	if response := postSessionUpdate(t, server, `{"notify":"sometimes"}`); response.Code != http.StatusBadRequest || stored != "off" {
		t.Fatalf("invalid status=%d stored=%q body=%s", response.Code, stored, response.Body)
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
	server := newTestServer(t, &cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: root}, bus)
	registry := session.NewRegistry(bus, writers, server.Connection, 0, server.ConfigSnapshot)
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
	if got := item.Snapshot(); got.Runnable || got.NotRunnableReason != "connection removed no longer exists" {
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
	server := newTestServer(t, &cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: root}, bus)
	registry := session.NewRegistry(bus, writers, server.Connection, 0, server.ConfigSnapshot)
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
	// Item 2nq (b): "model" is not a model, so a connection that is supposed to be
	// runnable cannot be built with the placeholder any more.
	connection.ID, connection.Label, connection.Model = id, id, "test-model"
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
