package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Item 2bq: every plan the product creates starts from the five-section shape
// with a generated map of its repository, and the page hears about it.
func TestNewPlansTakeTheFiveSectionShapeAndAreAnnounced(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "internal", "engine"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "internal", "engine", "engine.go"), []byte("// Package engine runs the walk.\npackage engine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "docs", "README.md"), []byte("# How the walk is documented\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module walk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	heard := []string{}
	PlanChanged = func(kind, planDir string) { heard = append(heard, kind+" "+filepath.Base(planDir)) }
	defer func() { PlanChanged = nil }()
	registry := &Registry{}
	registry.plansRoot = t.TempDir()
	plan, created, err := registry.EnsurePlan(repo)
	if err != nil || !created {
		t.Fatalf("EnsurePlan: %v created=%t", err, created)
	}
	text, err := os.ReadFile(filepath.Join(registry.plansRoot, plan.ID, "plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(text)
	for _, section := range []string{"# " + filepath.Base(repo) + "\n", "## Product and end goals", "## Architecture", "### Invariants", "## Standing rules", "## Milestones and current order", "## Index", "- `docs/` — How the walk is documented", "- `internal/` — 1 entries", "files: go.mod", "Discovery before repair"} {
		if !strings.Contains(body, section) {
			t.Fatalf("plan.md is missing %q:\n%s", section, body)
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "[ ]") && strings.HasPrefix(strings.TrimSpace(line), "-") {
			t.Fatalf("the template must carry no marker line: %q", line)
		}
	}
	if len(heard) != 1 || heard[0] != "plan.created "+plan.ID {
		t.Fatalf("heard %v", heard)
	}
	if err := UpdatePlanFile(filepath.Join(registry.plansRoot, plan.ID, "plan.md"), func(current string, _ bool) (string, error) { return current + "- [ ] 1 a new item\n", nil }); err != nil {
		t.Fatal(err)
	}
	if len(heard) != 2 || heard[1] != "plan.updated "+plan.ID {
		t.Fatalf("a rewrite was not announced: %v", heard)
	}
}

func TestRepoMapNamesAnUnboundPlanHonestly(t *testing.T) {
	if got := RepoMap(""); !strings.Contains(got, "no repository is bound") {
		t.Fatalf("got %q", got)
	}
}

// The fourth route: a d-session's first write creates its plan from the same
// template, announced once the folder is complete.
func TestADSessionsFirstWriteCreatesATemplatedPlan(t *testing.T) {
	heard := []string{}
	PlanChanged = func(kind, planDir string) { heard = append(heard, kind) }
	defer func() { PlanChanged = nil }()
	s := &Session{ID: "d1", Role: "d", PlansRoot: t.TempDir()}
	dir, err := s.WriteRoot("plan.md")
	if err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(filepath.Join(dir, "plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(text), "# Untitled plan\n\n## Product and end goals") || !strings.Contains(string(text), "no repository is bound") {
		t.Fatalf("plan.md:\n%s", text)
	}
	if len(heard) != 1 || heard[0] != "plan.created" {
		t.Fatalf("heard %v", heard)
	}
}
