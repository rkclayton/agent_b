package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentBIsTheStableBuiltInAgent(t *testing.T) {
	cfg := Defaults(t.TempDir())
	if got := cfg.DefaultAgentID(); got != "agent_b" {
		t.Fatalf("default agent = %q, want agent_b", got)
	}
	if cfg.Agents[0].Name != "agent_b" {
		t.Fatalf("built-in name = %q", cfg.Agents[0].Name)
	}
}

func TestAgentNewFieldsAndLegacyPromptMigration(t *testing.T) {
	var agent Agent
	if err := json.Unmarshal([]byte(`{"name":"agent_b","b":"local","model":"m2","prompt":"new","private":true}`), &agent); err != nil {
		t.Fatal(err)
	}
	if agent.Model != "m2" || agent.Prompt != "new" || !agent.Private {
		t.Fatalf("new fields not loaded: %#v", agent)
	}
	var legacy Agent
	if err := json.Unmarshal([]byte(`{"name":"Local","b":"local","prompt_addendum":"old"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Prompt != "old" {
		t.Fatalf("legacy prompt = %q", legacy.Prompt)
	}
}

func TestAgentConnectionSelectsItsModel(t *testing.T) {
	cfg := Defaults(t.TempDir())
	cfg.Connections[0].Models = []ConnectionModel{{Model: "m1"}, {Model: "m2"}}
	cfg.Connections[0].Model = "m1"
	cfg.Agents[0].Model = "m2"
	connection, ok := cfg.AgentConnection("agent_b", "b")
	if !ok || connection.Model != "m2" {
		t.Fatalf("agent connection = %#v, %v", connection, ok)
	}
}

func TestAgentsBecomeProfilesAndConnectionsKeepBehavior2t8(t *testing.T) {
	root := t.TempDir()
	cfg := Defaults(root)
	a := cfg.Connections[0]
	a.ID, a.Label, a.Model = "alpha", "Alpha", "model-a"
	b := a
	b.ID, b.Label, b.Model = "beta", "Beta", "model-b"
	cfg.ConfigVersion = 14
	cfg.Connections = []Connection{a, b}
	cfg.Agents = []Agent{
		{Name: "agent_b", B: "alpha", Model: "model-a", Toolset: FullToolset()},
		{Name: "Writer", B: "beta", Model: "model-b", Prompt: "writer prompt", Toolset: []string{"read_file"}, Private: true},
		{Name: "Reviewer", B: "alpha", Prompt: "review prompt", Toolset: []string{"read_file", "search"}},
	}
	path := filepath.Join(root, "harness.json")
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, migrated, _, err := LoadWithTemplate(path, path)
	if err != nil {
		t.Fatal(err)
	}
	if !migrated || loaded.ConfigVersion != CurrentConfigVersion {
		t.Fatalf("migrated=%t version=%d", migrated, loaded.ConfigVersion)
	}
	if len(loaded.Agents) != 3 || loaded.Agents[1].Prompt != "writer prompt" || !loaded.Agents[1].Private || loaded.Agents[1].B != "" || loaded.Agents[1].Model != "" {
		t.Fatalf("profiles changed: %+v", loaded.Agents)
	}
	if got, ok := loaded.Connection("beta"); !ok || got.AgentProfile != "writer" || got.Model != "model-b" {
		t.Fatalf("beta = %+v, %t", got, ok)
	}
	var duplicate *Connection
	for index := range loaded.Connections {
		if loaded.Connections[index].Label == "Alpha · Reviewer" {
			duplicate = &loaded.Connections[index]
		}
	}
	if duplicate == nil || duplicate.AgentProfile != "reviewer" || duplicate.BaseURL != a.BaseURL || duplicate.Model != "model-a" {
		t.Fatalf("shared connection was not duplicated without loss: %+v", loaded.Connections)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loadedAgain, migratedAgain, _, err := LoadWithTemplate(path, path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if migratedAgain || string(first) != string(second) || len(loadedAgain.Connections) != len(loaded.Connections) {
		t.Fatalf("second load changed migration: migrated=%t", migratedAgain)
	}
}
