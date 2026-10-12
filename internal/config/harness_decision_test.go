package config

import (
	"encoding/json"
	"testing"
)

func TestExistingAndNewAgentProfilesThinkByDefault2tp(t *testing.T) {
	var existing Agent
	if err := json.Unmarshal([]byte(`{"name":"existing","toolset":[]}`), &existing); err != nil {
		t.Fatal(err)
	}
	if !existing.Thinking || !Defaults(t.TempDir()).Agents[0].Thinking {
		t.Fatalf("existing=%v new=%v", existing.Thinking, Defaults(t.TempDir()).Agents[0].Thinking)
	}
	var off Agent
	if err := json.Unmarshal([]byte(`{"name":"off","thinking":false,"toolset":[]}`), &off); err != nil {
		t.Fatal(err)
	}
	if off.Thinking {
		t.Fatal("an explicit off profile was changed")
	}
}
