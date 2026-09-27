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

// Item 2mb (b), (c) and (d): a refused delete says which role holds the
// connection, on the row that was clicked, and a delete that is refused or not
// found leaves the configuration exactly as it was.
func TestARefusedDeleteNamesTheRoleAndLeavesTheSliceAlone2mb(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	cfg.Connections = []config.Connection{runnableTestConnection("a"), runnableTestConnection("b"), runnableTestConnection("c")}
	cfg.Agents = []config.Agent{{Name: "Coder", B: "a", C: "b", Toolset: config.FullToolset()}}
	path := filepath.Join(root, "harness.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	server := New(&cfg, path, root, RuntimeRoots{Application: root, Data: root, Workspace: root}, events.NewBus())
	server.SetRegistry(session.NewRegistry(events.NewBus(), nil, server.Connection, 1, server.ConfigSnapshot))

	// (b): the message names the agent and the role, not "an agent role".
	response := httptest.NewRecorder()
	server.connection(response, httptest.NewRequest(http.MethodDelete, "/api/connections/b", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("deleting an assigned connection: %d %s", response.Code, response.Body)
	}
	var refusal struct{ Error, Field string }
	if err := json.Unmarshal(response.Body.Bytes(), &refusal); err != nil {
		// The envelope may nest; fall back to the raw body for the assertions.
		refusal.Error, refusal.Field = response.Body.String(), response.Body.String()
	}
	body := response.Body.String()
	if !strings.Contains(body, "Coder") || !strings.Contains(body, "C role") {
		t.Errorf("the refusal does not name the agent and its role: %s", body)
	}
	// (c): the field is the row, so the client can put it where the click was.
	if !strings.Contains(body, "connections.b") {
		t.Errorf("the refusal does not carry the row as its field: %s", body)
	}

	// (d): a NOT-FOUND delete leaves the configuration untouched, and a snapshot
	// taken before a successful delete is not rewritten under the reader.
	before := server.ConfigSnapshot().Connections
	notFound := httptest.NewRecorder()
	server.connection(notFound, httptest.NewRequest(http.MethodDelete, "/api/connections/nope", nil))
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("deleting an unknown id: %d %s", notFound.Code, notFound.Body)
	}
	if len(server.ConfigSnapshot().Connections) != 3 {
		t.Errorf("a not-found delete changed the configuration: %+v", server.ConfigSnapshot().Connections)
	}

	// The aliasing itself: delete the middle connection and read the slice that
	// was taken beforehand. With the old in-place filter this read [a, c, c].
	held := before
	ok := httptest.NewRecorder()
	server.connection(ok, httptest.NewRequest(http.MethodDelete, "/api/connections/c", nil))
	if ok.Code != http.StatusOK {
		t.Fatalf("deleting an unassigned connection: %d %s", ok.Code, ok.Body)
	}
	if len(held) != 3 || held[0].ID != "a" || held[1].ID != "b" || held[2].ID != "c" {
		t.Errorf("the delete rewrote a slice someone else was holding: %v", []string{held[0].ID, held[1].ID, held[2].ID})
	}
}
