package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/credential"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
)

func TestApprovedConnectorMutationValidatesPersistsAndRemoves(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "harness.json")
	cfg := config.Defaults(root)
	server := New(&cfg, path, root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, events.NewBus())
	service := config.Service{Kind: "mcp", BaseURL: "https://broker.test/mcp", Auth: "exec:helper headers", AllowedMethods: []string{"POST"}, TimeoutS: 60, MaxBodyKB: 64}
	if err := server.ApplyConnector(tools.ConnectorChange{Operation: "add", Name: "deploy-broker", Service: service}); err != nil {
		t.Fatal(err)
	}
	if got := server.ConfigSnapshot().Services["deploy-broker"]; got.Kind != "mcp" || got.Auth != service.Auth {
		t.Fatalf("service=%+v", got)
	}
	persisted, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(persisted, []byte(`"deploy-broker"`)) {
		t.Fatalf("persisted=%s err=%v", persisted, err)
	}
	bad := service
	bad.Auth = "pasted-secret"
	if err := server.ApplyConnector(tools.ConnectorChange{Operation: "edit", Name: "deploy-broker", Service: bad}); err == nil {
		t.Fatal("bad auth was written")
	}
	if server.ConfigSnapshot().Services["deploy-broker"].Auth != service.Auth {
		t.Fatal("bad edit changed config")
	}
	if err := server.ApplyConnector(tools.ConnectorChange{Operation: "remove", Name: "deploy-broker"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := server.ConfigSnapshot().Services["deploy-broker"]; ok {
		t.Fatal("removed connector remains")
	}
}

func TestConfigPOSTAssignsConnectionsToLetteredAgentRoles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "harness.json")
	cfg := config.Defaults(root)
	cfg.Connections[0] = runnableTestConnection("local")
	small := cfg.Connections[0]
	small.ID, small.Label = "small", "Small"
	cfg.Connections = append(cfg.Connections, small)
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	server := New(&cfg, path, root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, events.NewBus())
	writers, err := events.NewWriters(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	server.SetRegistry(session.NewRegistry(events.NewBus(), writers, server.Connection, cfg.Run.MaxTurns, server.ConfigSnapshot))

	response := postConfigPatch(t, server, `{"agents":[{"name":"Coder","b":"small","c":"local","toolset":[]}]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	var returned config.Config
	if err := json.Unmarshal(response.Body.Bytes(), &returned); err != nil {
		t.Fatal(err)
	}
	if len(returned.Agents) != 1 || returned.Agents[0].B != "small" || returned.Agents[0].C != "local" {
		t.Fatalf("agents=%+v", returned.Agents)
	}

	response = postConfigPatch(t, server, `{"agents":[{"name":"Coder","b":"small","c":"small","toolset":[]}]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("same-connection status=%d body=%s", response.Code, response.Body)
	}
	if got, _ := server.ConfigSnapshot().Agent("coder"); got.C != "small" {
		t.Fatalf("c connection=%q", got.C)
	}
	request := httptest.NewRequest(http.MethodDelete, "/api/connections/small", nil)
	authorizeMutation(request, server)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "assigned to an agent role") {
		t.Fatalf("delete status=%d body=%s", response.Code, response.Body)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"label":"uses main"}`))
	request.Header.Set("Content-Type", "application/json")
	authorizeMutation(request, server)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated || !bytes.Contains(response.Body.Bytes(), []byte(`"connection_id":"small"`)) {
		t.Fatalf("new session status=%d body=%s", response.Code, response.Body)
	}
}

func TestConfigPOSTStoresConnectionSecretOutsideJSON(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("DPAPI is Windows-only")
	}
	root := t.TempDir()
	path := filepath.Join(root, "harness.json")
	cfg := config.Defaults(root)
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	eventStream, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	server := New(&cfg, path, root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, bus)
	const secret = "connection-test-secret"

	response := postConfigPatch(t, server, `{"connections":[{"id":"local","credential":"homepc","api_key":"`+secret+`"}]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	if bytes.Contains(response.Body.Bytes(), []byte(secret)) || !bytes.Contains(response.Body.Bytes(), []byte(`"api_key":"•••• set"`)) {
		t.Fatalf("unsafe response: %s", response.Body)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(persisted, []byte(secret)) || bytes.Contains(persisted, []byte("api_key")) || !bytes.Contains(persisted, []byte(`"credential": "homepc"`)) {
		t.Fatalf("unsafe config: %s", persisted)
	}
	store, err := credential.NewNamed(root, "homepc")
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Read()
	if err != nil || string(value) != secret {
		t.Fatalf("stored value=%q err=%v", value, err)
	}
	eventJSON, err := json.Marshal(drainTestEvents(eventStream, ""))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(eventJSON, []byte(secret)) {
		t.Fatalf("secret entered event stream: %s", eventJSON)
	}
}

func postConfigPatch(t *testing.T, server *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	authorizeMutation(request, server)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}
