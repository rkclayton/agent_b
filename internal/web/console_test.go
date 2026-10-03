package web

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"harness/internal/agent"
	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/memory"
	"harness/internal/projection"
	"harness/internal/session"
	"harness/internal/tools"
)

func consoleServer(t *testing.T) (*Server, *session.Registry, *events.Writers, *memory.Manager, *config.Config, string) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Defaults(filepath.Join(root, "workspace"))
	cfg.Memory.Dir = filepath.Join(root, "memory")
	cfg.Connections[0] = runnableTestConnection("local")
	cfg.Agents = []config.Agent{{Name: "Coder", B: "local", Toolset: config.FullToolset()}}
	bus := events.NewBus()
	writers, err := events.NewWriters(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	bus.SetSink(writers.Write)
	server := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, bus)
	registry := session.NewRegistry(bus, writers, server.Connection, 40, server.ConfigSnapshot)
	memories := memory.New(root, server.ConfigSnapshot, func(context.Context, string, string) (int, error) { return 0, nil })
	registry.SetMemoryLoader(memories.Load)
	registry.SetAgentMemoryLoader(memories.LoadAgent)
	server.SetRegistry(registry)
	server.SetWorkspaceState(nil, memories)
	server.SetProjection(projection.NewStore(), writers)
	server.operatorRequest = func(*http.Request) error { return nil }
	return server, registry, writers, memories, &cfg, root
}

func TestChatTreeOperationsAreFilesystemOperations(t *testing.T) {
	server, registry, writers, _, _, root := consoleServer(t); defer writers.Close()
	chat, err := registry.Create("one", "coder", ""); if err != nil { t.Fatal(err) }
	if response := postConsole(t, server, "/api/chats/tree", `{"action":"add","name":"Work"}`); response.Code != 200 { t.Fatalf("add: %d %s", response.Code, response.Body.String()) }
	if response := postConsole(t, server, "/api/chats/tree", `{"action":"move","id":"`+chat.ID+`","folder":"Work"}`); response.Code != 200 { t.Fatalf("move: %d %s", response.Code, response.Body.String()) }
	if _, err := os.Stat(filepath.Join(root, "chats", "Work", "one", "chat.json")); err != nil { t.Fatal(err) }
	response := postConsole(t, server, "/api/chats/tree", `{"action":"delete","path":"Work"}`)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "folder must be empty") { t.Fatalf("non-empty delete: %d %s", response.Code, response.Body.String()) }
}

