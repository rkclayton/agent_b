package worker

import (
	"context"
	"testing"
	"time"

	"harness/internal/events"
	"harness/internal/session"
)

// queuedSubmitter leaves its run queued behind the model for a while, then
// starts it and never stops it.
type queuedSubmitter struct {
	bus     *events.Bus
	session *session.Session
	queued  time.Duration
}

func (q *queuedSubmitter) Submit(_ context.Context, sessionID, _ string) (string, error) {
	q.session.SetRun(session.RunState{Status: "queued", RunID: "r1"})
	go func() {
		time.Sleep(q.queued)
		q.session.SetRun(session.RunState{Status: "running", RunID: "r1"})
		q.bus.Publish(events.New(events.RunStarted, sessionID, "r1", map[string]any{"run_id": "r1"}))
	}()
	return "r1", nil
}

func (q *queuedSubmitter) Stop(string, bool) int { return 1 }

// v0.69.0/W16 cold review: an item queued behind its model profile (item 2fc)
// has not started, so its wall clock does not run while it waits.
func TestAQueuedItemsClockStartsWhenItsRunStarts(t *testing.T) {
	bus := events.NewBus()
	worker := &session.Session{ID: "c1", Role: "c", PlanID: "p1"}
	driver := New(bus, &queuedSubmitter{bus: bus, session: worker, queued: 600 * time.Millisecond}, nil)
	driver.deadline = 150 * time.Millisecond
	started := time.Now()
	outcome := driver.runItem(context.Background(), worker, Item{Text: "item", ID: "1"})
	if elapsed := time.Since(started); elapsed < 700*time.Millisecond {
		t.Fatalf("the item timed out after %v while its run was still queued: %+v", elapsed, outcome)
	}
	if outcome.Reason != "worker wall clock" {
		t.Fatalf("outcome = %+v", outcome)
	}
}
