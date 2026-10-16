package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
)

func unattendedGate(t *testing.T, on bool) (*Gate, *events.Bus) {
	t.Helper()
	bus := events.NewBus()
	cfg := config.Defaults(t.TempDir())
	_ = on
	return NewGate(bus, func() config.Config { return cfg }), bus
}

// Item 5f (v1.2.5): with the switch on, a worker raises no card on any of the
// four paths. Each is recorded as a boundary failure and the run moves on.
func TestUnattendedRefusesEveryCardKindAndRecordsIt(t *testing.T) {
	gate, bus := unattendedGate(t, true)
	stream, stop := bus.Subscribe()
	defer stop()

	worker := &session.Session{ID: "w1", Role: "c", Unattended: true}
	ctx := context.Background()
	for _, item := range []struct {
		name string
		call func() (string, error)
		want string
	}{
		{"policy", func() (string, error) { return gate.WaitPolicyDecision(ctx, worker, "r1", "c1", "shell", nil) }, "policy approval"},
		{"escape", func() (string, error) {
			return gate.WaitBoundaryDecision(ctx, worker, "r1", "c2", "shell.operator_override", nil)
		}, "identity escalation"},
		{"cycle", func() (string, error) { return gate.WaitCycleDecision(ctx, worker, "r1", "c3", nil) }, "cycle decision"},
		{"registration", func() (string, error) {
			return gate.WaitPolicyDecision(ctx, worker, "r1", "c4", "plan registration", nil)
		}, "plan registration"},
	} {
		decision, err := item.call()
		if err != nil || decision != "deny" {
			t.Fatalf("%s: decision=%q err=%v", item.name, decision, err)
		}
	}
	// Nothing asked: not one approval.required reached the bus.
	seen := 0
	for draining := true; draining; {
		select {
		case event := <-stream:
			if event.Type == events.ApprovalRequired {
				seen++
			}
		default:
			draining = false
		}
	}
	if seen != 0 {
		t.Fatalf("unattended raised %d cards", seen)
	}
	hits := worker.BoundaryHits()
	if len(hits) != 4 {
		t.Fatalf("boundary hits=%v", hits)
	}
	for _, want := range []string{"policy approval", "identity escalation", "cycle decision", "plan registration"} {
		found := false
		for _, hit := range hits {
			if strings.Contains(hit, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("no boundary hit named %q: %v", want, hits)
		}
	}
	for _, hit := range hits {
		if !strings.HasPrefix(hit, "[!] boundary: ") {
			t.Fatalf("hit is not in the item's form: %q", hit)
		}
	}
}

func TestUnattendedIsOnlyTheSessionChoice2vf(t *testing.T) {
	gate, _ := unattendedGate(t, true)
	for _, role := range []string{"b", "d"} {
		if gate.unattended(&session.Session{ID: "s1", Role: role}) {
			t.Fatalf("role %q became unattended without the choice", role)
		}
	}
	if !gate.unattended(&session.Session{ID: "w1", Role: "c", Unattended: true}) {
		t.Fatal("the chosen unattended mode was ignored")
	}
}

// With the switch off, nothing about the gate changes.
func TestAttendedIsUnchanged(t *testing.T) {
	gate, _ := unattendedGate(t, false)
	if gate.unattended(&session.Session{ID: "w1", Role: "c"}) {
		t.Fatal("the switch is off; a worker is attended")
	}
}

func TestScheduledRunAsksUnlessUnattendedWasChosen2vf(t *testing.T) {
	gate, bus := unattendedGate(t, false)
	stream, stop := bus.Subscribe()
	defer stop()
	item := &session.Session{ID: "scheduled", Role: "b", Origin: "scheduled"}
	result := make(chan string, 1)
	go func() {
		decision, _ := gate.WaitPolicyDecision(context.Background(), item, "r1", "c1", "shell", nil)
		result <- decision
	}()
	for {
		select {
		case event := <-stream:
			if event.Type == events.ApprovalRequired {
				if err := gate.Decide(item.ID, "c1", "approve"); err != nil {
					t.Fatal(err)
				}
				if decision := <-result; decision != "approve" {
					t.Fatalf("decision=%q", decision)
				}
				return
			}
		case <-time.After(time.Second):
			t.Fatal("scheduled attended job did not raise a card")
		}
	}
}

// Item 2q2: a card in a b chat that nobody answers is refused after ten minutes,
// recorded as a boundary hit, and the run goes on; one answered before then
// behaves as today. A fast clock stands in for the ten minutes.
func TestAnUnansweredCardWaitsUntilTheRunIsStopped2vf(t *testing.T) {
	gate, bus := unattendedGate(t, false)
	stream, stop := bus.Subscribe()
	defer stop()
	chat := &session.Session{ID: "s51", Role: "b"}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	decision, err := gate.WaitPolicyDecision(ctx, chat, "r1", "c1", "shell", map[string]any{"command": "Get-Process"})
	if err == nil || decision != "dismissed" || time.Since(started) < 125*time.Millisecond {
		t.Fatalf("an unanswered card did not wait for run stop: %q %v after %s", decision, err, time.Since(started))
	}
	if hits := chat.BoundaryHits(); len(hits) != 0 {
		t.Fatalf("an unanswered card recorded a refusal: %v", hits)
	}
	for {
		select {
		case event := <-stream:
			data, _ := event.Data.(map[string]any)
			if event.Type == events.ApprovalDecided && data["unanswered"] == true {
				t.Fatal("an unanswered timer decision was published")
			}
		default:
			goto drained
		}
	}
drained:
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = gate.Decide(chat.ID, "c2", "approve")
	}()
	if decision, err := gate.WaitPolicyDecision(context.Background(), chat, "r1", "c2", "shell", nil); decision != "approve" || err != nil {
		t.Fatalf("a card answered in time must keep its answer: %q %v", decision, err)
	}
}