func TestReplayChatTreeWithoutWritableStoreIsEmpty(t *testing.T) {
	response := httptest.NewRecorder()
	(&Server{}).chatTree(response, httptest.NewRequest(http.MethodGet, "/api/chats/tree", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"root":""`) || !strings.Contains(response.Body.String(), `"chats":[]`) {
		t.Fatalf("replay chat tree: %d %s", response.Code, response.Body.String())
	}
}

func TestDeleteAllChatsRefusesRunningThenKeepsFiles(t *testing.T) {
	server, registry, writers, _, _, _ := consoleServer(t); defer writers.Close()
	first, _ := registry.Create("busy", "coder", "")
	_, _ = registry.Create("idle", "coder", "")
	kept := filepath.Join(first.Workspace, "kept.txt"); if err := os.WriteFile(kept, []byte("keep"), 0o600); err != nil { t.Fatal(err) }
	first.SetRun(session.RunState{Status: "running"})
	response := postConsole(t, server, "/api/chats/delete-all", `{"confirm":true}`)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "busy is running") || len(registry.List()) != 2 { t.Fatalf("refusal: %d %s", response.Code, response.Body.String()) }
	first.SetRun(session.RunState{Status: "idle"})
	response = postConsole(t, server, "/api/chats/delete-all", `{"confirm":true}`)
	if response.Code != 200 || len(registry.List()) != 0 { t.Fatalf("delete: %d %s", response.Code, response.Body.String()) }
	if content, err := os.ReadFile(kept); err != nil || string(content) != "keep" { t.Fatalf("kept file = %q, %v", content, err) }
}

// Item 2hq (v1.6.2): intentional deletion of an already-closed chat removes
// the chat, while what it produced elsewhere stays.
func TestDeletingAClosedChatRemovesItAndKeepsWhatItProduced(t *testing.T) {
	server, registry, writers, memories, cfg, root := consoleServer(t)
	defer writers.Close()
	workspaceFile := filepath.Join(cfg.Workspace, "kept.txt")
	exchangeFile := filepath.Join(root, "exchange", "kept.txt")
	evidenceFile := filepath.Join(root, "logs", "evidence", "kept.jsonl")
	for _, path := range []string{workspaceFile, exchangeFile, evidenceFile} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	item, err := registry.Create("delete me", "coder", cfg.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	path, _, err := memories.NoteAgent("coder", "drop poisoned preference")
	if err != nil {
		t.Fatal(err)
	}
	server.bus.Publish(events.New(events.MemoryNoted, item.ID, "", map[string]any{"path": path, "note": "drop poisoned preference", "target": "agent", "agent_id": "coder"}))
	if _, _, err := writers.RotateSession(item.ID); err != nil {
		t.Fatal(err)
	}
	if err := registry.Close(item.ID); err != nil {
		t.Fatal(err)
	}
	projected, err := server.projector.Snapshot(writers.SessionCursors())
	if err != nil || projected[item.ID].ID != item.ID {
		t.Fatalf("projector seed=%+v err=%v", projected, err)
	}
	result := deleteConsole(t, server, "/api/sessions/main")
	if result.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", result.Code, result.Body)
	}
	if !strings.Contains(result.Body.String(), `"jsonl_files":2`) {
		t.Fatalf("delete did not report what it removed: %s", result.Body)
	}
	if _, ok := registry.Get(item.ID); ok {
		t.Fatal("registry entry survived")
	}
	if matches, _ := filepath.Glob(filepath.Join(root, "logs", "main-*.jsonl")); len(matches) != 0 {
		t.Fatalf("session logs=%v", matches)
	}
	if projected, err := server.projector.Snapshot(writers.SessionCursors()); err != nil || len(projected) != 0 {
		t.Fatalf("projector residue=%+v err=%v", projected, err)
	}
	// The agent's memory note is one of the things that is NOT the chat.
	if value, _ := memories.ReadAgent("coder"); !strings.Contains(value, "drop poisoned preference") {
		t.Fatalf("the agent memory note did not survive the chat: %q", value)
	}
	for _, path := range []string{workspaceFile, exchangeFile, evidenceFile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("kept path %s: %v", path, err)
		}
	}
}

func TestFlushMemoryNamesAndClearsAgentAndWorkspaceLayers(t *testing.T) {
	server, registry, writers, memories, cfg, _ := consoleServer(t)
	defer writers.Close()
	item, err := registry.Create("main", "coder", cfg.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := memories.Note(cfg.Workspace, "workspace fact"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := memories.NoteAgent("coder", "operator preference"); err != nil {
		t.Fatal(err)
	}
	preview := postConsole(t, server, "/api/agents/coder/memory/flush", `{"workspace":"`+strings.ReplaceAll(cfg.Workspace, `\`, `\\`)+`","confirm":false}`)
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), `"agent_entries":1`) || !strings.Contains(preview.Body.String(), `"workspace_entries":1`) {
		t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body)
	}
	result := postConsole(t, server, "/api/agents/coder/memory/flush", `{"workspace":"`+strings.ReplaceAll(cfg.Workspace, `\`, `\\`)+`","confirm":true}`)
	if result.Code != http.StatusOK {
		t.Fatalf("flush status=%d body=%s", result.Code, result.Body)
	}
	if workspace, _ := memories.Read(cfg.Workspace); workspace != "" {
		t.Fatalf("workspace memory=%q", workspace)
	}
	if agent, _ := memories.ReadAgent("coder"); agent != "" {
		t.Fatalf("agent memory=%q", agent)
	}
	if snapshot := item.Snapshot(); snapshot.MemoryContent != "" || snapshot.AgentMemoryContent != "" {
		t.Fatalf("session memory=%+v", snapshot)
	}
}

// Item 2py CHECKS 1 and 5: delete on an open chat and on a chat mid-run is one act
// (the run stops, the chat closes, it is deleted), and afterwards the planted words
// are nowhere under the data root, while the memory note the chat made is kept.
func TestDeleteDeletesOpenAndRunningChatsAndLeavesNoCopy2py(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			<-r.Context().Done() // the run is mid-flight until Delete stops it
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"test-model"}]}`)
	}))
	defer model.Close()
	server, registry, writers, memories, cfg, root := consoleServer(t)
	defer writers.Close()
	cfg.Connections[0].BaseURL = model.URL
	prompt := filepath.Join(root, "system.md")
	if err := os.WriteFile(prompt, []byte("system"), 0o600); err != nil {
		t.Fatal(err)
	}
	renderer, err := agent.LoadTemplate(prompt)
	if err != nil {
		t.Fatal(err)
	}
	runner := agent.NewRunner(server.bus, tools.New(), renderer, server.Connection, server.ConfigSnapshot)
	scheduler := agent.NewScheduler(runner, registry, server.bus, server.ConfigSnapshot)
	server.SetRuntime(scheduler, runner, renderer)
	open, err := registry.Create("open chat", "coder", "")
	if err != nil {
		t.Fatal(err)
	}
	server.bus.Publish(events.New(events.MessageAppended, open.ID, "", map[string]any{"message": map[string]any{"id": "m-planted", "role": "user", "content": "PLANTED-2py-OPEN words"}}))
	path, _, err := memories.Note(cfg.Workspace, "keep project fact")
	if err != nil {
		t.Fatal(err)
	}
	server.bus.Publish(events.New(events.MemoryNoted, open.ID, "", map[string]any{"path": path, "note": "keep project fact", "target": "workspace", "agent_id": "coder"}))
	if response := deleteConsole(t, server, "/api/sessions/"+open.ID); response.Code != http.StatusOK {
		t.Fatalf("open delete status=%d body=%s", response.Code, response.Body)
	}
	running, err := registry.Create("running chat", "coder", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Submit(context.Background(), running.ID, "PLANTED-2py-RUNNING words"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); !running.IsRunning(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the run never started")
		}
	}
	if response := deleteConsole(t, server, "/api/sessions/"+running.ID); response.Code != http.StatusOK {
		t.Fatalf("running delete status=%d body=%s", response.Code, response.Body)
	}
	for _, id := range []string{open.ID, running.ID} {
		if _, ok := registry.Get(id); ok {
			t.Fatalf("chat %s survived", id)
		}
	}
	found := []string{}
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			if data, readErr := os.ReadFile(path); readErr == nil && (bytes.Contains(data, []byte("PLANTED-2py-OPEN")) || bytes.Contains(data, []byte("PLANTED-2py-RUNNING"))) {
				found = append(found, path)
			}
		}
		return nil
	})
	if len(found) != 0 {
		t.Fatalf("the deleted chats survive in %v", found)
	}
	if value, _ := memories.Read(cfg.Workspace); !strings.Contains(value, "keep project fact") {
		t.Fatalf("the memory note was not kept: %q", value)
	}
}

