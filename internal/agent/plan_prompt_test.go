package agent

import (
	"os"
	"path/filepath"
	"testing"

	"harness/internal/config"
	"harness/internal/session"
)

func TestPlanAwarenessDoesNotChurnSessionPrompt(t *testing.T) {
	root := t.TempDir()
	template := filepath.Join(root, "system.md")
	if err := os.WriteFile(template, []byte("folder={{workspace}} plans={{plans}} tools={{tools}} {{project}} {{memory}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	renderer, err := LoadTemplate(template)
	if err != nil {
		t.Fatal(err)
	}
	item := &session.Session{Workspace: filepath.Join(root, "repo"), PlansRoot: filepath.Join(root, "plans")}
	connection := &config.Connection{}
	before := renderer.Render(connection, item, []string{"read_file"}, "")
	if err := os.MkdirAll(filepath.Join(item.PlansRoot, "new-plan"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(item.PlansRoot, "new-plan", "plan.md"), []byte("# New\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after := renderer.Render(connection, item, []string{"read_file"}, "")
	if before != after {
		t.Fatalf("prompt changed after plan creation:\nbefore %q\nafter  %q", before, after)
	}
}

func TestRegisteredPlainRepoPromptAndToolsMatchNoRepo(t *testing.T) {
	renderer := &PromptRenderer{text: "workspace={{workspace}} plans={{plans}} tools={{tools}}"}
	connection := &config.Connection{}
	workspace := t.TempDir()
	plain := &session.Session{Role: "b", Workspace: workspace, PlansRoot: filepath.Join(t.TempDir(), "plans"), PlanRepos: func() []string { return []string{workspace} }}
	none := &session.Session{Role: "b", Workspace: workspace}
	tools := []string{"read_file", "list_dir", "write_file"}
	if got, want := renderer.Render(connection, plain, tools, ""), renderer.Render(connection, none, tools, ""); got != want {
		t.Fatalf("plain repo gained planning prompt/tools:\nplain %q\nnone  %q", got, want)
	}
}
