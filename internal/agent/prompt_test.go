package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/session"
	"harness/internal/tools"
)

func TestModelTextNamesNoRemovedCommandRule2tc(t *testing.T) {
	var text strings.Builder
	entries, err := os.ReadDir(filepath.Join("..", "..", "prompts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		body, err := os.ReadFile(filepath.Join("..", "..", "prompts", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		text.Write(body)
	}
	cfg := config.Defaults(t.TempDir())
	shell := tools.NewShell(cfg.Shell)
	for _, tool := range []tools.Tool{shell, tools.NewRunScript(shell)} {
		text.WriteString(tool.Description())
		schema, _ := json.Marshal(tool.Schema())
		text.Write(schema)
	}
	runSource, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatal(err)
	}
	text.Write(runSource)
	lower := strings.ToLower(text.String())
	for _, removed := range []string{"file-routing", "file routing", "use read_file instead", "use list_dir instead", "execution-policy bypass", "script-host", "script host", "set-authenticodesignature", "certificate-store", "certificate store", "signing commands"} {
		if strings.Contains(lower, removed) {
			t.Errorf("model-visible text still names removed rule %q", removed)
		}
	}
}

func TestPromptNamesTrustedFoldersAndSaysWhenThereAreNone(t *testing.T) {
	left, right := filepath.Join(t.TempDir(), "notes"), filepath.Join(t.TempDir(), "projects")
	trusted := []config.TrustedFolder{{Path: left}, {Path: right}}
	renderer := &PromptRenderer{text: "folders={{folders}}"}
	renderer.SetTrustedFolders(func() []config.TrustedFolder { return trusted })
	value := renderer.Render(&config.Connection{}, &session.Session{}, nil, "")
	for _, want := range []string{"notes (" + left + ")", "projects (" + right + ")"} {
		if !strings.Contains(value, want) {
			t.Fatalf("prompt %q does not contain %q", value, want)
		}
	}
	trusted = nil
	if value = renderer.Render(&config.Connection{}, &session.Session{}, nil, ""); value != "folders=none yet" {
		t.Fatalf("empty trusted folders prompt=%q", value)
	}
}

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
	full := renderer.RenderParts(connection, item, []string{"read_file"}, item.ProjectBlock, "- MEMORY BLOCK")
	toolsAt, projectAt, memoryAt := strings.Index(full, "tools=read_file"), strings.Index(full, "PROJECT BLOCK"), strings.Index(full, "This folder has 1 saved notes")
	if toolsAt < 0 || projectAt <= toolsAt || memoryAt <= projectAt {
		t.Fatalf("prompt order=%q", full)
	}
}

func TestAgentAddendumPrecedesTheMemoryCount(t *testing.T) {
	renderer := &PromptRenderer{text: "tools={{tools}}\n{{agent}}\n{{project}}\n{{memory}}"}
	item := &session.Session{Workspace: "workspace", PromptAddendum: "Prefer terse reports."}
	value := renderer.RenderMemoryParts(&config.Connection{}, item, []string{"read_file"}, "PROJECT", "- workspace fixture", "- agent fixture")
	toolsAt := strings.Index(value, "tools=read_file")
	addendumAt := strings.Index(value, "BEGIN OPERATOR AGENT ADDENDUM")
	projectAt := strings.Index(value, "PROJECT")
	memoryAt := strings.Index(value, "This folder has 1 saved notes")
	if toolsAt < 0 || addendumAt <= toolsAt || projectAt <= addendumAt || memoryAt <= projectAt || strings.Contains(value, "workspace fixture") || strings.Contains(value, "agent fixture") {
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

func TestMediaCapabilitiesAreSessionStable(t *testing.T) {
	renderer := &PromptRenderer{text: "{{media_capabilities}}\n{{date}}"}
	item := &session.Session{MediaCapabilities: "Image capabilities: exact session claims.", MediaCapabilitiesSet: true}
	before := renderer.RenderMemoryParts(&config.Connection{}, item, nil, "", "", "")
	after := renderer.RenderMemoryParts(&config.Connection{AttachmentHandling: "native"}, item, []string{"run_script"}, "", "", "")
	if before != after || strings.Contains(before, "{{media_capabilities}}") || !strings.Contains(before, "exact session claims") {
		t.Fatalf("session prompt changed: before=%q after=%q", before, after)
	}
}

func TestSplitOffPromptContainsNoNetworkBoundaryLine(t *testing.T) {
	cfg := config.Defaults(t.TempDir())
	cfg.Shell.ServiceAccount.Enabled = false
	renderer := &PromptRenderer{text: "before\n{{network_boundary}}\nafter"}
	item := &session.Session{NetworkBoundary: session.NetworkBoundary(cfg), NetworkBoundarySet: true}
	value := renderer.RenderMemoryParts(&config.Connection{}, item, nil, "", "", "")
	if strings.Contains(value, "Network boundary") || value != "before\n\nafter" {
		t.Fatalf("split-off prompt=%q", value)
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
