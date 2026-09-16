package worker

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"harness/internal/agent"
	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
)

// gatedSubmitter stands in for a run whose first tool needs the operator: it
// waits on the real approval gate, exactly where a run loop would, and stops
// done or denied according to the decision it is given.
type gatedSubmitter struct {
	mu      sync.Mutex
	bus     *events.Bus
	gate    *agent.Gate
	session *session.Session
	cycle   bool
	stops   int
	cancel  []context.CancelFunc
}

func (g *gatedSubmitter) Submit(parent context.Context, sessionID, text string) (string, error) {
	ctx, cancel := context.WithCancel(parent)
	g.mu.Lock()
	g.cancel = append(g.cancel, cancel)
	g.mu.Unlock()
	runID := "run-gated"
	go func() {
		var decision string
		if g.cycle {
			decision, _ = g.gate.WaitCycleDecision(ctx, g.session, runID, "cycle-1", map[string]any{"tool": "read_file"})
		} else {
			decision, _ = g.gate.WaitBoundaryDecision(ctx, g.session, runID, "call-1", "read_file.operator_override", map[string]any{"path": `C:\repo\a.txt`, "reason": "the restricted account cannot read it"})
		}
		reason := "done"
		switch decision {
		case "session", "once", "continue":
		case "dismissed":
			return
		default:
			reason = "tool_errors"
		}
		g.bus.Publish(events.New(events.RunStopped, g.session.ID, runID, map[string]any{"reason": reason}))
	}()
	return runID, nil
}

func (g *gatedSubmitter) Stop(sessionID string, all bool) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stops++
	for _, cancel := range g.cancel {
		cancel()
	}
	return 1
}

func gatedWorker(t *testing.T, cycle bool) (*events.Bus, *agent.Gate, *session.Session, *gatedSubmitter, *Plan) {
	t.Helper()
	bus := events.NewBus()
	gate := agent.NewGate(bus, func() config.Config { return config.Config{} })
	worker := &session.Session{ID: "c1", Role: "c", PlanID: "p1"}
	fake := &gatedSubmitter{bus: bus, gate: gate, session: worker, cycle: cycle}
	plan := newProofPlan(t)
	text, _ := plan.Read()
	_ = writePlan(plan, strings.Replace(text, "- [ ] 2ab seeded stuck item\n- [ ] 2ac seeded question item\n", "", 1))
	return bus, gate, worker, fake, plan
}

func writePlan(plan *Plan, text string) error { return os.WriteFile(plan.Path(), []byte(text), 0o600) }

// awaitApproval returns the first approval.required the worker raises.
func awaitApproval(t *testing.T, stream chan events.Event) events.Event {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case event := <-stream:
			if event.Type == events.ApprovalRequired {
				return event
			}
		case <-timeout:
			t.Fatal("the worker never raised an approval")
		}
	}
}

// A worker whose first tool needs the operator raises the ordinary card, tagged
// with its plan so the design thread draws it and the notification points
// there; answering it against the worker's session resumes the worker.
func TestWorkerApprovalIsRaisedForItsPlanAndAnsweringItResumes(t *testing.T) {
	for _, cycle := range []bool{false, true} {
		bus, gate, worker, fake, plan := gatedWorker(t, cycle)
		stream, release := bus.Subscribe()
		driver := verified(New(bus, fake, nil))
		result := make(chan Summary, 1)
		go func() {
			summary, _ := driver.Go(context.Background(), worker, plan, `C:\repo`)
			result <- summary
		}()

		event := awaitApproval(t, stream)
		release()
		data, _ := event.Data.(map[string]any)
		if event.SessionID != worker.ID || data["role"] != "c" || data["plan_id"] != "p1" {
			t.Fatalf("cycle=%v: approval is not the worker's for plan p1: session=%s data=%v", cycle, event.SessionID, data)
		}
		human, _ := data["human"].(events.HumanNotice)
		if human.Happened == "" || human.HarnessAction == "" {
			t.Fatalf("cycle=%v: approval carries no human sentence: %#v", cycle, data["human"])
		}
		if worker.Snapshot().Run.Status != "paused" {
			t.Fatalf("cycle=%v: worker run is %q while waiting, want paused", cycle, worker.Snapshot().Run.Status)
		}
		callID, decision := "call-1", "session"
		if cycle {
			callID, decision = "cycle-1", "continue"
		}
		if err := gate.Decide(worker.ID, callID, decision); err != nil {
			t.Fatalf("cycle=%v: deciding against the worker's session: %v", cycle, err)
		}
		select {
		case summary := <-result:
			if summary.Done != 1 || summary.Stuck != 0 {
				t.Fatalf("cycle=%v: summary after the answer = %+v", cycle, summary)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("cycle=%v: the worker did not resume after the answer", cycle)
		}
	}
}

// An approval nobody answers expires with the item: the item is stuck as
// waiting for approval, and the run is ended rather than left on the card.
func TestUnansweredWorkerApprovalExpiresAsWaitedForApproval(t *testing.T) {
	bus, _, worker, fake, plan := gatedWorker(t, false)
	driver := verified(New(bus, fake, nil))
	driver.deadline = 150 * time.Millisecond
	summary, err := driver.Go(context.Background(), worker, plan, `C:\repo`)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := plan.Read()
	if !strings.Contains(text, "- [!] 2aa first item  — stuck: waited for approval") {
		t.Fatalf("plan.md after expiry:\n%s", text)
	}
	if summary.Stuck != 1 || summary.Done != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	fake.mu.Lock()
	stops := fake.stops
	fake.mu.Unlock()
	if stops == 0 {
		t.Fatal("the expired item's run was never stopped")
	}
}