func deleteConsole(t *testing.T, server *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodDelete, path, nil)
	authorizeMutation(request, server)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func postConsole(t *testing.T, server *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	authorizeMutation(request, server)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

// Item 2py CHECK 3, the PC's side: a paired phone is told the chat was deleted, on
// the stream it already receives, and a phone that reconnects is not sent it again.
func TestAPairedPhoneIsToldADeletion2py(t *testing.T) {
	server, registry, writers, _, _, _ := consoleServer(t)
	defer writers.Close()
	item, err := registry.Create("phone chat", "coder", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Close(item.ID); err != nil {
		t.Fatal(err)
	}
	phone := &deletionPhone{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.streamUnitsToDevice(ctx, phone)
	time.Sleep(100 * time.Millisecond)
	if response := deleteConsole(t, server, "/api/sessions/"+item.ID); response.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", response.Code, response.Body)
	}
	for deadline := time.Now().Add(2 * time.Second); !phone.saw(`"chat.deleted"`) || !phone.saw(item.ID); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the phone was not told: %s", phone.text())
		}
	}
	again := &deletionPhone{}
	reconnect, stop := context.WithCancel(context.Background())
	go server.streamUnitsToDevice(reconnect, again)
	time.Sleep(100 * time.Millisecond)
	stop()
	if again.saw(item.ID) {
		t.Fatal("a reconnecting phone was sent the deleted chat")
	}
}

type deletionPhone struct {
	mu    sync.Mutex
	units []string
}

func (d *deletionPhone) Deliver(plaintext []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.units = append(d.units, string(plaintext))
	return nil
}
func (d *deletionPhone) Notify(string, string, string) error { return nil }
func (d *deletionPhone) text() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.Join(d.units, "\n")
}
func (d *deletionPhone) saw(fragment string) bool { return strings.Contains(d.text(), fragment) }
