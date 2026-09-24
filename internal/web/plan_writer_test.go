package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"harness/internal/agent"
	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
	"harness/internal/worker"
)

type planWriterFixture struct {
	server   *Server
	registry *session.Registry
	runner   *agent.Runner
	design   *session.Session
	planDir  string
	data     string
}

func newPlanWriterFixture(t *testing.T) planWriterFixture {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "Repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	cfg := config.Defaults(repo)
	cfg.Context.Accounting = "estimated"
	cfg.Connections = []config.Connection{runnableTestConnection("fake")}
	cfg.Agents = []config.Agent{{Name: "Writer", B: "fake", D: "fake", Toolset: config.FullToolset()}}
	bus := events.NewBus()
	writers, err := events.NewWriters(filepath.Join(data, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	bus.SetSink(writers.Write)
	server := New(&cfg, filepath.Join(data, "harness.json"), repo, RuntimeRoots{Application: repo, Data: data, Workspace: repo}, bus)
	registry := session.NewRegistry(bus, writers, server.Connection, cfg.Run.MaxTurns, server.ConfigSnapshot)
	server.SetRegistry(registry)
	plan, _, err := registry.EnsurePlan(repo)
	if err != nil {
		t.Fatal(err)
	}
	design, err := registry.CreateRole("design", "writer", "", "d", plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	toolset := tools.New(tools.NewEditFile(tools.NewFileCoordinator(session.NewWorkspaceRegistry(), registry.Label, bus)))
	toolset.Configure(cfg)
	runner := agent.NewRunner(bus, toolset, &agent.PromptRenderer{}, server.Connection, server.ConfigSnapshot)
	server.SetRuntime(nil, runner, nil)
	return planWriterFixture{server: server, registry: registry, runner: runner, design: design, planDir: design.PlanDir, data: data}
}

// A marker change and a tray accept on the same plan.md in the same second both
// land: the worker's marker and the planner's accepted edit go through one
// serialised writer, so neither read-modify-write overwrites the other.
func TestMarkerAndTrayAcceptOnTheSamePlanBothLand(t *testing.T) {
	fixture := newPlanWriterFixture(t)
	planPath := filepath.Join(fixture.planDir, "plan.md")
	const rounds = 60
	lines := []string{"# Repo"}
	for index := 0; index < rounds; index++ {
		lines = append(lines, fmt.Sprintf("- [ ] [[%d]] marker item %d", index+1, index+1))
	}
	lines = append(lines, "tray anchor")
	if err := os.WriteFile(planPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.design.SetPlanPage(true)
	plan := &worker.Plan{Dir: fixture.planDir}

	var group sync.WaitGroup
	errs := make(chan error, 2*rounds)
	group.Add(2)
	go func() {
		defer group.Done()
		for index := 0; index < rounds; index++ {
			text, err := plan.Read()
			if err != nil {
				errs <- err
				return
			}
			for _, item := range worker.Parse(text) {
				if item.ID == fmt.Sprint(index+1) {
					if err := plan.Mark(item, "x", ""); err != nil {
						errs <- err
					}
				}
			}
		}
	}()
	go func() {
		defer group.Done()
		for index := 0; index < rounds; index++ {
			old := "tray anchor"
			if index > 0 {
				old = fmt.Sprintf("tray anchor %d", index)
			}
			outcome := fixture.runner.AcceptPlanEdit(context.Background(), fixture.design, "plan.md", old, fmt.Sprintf("tray anchor %d", index+1))
			if !outcome.OK {
				errs <- fmt.Errorf("accept %d: %s", index, outcome.Content)
			}
		}
	}()
	group.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	final, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(final)
	if !strings.Contains(text, fmt.Sprintf("tray anchor %d\n", rounds)) {
		t.Fatalf("an accepted edit was lost:\n%s", text)
	}
	for index := 0; index < rounds; index++ {
		if !strings.Contains(text, fmt.Sprintf("- [x] [[%d]] marker item %d\n", index+1, index+1)) {
			t.Fatalf("marker %d was lost:\n%s", index+1, text)
		}
	}
}

// Registering a plan whose repository is inside the plans folder is refused
// with the reason; a plan that already has one shows the reason on its panel and
// Go refuses rather than starting a worker that could only be refused.
func TestRepoInsidePlansIsRefusedAndAnExistingOneShowsWhy(t *testing.T) {
	fixture := newPlanWriterFixture(t)
	plansRoot := filepath.Dir(fixture.planDir)
	inside := filepath.Join(plansRoot, "nested-repo")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.registry.EnsurePlan(inside); err == nil || !strings.Contains(err.Error(), "inside the plans folder") {
		t.Fatalf("registration of a repo inside the plans root: err=%v", err)
	}

	// An existing plan whose manifest already names such a repository.
	manifest, _ := json.Marshal(map[string]string{"repo": inside})
	if err := os.WriteFile(filepath.Join(fixture.planDir, "plan.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/plan?session_id="+fixture.design.ID, nil)
	recorder := httptest.NewRecorder()
	fixture.server.planSurface(recorder, request)
	var surface struct {
		Refusal string `json:"refusal"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &surface); err != nil {
		t.Fatalf("%v: %s", err, recorder.Body.String())
	}
	if !strings.Contains(surface.Refusal, "inside the plans folder") {
		t.Fatalf("panel refusal = %q", surface.Refusal)
	}
	if reason := session.RepoInsidePlans(plansRoot, filepath.Dir(plansRoot)); reason != "" {
		t.Fatalf("a repository that merely contains the plans folder was refused: %s", reason)
	}
}
