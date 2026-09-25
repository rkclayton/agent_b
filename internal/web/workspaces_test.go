package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/memory"
	"harness/internal/session"
	workspaceinfo "harness/internal/workspace"
)

func TestNewSessionsIgnoreLegacyFolderInputsAndUseScratch(t *testing.T) {
	root, data, logs := t.TempDir(), t.TempDir(), t.TempDir()
	bound := filepath.Join(root, "repo")
	if err := os.MkdirAll(bound, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bound, "AGENTS.md"), []byte("project route rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults(filepath.Join(root, "default"))
	cfg.Connections = []config.Connection{{ID: "main", Label: "Main", BaseURL: "http://127.0.0.1:8000", Model: "model", Context: config.Context{NCtx: 32768, ReserveOutput: 8192}, Capabilities: config.Capabilities{Streaming: true, ToolCalls: true, OverflowBehavior: "error"}}}
	cfg.Agents = []config.Agent{{Name: "Main", B: "main", D: "main", Toolset: config.FullToolset()}}
	bus := events.NewBus()
	writers, err := events.NewWriters(logs)
	if err != nil {
		t.Fatal(err)
	}
	defer writers.Close()
	server := New(&cfg, filepath.Join(data, "harness.json"), root, RuntimeRoots{Data: data, Workspace: cfg.Workspace}, bus)
	memories := memory.New(data, server.ConfigSnapshot, func(context.Context, string, string) (int, error) { return 0, nil })
	workspaces := workspaceinfo.New(data, memories.Path)
	registry := session.NewRegistry(bus, writers, server.Connection, 40, server.ConfigSnapshot)
	registry.SetMemoryLoader(memories.Load)
	registry.SetAgentMemoryLoader(memories.LoadAgent)
	registry.SetWorkspaceManager(workspaces)
	server.SetRegistry(registry)
	server.SetWorkspaceState(workspaces, memories)

	call := func(method, path string, body any) *httptest.ResponseRecorder {
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		request := httptest.NewRequest(method, path, bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		authorizeMutation(request, server)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		return response
	}
	legacy := call(http.MethodPost, "/api/sessions", map[string]any{"connection_id": "main", "workspace": bound})
	if legacy.Code != http.StatusBadRequest {
		t.Fatalf("legacy folder create %d %s", legacy.Code, legacy.Body.String())
	}
	created := call(http.MethodPost, "/api/sessions", map[string]any{"connection_id": "main"})
	if created.Code != 201 {
		t.Fatalf("create %d %s", created.Code, created.Body.String())
	}
	var response struct {
		Session session.Snapshot `json:"session"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Session.Scratch || response.Session.WorkspaceDir == filepath.Clean(bound) || len(response.Session.ProjectFiles) != 0 {
		t.Fatalf("session=%+v", response.Session)
	}
	planDir := filepath.Join(data, "plans", "stable")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "plan.md"), []byte("# Stable display name\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]any{"repo": bound})
	if err := os.WriteFile(filepath.Join(planDir, "plan.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	dLegacy := call(http.MethodPost, "/api/sessions", map[string]any{"agent_id": "main", "workspace": bound, "role": "d", "plan_id": "stable"})
	if dLegacy.Code != http.StatusBadRequest {
		t.Fatalf("legacy d folder create %d %s", dLegacy.Code, dLegacy.Body.String())
	}
	dCreated := call(http.MethodPost, "/api/sessions", map[string]any{"agent_id": "main", "role": "d"})
	if dCreated.Code != http.StatusCreated {
		t.Fatalf("d create %d %s", dCreated.Code, dCreated.Body.String())
	}
	var dResponse struct {
		Session session.Snapshot `json:"session"`
	}
	if err := json.Unmarshal(dCreated.Body.Bytes(), &dResponse); err != nil {
		t.Fatal(err)
	}
	if dResponse.Session.Role != "d" || !dResponse.Session.Scratch || dResponse.Session.PlanID != "" || dResponse.Session.PlanName != "" || dResponse.Session.ConnectionID != "main" {
		t.Fatalf("d session=%+v", dResponse.Session)
	}
	plans := call(http.MethodGet, "/api/plans", nil)
	if plans.Code != http.StatusOK || !bytes.Contains(plans.Body.Bytes(), []byte(`"id":"stable","name":"Stable display name"`)) {
		t.Fatalf("plans %d %s", plans.Code, plans.Body.String())
	}
	listed := call(http.MethodGet, "/api/workspaces", nil)
	var known []workspaceinfo.Entry
	if err := json.Unmarshal(listed.Body.Bytes(), &known); err != nil {
		t.Fatal(err)
	}
	if listed.Code != 200 || len(known) != 1 || known[0].Dir != filepath.Clean(bound) {
		t.Fatalf("workspaces %d %s", listed.Code, listed.Body.String())
	}
	if strings.HasPrefix(known[0].Dir, filepath.Join(data, "scratch")) {
		t.Fatalf("scratch folder survived known-folder filter: %+v", known)
	}
	if _, _, err := memories.NoteAgent("main", "transient ping output"); err != nil {
		t.Fatal(err)
	}
	withMemory := call(http.MethodPost, "/api/sessions", map[string]any{"agent_id": "main"})
	if withMemory.Code != http.StatusCreated || !bytes.Contains(withMemory.Body.Bytes(), []byte("transient ping output")) {
		t.Fatalf("session did not load agent memory: %d %s", withMemory.Code, withMemory.Body.String())
	}
	// Reproduce production: the durable line is gone while an older session
	// still projects it. The browser request must reconcile that stale view,
	// not turn the already-completed deletion into a bare 404.
	if removed, err := memories.RemoveAgent("main", "transient ping output"); err != nil || !removed {
		t.Fatalf("fixture remove=%t err=%v", removed, err)
	}
	removed := call(http.MethodPost, "/api/agent-memory/remove", map[string]any{"agent_id": "main", "note": "transient ping output", "confirm": true})
	if removed.Code != http.StatusOK || !bytes.Contains(removed.Body.Bytes(), []byte(`"already_absent":true`)) {
		t.Fatalf("agent memory remove %d %s", removed.Code, removed.Body.String())
	}
	remaining, err := memories.ReadAgent("main")
	if err != nil || strings.Contains(remaining, "transient ping output") {
		t.Fatalf("remaining=%q err=%v", remaining, err)
	}
	fresh := call(http.MethodPost, "/api/sessions", map[string]any{"agent_id": "main"})
	if fresh.Code != http.StatusCreated || bytes.Contains(fresh.Body.Bytes(), []byte("transient ping output")) {
		t.Fatalf("fresh session retained removed agent memory: %d %s", fresh.Code, fresh.Body.String())
	}
}
