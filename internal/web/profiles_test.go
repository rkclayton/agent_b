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
	"harness/internal/profiles"
	"harness/internal/session"
)

func TestProfilesEndpointCreatesRenamesAndSwitches(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(t.TempDir())
	path := filepath.Join(root, "harness.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	manager, _, err := profiles.Open(root, path, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := New(&cfg, path, t.TempDir(), RuntimeRoots{Data: root, Profile: manager.Root(manager.Active())}, events.NewBus())
	server.SetProfiles(manager)
	var switched string
	server.SetProfileChanged(func(value string) error { switched = value; return nil })

	response := profileCall(t, server, `{"action":"create","name":"Second"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	response = profileCall(t, server, `{"action":"switch","name":"Second"}`)
	if response.Code != http.StatusOK || manager.Active() != "Second" || switched != manager.Root("Second") {
		t.Fatalf("switch: %d active=%q callback=%q body=%s", response.Code, manager.Active(), switched, response.Body.String())
	}
	response = profileCall(t, server, `{"action":"rename","name":"Second","new_name":"Work"}`)
	if response.Code != http.StatusOK || manager.Active() != "Work" {
		t.Fatalf("rename: %d active=%q body=%s", response.Code, manager.Active(), response.Body.String())
	}
	state := server.snapshot()
	profilesState := state["profiles"].(map[string]any)
	if profilesState["active"] != "Work" {
		t.Fatalf("state profiles=%+v", profilesState)
	}
}

func TestProfileSwitchRefusesLiveRun(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(t.TempDir())
	path := filepath.Join(root, "harness.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	manager, _, err := profiles.Open(root, path, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Create("Second"); err != nil {
		t.Fatal(err)
	}
	writers, err := events.NewWriters(filepath.Join(manager.Root(manager.Active()), "logs"))
	if err != nil {
		t.Fatal(err)
	}
	defer writers.Close()
	bus := events.NewBus()
	server := New(&cfg, path, t.TempDir(), RuntimeRoots{Data: root, Profile: manager.Root(manager.Active())}, bus)
	server.SetProfiles(manager)
	registry := session.NewRegistry(bus, writers, server.Connection, cfg.Run.MaxTurns, server.ConfigSnapshot)
	server.SetRegistry(registry)
	item, err := registry.Create("live", cfg.DefaultAgentID(), "")
	if err != nil {
		t.Fatal(err)
	}
	item.Run.Status = "running"
	response := profileCall(t, server, `{"action":"switch","name":"Second"}`)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "stop the run first") {
		t.Fatalf("response: %d %s", response.Code, response.Body.String())
	}
}

func TestConfigUpdatePersistsAgentsInActiveProfile(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(t.TempDir())
	path := filepath.Join(root, "harness.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	manager, _, err := profiles.Open(root, path, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	first := manager.Active()
	if err := manager.Create("Second"); err != nil {
		t.Fatal(err)
	}
	server := New(&cfg, path, t.TempDir(), RuntimeRoots{Data: root, Profile: manager.Root(manager.Active())}, events.NewBus())
	server.SetProfiles(manager)
	agents := append([]config.Agent(nil), cfg.Agents...)
	agents[0].Name = "Persisted agent"
	body, err := json.Marshal(map[string]any{"agents": agents})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	authorizeMutation(request, server)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("config update: %d %s", response.Code, response.Body.String())
	}
	if got := profileCall(t, server, `{"action":"switch","name":"Second"}`); got.Code != http.StatusOK {
		t.Fatalf("switch second: %d %s", got.Code, got.Body.String())
	}
	if got := profileCall(t, server, `{"action":"switch","name":"`+first+`"}`); got.Code != http.StatusOK {
		t.Fatalf("switch back: %d %s", got.Code, got.Body.String())
	}
	if cfg.Agents[0].Name != "Persisted agent" {
		t.Fatalf("active profile agent was not persisted: %+v", cfg.Agents)
	}
}

func profileCall(t *testing.T, server *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/profiles", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	authorizeMutation(request, server)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code == http.StatusOK {
		var value map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
	}
	return response
}
