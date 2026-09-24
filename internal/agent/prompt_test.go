package agent

import (
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/session"
)

func TestPromptIncludesOSDateAndContextWithoutInventingCity(t *testing.T) {
	renderer := &PromptRenderer{text: "date={{date}} context={{os_context}}"}
	connection := &config.Connection{}
	value := renderer.Render(connection, &session.Session{Workspace: "workspace"}, nil, "")
	if strings.Contains(value, "{{") || !strings.Contains(value, "date=") || !strings.Contains(value, "timezone") {
		t.Fatalf("OS context was not rendered: %q", value)
	}
}

func TestProjectInstructionsSitAfterToolNamesAndBeforeMemory(t *testing.T) {
	renderer := &PromptRenderer{text: "prefix tools={{tools}}\n{{project}}\nbody\n{{memory}}"}
	connection := &config.Connection{}
	item := &session.Session{Workspace: "workspace", ProjectBlock: "PROJECT BLOCK"}
	base := renderer.RenderParts(connection, item, []string{"read_file"}, "", "")
	full := renderer.RenderParts(connection, item, []string{"read_file"}, item.ProjectBlock, "MEMORY BLOCK")
	toolsAt, projectAt, memoryAt := strings.Index(full, "tools=read_file"), strings.Index(full, "PROJECT BLOCK"), strings.Index(full, "MEMORY BLOCK")
	if toolsAt < 0 || projectAt <= toolsAt || memoryAt <= projectAt {
		t.Fatalf("prompt order=%q", full)
	}
	if delta := len(full) - len(base); delta != len("PROJECT BLOCK")+len("MEMORY BLOCK")+1 {
		t.Fatalf("byte delta=%d base=%q full=%q", delta, base, full)
	}
}

func TestAgentAddendumAndTwoMemoryLayersHaveStableOrder(t *testing.T) {
	renderer := &PromptRenderer{text: "tools={{tools}}\n{{agent}}\n{{project}}\n{{memory}}"}
	item := &session.Session{Workspace: "workspace", PromptAddendum: "Prefer terse reports."}
	value := renderer.RenderMemoryParts(&config.Connection{}, item, []string{"read_file"}, "PROJECT", "WORKSPACE MEMORY", "AGENT MEMORY")
	toolsAt := strings.Index(value, "tools=read_file")
	addendumAt := strings.Index(value, "BEGIN OPERATOR AGENT ADDENDUM")
	projectAt := strings.Index(value, "PROJECT")
	workspaceAt := strings.Index(value, "WORKSPACE MEMORY")
	agentAt := strings.Index(value, "AGENT MEMORY")
	if toolsAt < 0 || addendumAt <= toolsAt || projectAt <= addendumAt || workspaceAt <= projectAt || agentAt <= workspaceAt {
		t.Fatalf("prompt order=%q", value)
	}
}

func TestNetworkBoundaryIsSessionStable(t *testing.T) {
	renderer := &PromptRenderer{text: "{{network_boundary}}\n{{date}}"}
	item := &session.Session{NetworkBoundary: "Network boundary: exact session policy."}
	before := renderer.RenderMemoryParts(&config.Connection{}, item, nil, "", "", "")
	item.NetworkBoundary = "Network boundary: exact session policy."
	after := renderer.RenderMemoryParts(&config.Connection{}, item, nil, "", "", "")
	if before != after || !strings.Contains(before, "Network boundary: exact session policy.") {
		t.Fatalf("session prompt changed: before=%q after=%q", before, after)
	}
}

func TestPlannerPromptLoadsForDAndPlanPageFallbackAndStaysStable(t *testing.T) {
	renderer := &PromptRenderer{text: "system", planner: "planner rules"}
	connection := &config.Connection{}
	d := &session.Session{Role: "d"}
	if got := renderer.Render(connection, d, nil, ""); got != "system\n\nplanner rules" {
		t.Fatalf("d prompt=%q", got)
	}
	b := &session.Session{Role: "b"}
	if got := renderer.Render(connection, b, nil, ""); got != "system" {
		t.Fatalf("ordinary b prompt=%q", got)
	}
	b.SetPlanPage(true)
	before, after := renderer.Render(connection, b, nil, ""), renderer.Render(connection, b, nil, "")
	if before != after || before != "system\n\nplanner rules" {
		t.Fatalf("fallback prompt before=%q after=%q", before, after)
	}
}
