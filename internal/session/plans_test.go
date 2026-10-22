package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/chatstore"
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

func TestChatWithoutFolderOwnsNamedChatDirectory(t *testing.T) {
	registry, data, connection := testPlanRegistry(t)
	item, err := registry.Create("", connection, "")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(data, "chats", "chat")
	if !item.Scratch || item.Workspace != want {
		t.Fatalf("chat-owned=%v folder=%q want %q", item.Scratch, item.Workspace, want)
	}
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Fatalf("chat folder: %v", err)
	}
	metadata, err := chatstore.ReadMetadata(want)
	if err != nil || metadata.ID != item.ID || metadata.Label != item.Label {
		t.Fatalf("chat.json = %+v, %v", metadata, err)
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
	if item.Workspace == orphan || item.Label != "" || !strings.HasPrefix(item.Workspace, filepath.Join(data, "chats")) {
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
	registered, _, err := registry.EnsurePlan(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.EnablePlanning(registered.ID); err != nil {
		t.Fatal(err)
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
	if !samePath(d.Workspace, repo) || !samePath(d.PlanRepo, repo) {
		t.Fatalf("plan-selected folder=%q repo=%q", d.Workspace, d.PlanRepo)
	}
	// The worker binds to the same plan, and to the repository the plan names:
	// a worker in scratch is a worker whose every path points at nothing.
	c, err := registry.CreateRole("worker", connection, "", "c", plans[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(c.Workspace, repo) || !samePath(c.PlanRepo, repo) || c.PlanID != plans[0].ID {
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

func TestRestoreRebasesAndRecreatesMissingChatHomeAfterProfileMigration(t *testing.T) {
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
	want := filepath.Join(profileRoot, "chats", "chat")
	got := restored.Snapshot()
	if got.Workspace != want || got.WorkspaceMissing || !got.Runnable {
		t.Fatalf("restored chat=%+v want workspace %q", got, want)
	}
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Fatalf("restored chat folder: %v", err)
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

func TestRootPlanWriteOnlyByItsBoundPlanner(t *testing.T) {
	root := t.TempDir()
	plans := filepath.Join(root, "plans")
	own := filepath.Join(root, "own")
	other := filepath.Join(root, "other")
	for _, dir := range []string{own, other} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte("# Plan\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d := &Session{Role: "d", Workspace: own, PlansRoot: plans, PlanDir: own, PlanRepo: own}
	if got, err := d.WriteRoot(filepath.Join(own, "plan.md")); err != nil || got != own {
		t.Fatalf("bound write root=%q err=%v", got, err)
	}
	if got, err := d.WriteRoot(filepath.Join(other, "plan.md")); err != nil || got != own {
		t.Fatalf("sibling resolves to workspace, got=%q err=%v", got, err)
	}
}

func TestRepositoryRegistrationDoesNotCreateAPlan(t *testing.T) {
	registry, _, _ := testPlanRegistry(t)
	repo := t.TempDir()
	registered, created, err := registry.EnsurePlan(repo)
	if err != nil || !created {
		t.Fatalf("register = %+v created=%v err=%v", registered, created, err)
	}
	if registered.Planning {
		t.Fatalf("plain repository is planning: %+v", registered)
	}
	if _, err := os.Stat(filepath.Join(repo, "plan.md")); !os.IsNotExist(err) {
		t.Fatalf("registration created plan.md: %v", err)
	}
	if entries, err := os.ReadDir(registry.plansRoot); err != nil || len(entries) != 1 || entries[0].Name() != "repos.json" {
		t.Fatalf("profile plan storage = %+v, %v", entries, err)
	}
}

func TestPlanningIsOnlyARegularRootPlanFile(t *testing.T) {
	registry, _, _ := testPlanRegistry(t)
	repo := t.TempDir()
	registered, _, err := registry.EnsurePlan(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "plan.md"), []byte("# Root plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	values, err := ListPlans(registry.plansRoot)
	if err != nil || len(values) != 1 || values[0].ID != registered.ID || !values[0].Planning {
		t.Fatalf("repositories = %+v, %v", values, err)
	}
	if err := os.Remove(filepath.Join(repo, "plan.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, "plan.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	values, err = ListPlans(registry.plansRoot)
	if err != nil || values[0].Planning {
		t.Fatalf("directory named plan.md enabled planning: %+v, %v", values, err)
	}
}

func TestLegacyPlansMigrateCopyOnlyAndAreNeverReadAgain(t *testing.T) {
	root := t.TempDir()
	plans := filepath.Join(root, "plans")
	repoCopied := filepath.Join(root, "copied")
	repoKept := filepath.Join(root, "kept")
	missing := filepath.Join(root, "missing")
	for _, dir := range []string{repoCopied, repoKept} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repoKept, "plan.md"), []byte("# Keep root\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := []struct{ id, repo, text string }{
		{"copy-id", repoCopied, "# Copy legacy\n"},
		{"keep-id", repoKept, "# Never overwrite\n"},
		{"missing-id", missing, "# Missing legacy\n"},
	}
	for _, item := range legacy {
		dir := filepath.Join(plans, item.id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := writePlanRepo(dir, item.repo); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte(item.text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	values, err := ListPlans(plans)
	if err != nil || len(values) != 3 {
		t.Fatalf("migration = %+v, %v", values, err)
	}
	assertText := func(path, want string) {
		t.Helper()
		got, readErr := os.ReadFile(path)
		if readErr != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", path, got, readErr, want)
		}
	}
	assertText(filepath.Join(repoCopied, "plan.md"), "# Copy legacy\n")
	assertText(filepath.Join(repoKept, "plan.md"), "# Keep root\n")
	assertText(filepath.Join(plans, "copy-id", "plan.md"), "# Copy legacy\n")
	if err := os.WriteFile(filepath.Join(plans, "copy-id", "plan.md"), []byte("# Changed old copy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	values, err = ListPlans(plans)
	if err != nil {
		t.Fatal(err)
	}
	assertText(filepath.Join(repoCopied, "plan.md"), "# Copy legacy\n")
	for _, item := range values {
		if item.ID == "missing-id" && (!item.Missing || item.Planning || item.Repo != missing) {
			t.Fatalf("missing repository = %+v", item)
		}
	}
}
