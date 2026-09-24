package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	workspaceinfo "harness/internal/workspace"
)

func testPlanRegistry(t *testing.T) (*Registry, string, string) {
	t.Helper()
	root := t.TempDir()
	logs := filepath.Join(root, "logs")
	data := filepath.Join(root, "data")
	writers, err := events.NewWriters(logs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writers.Close() })
	connection := &config.Connection{ID: "main", Label: "Coder", Context: config.Context{NCtx: 32768, ReserveOutput: 8192}, Capabilities: config.Capabilities{Streaming: true, ToolCalls: true, OverflowBehavior: "error"}}
	cfg := config.Defaults(root)
	cfg.Agents = []config.Agent{{Name: "Coder", B: connection.ID, D: connection.ID, Toolset: config.FullToolset()}}
	registry := NewRegistry(events.NewBus(), writers, func(id string) (*config.Connection, bool) { return connection, id == connection.ID }, 40, func() config.Config { return cfg })
	registry.SetWorkspaceManager(workspaceinfo.New(data, func(dir string) string { return filepath.Join(data, filepath.Base(dir)+".md") }))
	registry.SetPlansRoot(filepath.Join(data, "plans"))
	return registry, data, config.AgentID("Coder")
}

func TestChatWithoutFolderOwnsScratch(t *testing.T) {
	registry, data, connection := testPlanRegistry(t)
	item, err := registry.Create("", connection, "")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(data, "scratch", item.ID)
	if !item.Scratch || item.Workspace != want {
		t.Fatalf("scratch=%v folder=%q want %q", item.Scratch, item.Workspace, want)
	}
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Fatalf("scratch folder: %v", err)
	}
}

func TestScratchNeverReusesAnOrphanedChatFolder(t *testing.T) {
	registry, data, connection := testPlanRegistry(t)
	orphan := filepath.Join(data, "scratch", "main")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "retained.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	item, err := registry.Create("", connection, "")
	if err != nil {
		t.Fatal(err)
	}
	// Item 2go (v1.2.5): a chat created with no label HAS no label - it is named
	// by the operator's first message, not after its own id.
	if item.ID == "main" || item.Workspace == orphan || item.Label != "" {
		t.Fatalf("scratch reused orphan: id=%q label=%q folder=%q", item.ID, item.Label, item.Workspace)
	}
	if got, err := os.ReadFile(filepath.Join(orphan, "retained.txt")); err != nil || string(got) != "keep" {
		t.Fatalf("orphan changed: %q %v", got, err)
	}
}

