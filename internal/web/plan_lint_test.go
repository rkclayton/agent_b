package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
)

func planLintServer(t *testing.T) (*Server, *session.Registry, *events.Bus, string) {
	t.Helper()
	repo := t.TempDir()
	data := t.TempDir()
	cfg := config.Defaults(repo)
	p := runnableTestProfile("fake")
	cfg.Servers = []config.Profile{p}
	cfg.Agents = []config.Agent{{Name: "Lint", B: "fake", D: "fake", Toolset: config.FullToolset()}}
	bus := events.NewBus()
	writers, err := events.NewWriters(filepath.Join(data, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writers.Close() })
	server := New(&cfg, filepath.Join(data, "harness.json"), repo, RuntimeRoots{Application: repo, Data: data, Workspace: repo}, bus)
	registry := session.NewRegistry(bus, writers, server.Profile, cfg.Run.MaxTurns, server.ConfigSnapshot)
	server.SetRegistry(registry)
	return server, registry, bus, repo
}

// Item 2bq: the plan lint runs before Go; an error disables Go with its reason
// and every finding reaches the panel.
func TestGoStateCarriesThePlanLintAndRefusesAMalformedPlan(t *testing.T) {
	server, registry, _, repo := planLintServer(t)
	plan, _, err := registry.EnsurePlan(repo)
	if err != nil {
		t.Fatal(err)
	}
	design, err := registry.CreateRole("design", "lint", "", "d", plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	planDir := design.Snapshot().PlanDir
	if err := os.WriteFile(filepath.Join(planDir, "plan.md"), []byte("# P\n\n- [ ] [[1]] one\n- [ ] [[1]] again\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "plan", "items", "1.md"), []byte("verify: echo ok\n\n# 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/plan/go?session_id="+design.ID, nil))
	var state struct {
		Enabled     bool   `json:"enabled"`
		Refusal     string `json:"refusal"`
		Diagnostics []struct {
			Severity string `json:"severity"`
			Message  string `json:"message"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatalf("%v: %s", err, response.Body)
	}
	if state.Enabled || !strings.Contains(state.Refusal, "plan lint") || len(state.Diagnostics) == 0 {
		t.Fatalf("a malformed plan must refuse Go with the lint's reason: %s", response.Body)
	}
}

func TestItemFileProposalsCarryAContractAndAVerifier(t *testing.T) {
	base := planProposal{ID: "p1", Kind: "add", Path: "plan/items/7a.md", ItemID: "7a", OldText: "# 7a\n", NewText: "# 7a\n\nAdd a line.\n"}
	if err := validatePlanProposal(base); err == nil || !strings.Contains(err.Error(), "## Contract") {
		t.Fatalf("an item without a contract and verifier must be refused: %v", err)
	}
	// A verifier below the heading is one the worker never reads (v0.68.0/W16).
	base.NewText = "# 7a\n\nverify: Test-Path CHANGELOG.md\n\n## Contract\n\n@change add a line [assumed]\n"
	if err := validatePlanProposal(base); err == nil {
		t.Fatal("a verifier the worker cannot see was accepted")
	}
	base.NewText = "verify: Test-Path CHANGELOG.md\n\n# 7a\n\n## Contract\n\n@change add a line [assumed]\n"
	if err := validatePlanProposal(base); err != nil {
		t.Fatalf("a complete item was refused: %v", err)
	}
	onPlan := planProposal{ID: "p2", Kind: "add", Path: "plan.md", ItemID: "7a", OldText: "# P\n", NewText: "# P\n- [ ] 7a add a line\n"}
	if err := validatePlanProposal(onPlan); err != nil {
		t.Fatalf("a plan.md line needs no contract: %v", err)
	}
}

// A plan.md rewrite by any route is on the page's stream within a second.
func TestPlanRewritesReachTheEventStreamWithinASecond(t *testing.T) {
	server, registry, bus, repo := planLintServer(t)
	server.PublishPlanChanges()
	t.Cleanup(func() { session.PlanChanged = nil })
	stream, cancel := bus.Subscribe()
	defer cancel()
	plan, _, err := registry.EnsurePlan(repo)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := session.UpdatePlanFile(filepath.Join(server.roots.Data, "plans", plan.ID, "plan.md"), func(current string, _ bool) (string, error) { return current + "- [x] [[1]] done\n", nil }); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	deadline := time.After(time.Second)
	for !seen["plan.updated"] {
		select {
		case event := <-stream:
			if event.SessionID == "" && strings.HasPrefix(event.Type, "plan.") {
				data := event.Data.(map[string]any)
				if data["plan_id"] != plan.ID || data["plan"] == nil {
					t.Fatalf("%s carries %v", event.Type, data)
				}
				seen[event.Type] = true
			}
		case <-deadline:
			t.Fatalf("plan events in the first second: %v", seen)
		}
	}
	if !seen["plan.created"] {
		t.Fatalf("creation was not announced: %v", seen)
	}
	t.Logf("plan.updated on the stream %v after the write", time.Since(started))
}

// Item 2fc (W16 cold review): "Build plan now?" opens a d chat bound to the
// plan, or — with no d profile — a b chat in the plan's repository, the
// planner fallback; neither sends a message.
func TestBuildPlanOpensTheBoundPlannerOrTheBFallback(t *testing.T) {
	server, registry, _, repo := planLintServer(t)
	plan, _, err := registry.EnsurePlan(repo)
	if err != nil {
		t.Fatal(err)
	}
	build := func() map[string]any {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/plans/build", strings.NewReader(`{"plan_id":"`+plan.ID+`","agent_id":"lint"}`))
		server.buildPlan(response, request)
		var body map[string]any
		_ = json.Unmarshal(response.Body.Bytes(), &body)
		if response.Code >= 300 {
			t.Fatalf("build: %d %s", response.Code, response.Body)
		}
		return body
	}
	first := build()
	item, _ := registry.Get(first["session_id"].(string))
	if item.Role != "d" || item.PlanID != plan.ID || len(item.MessagesCopy()) != 0 {
		t.Fatalf("with a d profile: role=%s plan=%s messages=%d", item.Role, item.PlanID, len(item.MessagesCopy()))
	}
	if again := build(); again["session_id"] != first["session_id"] || again["reused"] != true {
		t.Fatalf("an open planning chat is reused: %v", again)
	}
	cfg := server.ConfigSnapshot()
	cfg.Agents[0].D = ""
	*server.cfg = cfg
	item.Close()
	fallback := build()
	chat, _ := registry.Get(fallback["session_id"].(string))
	if fallback["fallback"] != true || chat.Role != "b" || !strings.EqualFold(filepath.Clean(chat.Workspace), filepath.Clean(plan.Repo)) || len(chat.MessagesCopy()) != 0 {
		t.Fatalf("with no d profile: %v role=%s workspace=%s", fallback, chat.Role, chat.Workspace)
	}
	// v0.70.1 cold review: a second Yes reuses that chat rather than making another.
	if again := build(); again["session_id"] != fallback["session_id"] || again["reused"] != true {
		t.Fatalf("a second Yes with no d profile made another chat: %v", again)
	}
}
