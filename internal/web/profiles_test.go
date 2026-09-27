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

// Item 2m0 (e) and its acceptance: two profiles, a write through each route,
// and the machine file's per-profile sections unchanged.
//
// This is the test rel-1.20.0 did not have, and the defect it would have caught
// is that EVERY writer of the machine file marshals the live configuration,
// which is the merge. A route that came to change a connection wrote whichever
// profile was active into the machine's defaults as a side effect.
func TestMachineFileKeepsItsOwnPerProfileSections2m0(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(t.TempDir())
	path := filepath.Join(root, "harness.json")
	cfg.Chat.TextSize = "" // the machine's default: unset
	cfg.Agents = []config.Agent{{Name: "Coder", B: "local", Toolset: config.FullToolset()}}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	manager, _, err := profiles.Open(root, path, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := New(&cfg, path, t.TempDir(), RuntimeRoots{Data: root, Profile: manager.Root(manager.Active())}, events.NewBus())
	server.SetProfiles(manager)

	// The active profile takes an opinion about a per-profile setting.
	cfg.Chat.TextSize = "large"
	if err := manager.SaveActive(); err != nil {
		t.Fatal(err)
	}

	// A MACHINE write, through the boundary every machine-scoped route now uses.
	// Nothing about it concerns typography.
	next := cfg
	next.Shell.AllowLocalNetwork = true
	if err := server.saveMachineConfig(next); err != nil {
		t.Fatal(err)
	}
	onDisk, _, _, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.Chat.TextSize != "" {
		t.Errorf("a machine write carried the profile's text size into the machine file: %q", onDisk.Chat.TextSize)
	}
	if !onDisk.Shell.AllowLocalNetwork {
		t.Error("the machine write did not land")
	}

	// A PER-PROFILE write, through the other boundary: it must land, and it must
	// land in the PROFILE rather than in the machine file. (c)'s rejected fix
	// failed exactly here -- it preserved the machine file's sections from disk
	// and dropped this write entirely.
	cfg.Agents[0].C = "local" // an existing connection; the point is the file it lands in
	if err := server.saveProfileConfig(cfg); err != nil {
		t.Fatal(err)
	}
	settings, err := manager.Settings(manager.Active())
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Agents) == 0 || settings.Agents[0].C != "local" {
		t.Errorf("the agent-role write did not reach the profile: %+v", settings.Agents)
	}
	if onDisk, _, _, err := config.Load(path); err != nil {
		t.Fatal(err)
	} else if onDisk.Chat.TextSize != "" {
		t.Errorf("the per-profile write carried typography into the machine file: %q", onDisk.Chat.TextSize)
	}

	// And a second profile still inherits the MACHINE's value rather than the
	// first profile's, which is the consequence all of this exists to protect.
	if err := manager.Create("Second"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Switch("Second"); err != nil {
		t.Fatal(err)
	}
	if cfg.Chat.TextSize != "" {
		t.Errorf("the second profile inherited the first profile's text size: %q", cfg.Chat.TextSize)
	}
}
