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
	if err := server.ApplyConnector(tools.ConnectorChange{Operation: "add", Name: "acme-services", Service: service}); err != nil {
		t.Fatal(err)
	}
	if got := server.ConfigSnapshot().Services["acme-services"]; got.Kind != "mcp" || got.Auth != service.Auth {
		t.Fatalf("service=%+v", got)
	}
	persisted, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(persisted, []byte(`"acme-services"`)) {
		t.Fatalf("persisted=%s err=%v", persisted, err)
	}
	bad := service
	bad.Auth = "pasted-secret"
	if err := server.ApplyConnector(tools.ConnectorChange{Operation: "edit", Name: "acme-services", Service: bad}); err == nil {
		t.Fatal("bad auth was written")
	}
	if server.ConfigSnapshot().Services["acme-services"].Auth != service.Auth {
		t.Fatal("bad edit changed config")
	}
	if err := server.ApplyConnector(tools.ConnectorChange{Operation: "remove", Name: "acme-services"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := server.ConfigSnapshot().Services["acme-services"]; ok {
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
	// Item 2mb (b), rel-1.23.0: the refusal NAMES the role rather than saying
	// "an agent role", because the operator's next move has to be readable off
	// the message. This fixture holds the connection on both B and C, so the
	// message names both.
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "assigned to Coder's B and C role") {
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

	response := postConfigPatch(t, server, `{"connections":[{"id":"local","credential":"acme","api_key":"`+secret+`"}]}`)
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
	if bytes.Contains(persisted, []byte(secret)) || bytes.Contains(persisted, []byte("api_key")) || !bytes.Contains(persisted, []byte(`"credential": "acme"`)) {
		t.Fatalf("unsafe config: %s", persisted)
	}
	store, err := credential.NewNamed(root, "acme")
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
	response = postConfigPatch(t, server, `{"connections":[{"id":"local","label":"Renamed"}]}`)
	if persisted, err = os.ReadFile(path); err != nil || response.Code != http.StatusOK || !bytes.Contains(persisted, []byte(`"credential": "acme"`)) {
		t.Fatalf("visible-field save changed the hidden credential: status=%d config=%s err=%v", response.Code, persisted, err)
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

func TestPickingModelsThroughConfigKeepsTheirOwnSettings2s5(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "harness.json")
	cfg := config.Defaults(root)
	connection := runnableTestConnection("local")
	connection.Model = "first"
	connection.Context.NCtx, connection.Context.ReserveOutput = 32000, 4000
	connection.Measurement = &config.Measurement{Passed: 1, Total: 2}
	connection.StoreActiveModel()
	connection.SelectModel("second", 128000)
	connection.Context.NCtx = 96000
	connection.Measurement = &config.Measurement{Passed: 2, Total: 2}
	connection.StoreActiveModel()
	connection.SelectModel("first", 0)
	cfg.Connections = []config.Connection{connection}
	cfg.Agents = []config.Agent{{Name: "Fixture", B: "local", Toolset: config.FullToolset()}}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	server := New(&cfg, path, root, RuntimeRoots{Application: root, Data: root, Workspace: root}, events.NewBus())

	if response := postConfigPatch(t, server, `{"connections":[{"id":"local","model":"second"}]}`); response.Code != http.StatusOK {
		t.Fatalf("pick second: %d %s", response.Code, response.Body)
	}
	second, _ := server.Connection("local")
	if second.Context.NCtx != 96000 || second.Measurement == nil || second.Measurement.Passed != 2 {
		t.Fatalf("second=%+v", second)
	}
	if response := postConfigPatch(t, server, `{"connections":[{"id":"local","context":{"n_ctx":88000}}]}`); response.Code != http.StatusOK {
		t.Fatalf("edit second: %d %s", response.Code, response.Body)
	}
	secondResult := &config.Measurement{Passed: 3, Total: 3, Decision: &config.ReasoningDecision{Enabled: false, ReasoningCap: 777}}
	if err := server.storeMeasurement("local", secondResult); err != nil {
		t.Fatal(err)
	}
	recommended := httptest.NewRecorder()
	server.connection(recommended, httptest.NewRequest(http.MethodPost, "/api/connections/local/recommended", nil))
	if recommended.Code != http.StatusOK || !strings.Contains(recommended.Body.String(), `"reasoning.max_tokens":777`) {
		t.Fatalf("recommended second: %d %s", recommended.Code, recommended.Body)
	}
	if response := postConfigPatch(t, server, `{"connections":[{"id":"local","model":"first"}]}`); response.Code != http.StatusOK {
		t.Fatalf("pick first: %d %s", response.Code, response.Body)
	}
	first, _ := server.Connection("local")
	if first.Context.NCtx != 32000 || first.Measurement == nil || first.Measurement.Passed != 1 || first.Measurement.Decision != nil {
		t.Fatalf("first=%+v", first)
	}
	first.SelectModel("second", 0)
	if first.Context.NCtx != 88000 || first.Measurement == nil || first.Measurement.Passed != 3 || first.Measurement.Decision == nil || first.Measurement.Decision.ReasoningCap != 777 {
		t.Fatalf("stored second=%+v", first)
	}

	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Connections []map[string]json.RawMessage `json:"connections"`
	}
	if err := json.Unmarshal(persisted, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Connections) != 1 || document.Connections[0]["models"] == nil || document.Connections[0]["context"] != nil || document.Connections[0]["measurement"] != nil {
		t.Fatalf("model settings were not nested: %s", persisted)
	}
}

// Item 2nq (b) and (e): the placeholder is never written back, and a refusal NAMES the
// connection it is about. The operator's config carried "model" on two connections and
// every fix so far only stopped new ones getting it; writing it back is how it stayed.
func TestSavingTheModelPlaceholderIsRefusedByName2nq(t *testing.T) {
	// Its own root, removed best-effort rather than by t.TempDir(): this case saves the
	// configuration, which creates directories under the root, and t.TempDir()'s cleanup
	// FAILS THE TEST when anything is left behind. It did exactly that on the Linux CI
	// job while passing everywhere else.
	root, err := os.MkdirTemp("", "agentb-model-placeholder-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	path := filepath.Join(root, "harness.json")
	cfg := config.Defaults(root)
	cfg.Connections[0] = runnableTestConnection("server-2")
	cfg.Connections[0].Label = "server-2"
	other := runnableTestConnection("server-3")
	other.Label = "server-3"
	cfg.Connections = append(cfg.Connections, other)
	cfg.Agents = []config.Agent{{Name: "server-2", B: "server-2", Toolset: config.FullToolset()}}
	if saveErr := cfg.Save(path); saveErr != nil {
		t.Fatal(saveErr)
	}
	server := New(&cfg, path, root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, events.NewBus())

	refused := postConfigPatch(t, server, `{"connections":[{"id":"server-3","model":"model"}]}`)
	if refused.Code != http.StatusBadRequest {
		t.Fatalf("the placeholder was accepted: %d %s", refused.Code, refused.Body)
	}
	var problem struct {
		Error string `json:"error"`
		Field string `json:"field"`
	}
	if err := json.Unmarshal(refused.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(problem.Error, "server-3") {
		t.Errorf("the refusal does not name the connection: %q", problem.Error)
	}
	if problem.Field != "connections.server-3.model" {
		t.Errorf("the refusal does not name the field: %q", problem.Field)
	}
	// And the connection that is fine still saves: one connection never blocks another.
	accepted := postConfigPatch(t, server, `{"connections":[{"id":"server-2","model":"a-real-model"}]}`)
	if accepted.Code != http.StatusOK {
		t.Fatalf("a good connection was refused: %d %s", accepted.Code, accepted.Body)
	}
	if got, _ := server.Connection("server-2"); got == nil || got.Model != "a-real-model" {
		t.Fatalf("the accepted model was not written: %+v", got)
	}
	// An EMPTY model still saves: an address is worth keeping before a model is
	// chosen, which is item 2nn (b)'s rule and not something this item takes away.
	empty := postConfigPatch(t, server, `{"connections":[{"id":"server-3","model":""}]}`)
	if empty.Code != http.StatusOK {
		t.Fatalf("an empty model was refused: %d %s", empty.Code, empty.Body)
	}
}
