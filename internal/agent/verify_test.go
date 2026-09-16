package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/session"
)

// An item's verifier runs as the worker through the ordinary shell path: under
// a stricter approval mode it raises the same card, tagged for the worker's plan,
// and the run state the stopped run left is put back once it has answered.
func TestVerifierRunsThroughTheWorkersGateAndRestoresRunState(t *testing.T) {
	cfg := config.Defaults(t.TempDir())
	cfg.Approval.Mode = config.ApprovalModeMutating
	cfg.Shell.ServiceAccount.Enabled = true
	tool := &shellPolicyTool{}
	runner, s, bus := shellPolicyRunner(t, cfg, tool)
	s.ID, s.Role, s.PlanID = "c1", "c", "p1"
	s.Run = session.RunState{Status: "stopped"}
	eventCh, unsubscribe := bus.Subscribe()
	defer unsubscribe()

	type result struct {
		ok     bool
		output string
	}
	done := make(chan result, 1)
	go func() {
		ok, output := runner.Verify(context.Background(), s, "go test ./...")
		done <- result{ok, output}
	}()
	event := nextApprovalEvent(t, eventCh)
	data, _ := event.Data.(map[string]any)
	if event.SessionID != "c1" || data["name"] != "shell" || data["role"] != "c" || data["plan_id"] != "p1" {
		t.Fatalf("verifier approval = %s %v", event.SessionID, data)
	}
	callID, _ := data["call_id"].(string)
	if err := runner.gate.Decide("c1", callID, "once"); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if !got.ok || tool.normalCalls != 1 || tool.normalArgs["command"] != "go test ./..." {
		t.Fatalf("verify = %+v calls=%d args=%v", got, tool.normalCalls, tool.normalArgs)
	}
	if status := s.Snapshot().Run.Status; status != "stopped" {
		t.Fatalf("run state after the verifier = %q, want the stopped run's state back", status)
	}
}

// Both prompt lines are present exactly once, and the worker's brief renders
// the item's verifier byte-identically every time.
func TestVerifierPromptLinesArePresentAndStable(t *testing.T) {
	planner, err := os.ReadFile("../../prompts/planner.md")
	if err != nil {
		t.Fatal(err)
	}
	plannerLine := "Every item file names a `verify:` command; the worker marks an item done only when it exits 0."
	if strings.Count(string(planner), plannerLine) != 1 {
		t.Fatalf("planner.md carries the verifier line %d times", strings.Count(string(planner), plannerLine))
	}
	renderer := &PromptRenderer{text: "system"}
	if err := renderer.LoadPlanner("../../prompts/planner.md"); err != nil {
		t.Fatal(err)
	}
	if got := renderer.Render(&config.Profile{}, &session.Session{Role: "d"}, nil, ""); !strings.Contains(got, plannerLine) {
		t.Fatalf("the planner's session block does not carry the line:\n%s", got)
	}
	if err := renderer.LoadWorker("../../prompts/worker.md"); err != nil {
		t.Fatal(err)
	}
	worker := &session.Session{Role: "c"}
	worker.SetWorkerJob(session.WorkerJob{ItemID: "2aa", Intent: "write NOTICE", Verify: "test -f NOTICE", Repo: `C:\repo`})
	first, second := renderer.renderWorker(worker), renderer.renderWorker(worker)
	if first != second {
		t.Fatal("the worker brief is not byte-stable")
	}
	line := "VERIFY: test -f NOTICE — the harness runs it after you stop; the item is done only if it exits 0."
	if strings.Count(first, line) != 1 || strings.Contains(first, "{{item_verify}}") {
		t.Fatalf("worker brief:\n%s", first)
	}
}
