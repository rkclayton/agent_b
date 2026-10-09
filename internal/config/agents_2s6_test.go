package config

import (
	"encoding/json"
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