func TestPlanRepoSelectionAndCanonicalDetection(t *testing.T) {
	registry, _, connection := testPlanRegistry(t)
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := registry.Create("", connection, repo)
	if err != nil {
		t.Fatal(err)
	}
	if first.Scratch {
		t.Fatal("folder chat marked scratch")
	}
	plans := registry.planListLocked()
	if len(plans) != 1 || !samePath(plans[0].Repo, repo) || plans[0].Name != "repo" {
		t.Fatalf("plans=%+v", plans)
	}
	if _, err := registry.Create("", connection, filepath.Join(repo, ".")); err != nil {
		t.Fatal(err)
	}
	if got := len(registry.planListLocked()); got != 1 {
		t.Fatalf("duplicate plans=%d", got)
	}
	d, err := registry.CreateRole("", connection, "", "d", plans[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Workspace != repo || d.PlanRepo != repo {
		t.Fatalf("plan-selected folder=%q repo=%q", d.Workspace, d.PlanRepo)
	}
	// The worker binds to the same plan, and to the repository the plan names:
	// a worker in scratch is a worker whose every path points at nothing.
	c, err := registry.CreateRole("worker", connection, "", "c", plans[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Workspace != repo || c.PlanRepo != repo || c.PlanID != plans[0].ID {
		t.Fatalf("worker folder=%q repo=%q plan=%q", c.Workspace, c.PlanRepo, c.PlanID)
	}
	if c.Scratch {
		t.Fatal("the worker was put in scratch instead of the plan's repository")
	}
	// Bound, and still refused the plan's text.
	if _, err := c.WriteRoot(filepath.Join(c.PlanDir, "plan.md")); err == nil {
		t.Fatal("the bound worker was allowed to write plan text")
	}
}

func TestRetainedChatRestoresPreviousFolder(t *testing.T) {
	registry, _, connection := testPlanRegistry(t)
	repo := t.TempDir()
	created, err := registry.Create("", connection, repo)
	if err != nil {
		t.Fatal(err)
	}
	saved := created.Snapshot()
	registry2, _, _ := testPlanRegistry(t)
	restored, err := registry2.Restore(saved)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Workspace != repo || restored.Scratch {
		t.Fatalf("restored folder=%q scratch=%v", restored.Workspace, restored.Scratch)
	}
}

func TestRestoreRebasesAndRecreatesMissingScratchAfterProfileMigration(t *testing.T) {
	registry, _, connection := testPlanRegistry(t)
	created, err := registry.Create("", connection, "")
	if err != nil {
		t.Fatal(err)
	}
	saved := created.Snapshot()
	legacyRoot := t.TempDir()
	saved.Workspace = filepath.Join(legacyRoot, "scratch", saved.ID)
	saved.WorkspaceDir = saved.Workspace
	saved.WorkspaceMissing, saved.Runnable, saved.NotRunnableReason = true, false, "scratch folder is unavailable"
	if err := os.RemoveAll(legacyRoot); err != nil {
		t.Fatal(err)
	}

	restoredRegistry, profileRoot, _ := testPlanRegistry(t)
	restored, err := restoredRegistry.Restore(saved)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(profileRoot, "scratch", saved.ID)
	got := restored.Snapshot()
	if got.Workspace != want || got.WorkspaceMissing || !got.Runnable {
		t.Fatalf("restored scratch=%+v want workspace %q", got, want)
	}
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Fatalf("restored scratch folder: %v", err)
	}
}

func TestRestoreMissingRepositoryIsNotRunnableAndNamesPath(t *testing.T) {
	registry, _, connection := testPlanRegistry(t)
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	created, err := registry.Create("", connection, repo)
	if err != nil {
		t.Fatal(err)
	}
	saved := created.Snapshot()
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}

	restoredRegistry, _, _ := testPlanRegistry(t)
	restored, err := restoredRegistry.Restore(saved)
	if err != nil {
		t.Fatal(err)
	}
	got := restored.Snapshot()
	if !got.WorkspaceMissing || got.Runnable || !strings.Contains(got.NotRunnableReason, repo) {
		t.Fatalf("restored missing repo=%+v", got)
	}
}

func TestEnsurePlanImmediatelyAddsRepoToBSessionUnion(t *testing.T) {
	registry, _, connection := testPlanRegistry(t)
	item, err := registry.Create("", connection, "")
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(t.TempDir(), "approved-repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	plan, created, err := registry.EnsurePlan(repo)
	if err != nil || !created || !samePath(plan.Repo, repo) {
		t.Fatalf("plan=%+v created=%v err=%v", plan, created, err)
	}
	if root, err := item.WriteRoot(filepath.Join(repo, "ready.txt")); err != nil || !samePath(root, repo) {
		t.Fatalf("new repo root=%q err=%v", root, err)
	}
}

func TestPlansTreeReadEverywhereWriteOnlyBoundPlan(t *testing.T) {
	root := t.TempDir()
	plans := filepath.Join(root, "plans")
	own := filepath.Join(plans, "own")
	other := filepath.Join(plans, "other")
	for _, dir := range []string{own, other} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b := &Session{Role: "b", Workspace: filepath.Join(root, "repo"), PlansRoot: plans}
	d := &Session{Role: "d", Workspace: filepath.Join(root, "repo"), PlansRoot: plans, PlanDir: own}
	for name, item := range map[string]*Session{"b": b, "d": d} {
		if got, err := item.ReadRoot(filepath.Join(other, "plan.md")); err != nil || got != plans {
			t.Fatalf("%s read root=%q err=%v", name, got, err)
		}
		if _, err := item.WriteRoot(filepath.Join(other, "plan.md")); err == nil {
			t.Fatalf("%s wrote sibling plan", name)
		}
	}
	if got, err := d.WriteRoot(filepath.Join(own, "plan.md")); err != nil || got != own {
		t.Fatalf("bound write root=%q err=%v", got, err)
	}
}
