package agent

import (
	"context"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
)

func unattendedGate(t *testing.T, on bool) (*Gate, *events.Bus) {
	t.Helper()
	bus := events.NewBus()
	cfg := config.Defaults(t.TempDir())
	cfg.Approval.Unattended = on
	return NewGate(bus, func() config.Config { return cfg }), bus
}

// Item 5f (v1.2.5): with the switch on, a worker raises no card on any of the
// four paths. Each is recorded as a boundary failure and the run moves on.
func TestUnattendedRefusesEveryCardKindAndRecordsIt(t *testing.T) {
	gate, bus := unattendedGate(t, true)
	stream, stop := bus.Subscribe()
	defer stop()

	worker := &session.Session{ID: "w1", Role: "c"}
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

// A chat the operator is typing in stays attended whatever the switch says: he
// is there, and asking him is the point rather than an interruption.
func TestUnattendedLeavesAnOperatorChatAlone(t *testing.T) {
	gate, _ := unattendedGate(t, true)
	for _, role := range []string{"b", "d"} {
		if gate.unattended(&session.Session{ID: "s1", Role: role}) {
			t.Fatalf("role %q must stay attended", role)
		}
	}
	if !gate.unattended(&session.Session{ID: "w1", Role: "c"}) {
		t.Fatal("a worker must be unattended when the switch is on")
	}
}

// With the switch off, nothing about the gate changes.
func TestAttendedIsUnchanged(t *testing.T) {
	gate, _ := unattendedGate(t, false)
	if gate.unattended(&session.Session{ID: "w1", Role: "c"}) {
		t.Fatal("the switch is off; a worker is attended")
	}
}
