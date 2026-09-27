package config

import (
	"strings"
	"testing"
)

// Item 2ih (a): assumption by size sets the STARTING POINT, and an unknown size
// assumes nothing.
func TestReasoningDefaultBySize2ih(t *testing.T) {
	for _, probe := range []struct {
		name       string
		parameters uint64
		enabled    bool
		applied    bool
	}{
		{"a 0.8B model", 800_000_000, false, true},
		{"a 4B model, exactly at the line", 4_000_000_000, false, true},
		{"just over the line", 4_000_000_001, true, true},
		{"a 30B model", 30_000_000_000, true, true},
		// An API connection has no artifact and not every GGUF carries the key.
		// Guessing here would set a model's most consequential switch from
		// nothing at all.
		{"unknown", 0, false, false},
	} {
		enabled, applied := ReasoningDefaultForSize(probe.parameters)
		if applied != probe.applied {
			t.Errorf("%s: applied=%v, want %v", probe.name, applied, probe.applied)
		}
		if applied && enabled != probe.enabled {
			t.Errorf("%s: enabled=%v, want %v", probe.name, enabled, probe.enabled)
		}
	}
}

// Item 2ih (c): the arm with the higher pass rate wins, OFF on a tie or within
// one brief, and OFF whenever the on-arm produced any empty reply.
func TestDecideReasoning2ih(t *testing.T) {
	arm := func(passed, empty, p95 int) MeasurementArm {
		return MeasurementArm{Passed: passed, Total: 10, EmptyReplies: empty, ReasoningP95: p95, Ran: true}
	}
	for _, probe := range []struct {
		name        string
		on, off     MeasurementArm
		wantEnabled bool
		wantIn      string
	}{
		{"thinking clearly better", arm(9, 0, 0), arm(6, 0, 0), true, "9/10 with thinking vs 6/10"},
		{"a tie goes off", arm(7, 0, 0), arm(7, 0, 0), false, "not enough to justify"},
		{"one brief is not enough", arm(8, 0, 0), arm(7, 0, 0), false, "not enough to justify"},
		{"two briefs is", arm(9, 0, 0), arm(7, 0, 0), true, "9/10 with thinking vs 7/10"},
		{"thinking worse", arm(4, 0, 0), arm(8, 0, 0), false, "not enough to justify"},
		// The veto: ONE empty reply is enough, even when thinking wins on passes
		// by a mile. A turn that finishes with reasoning and no reply is the
		// failure a pass count hides.
		{"one empty reply vetoes a clear win", arm(10, 1, 0), arm(5, 0, 0), false, "1 empty reply with thinking"},
		{"several empty replies", arm(10, 3, 0), arm(5, 0, 0), false, "3 empty replies with thinking"},
	} {
		got := DecideReasoning(probe.on, probe.off)
		if got.Enabled != probe.wantEnabled {
			t.Errorf("%s: enabled=%v, want %v (line: %s)", probe.name, got.Enabled, probe.wantEnabled, got.Line)
		}
		if !strings.Contains(got.Line, probe.wantIn) {
			t.Errorf("%s: the line does not say %q: %s", probe.name, probe.wantIn, got.Line)
		}
		// (c): every write is ONE LINE in the result, and it always names the
		// numbers it decided on.
		if !strings.HasPrefix(got.Line, "set reasoning o") {
			t.Errorf("%s: the line does not read as a setting that was written: %s", probe.name, got.Line)
		}
	}
}

// (c): the cap comes from what was observed, and is left alone when the arm
// could not report it.
func TestTheReasoningCapComesFromTheMeasurement2ih(t *testing.T) {
	on := MeasurementArm{Passed: 9, Total: 10, ReasoningP95: 1450, Ran: true}
	off := MeasurementArm{Passed: 6, Total: 10, Ran: true}
	decision := DecideReasoning(on, off)
	if !decision.Enabled || decision.ReasoningCap != 1450 {
		t.Fatalf("cap=%d enabled=%v, want 1450 and on", decision.ReasoningCap, decision.Enabled)
	}
	if !strings.Contains(decision.Line, "1450") {
		t.Errorf("the line does not show its work: %s", decision.Line)
	}
	// Not observable: the cap is NOT written, so the operator's own value
	// survives — @keep says a later Measure overwrites only what it measured.
	on.ReasoningP95 = 0
	if decision := DecideReasoning(on, off); decision.ReasoningCap != 0 {
		t.Errorf("a cap of %d was invented from an unobserved percentile", decision.ReasoningCap)
	}
}

// (f): a measurement that cannot run says so and writes nothing.
func TestAMeasurementThatCouldNotRunWritesNothing2ih(t *testing.T) {
	decision := DecideReasoning(MeasurementArm{Ran: false}, MeasurementArm{Passed: 8, Total: 10, Ran: true})
	if decision.Enabled {
		t.Error("reasoning was enabled from an arm that never ran")
	}
	if decision.ReasoningCap != 0 {
		t.Error("a cap was written from an arm that never ran")
	}
	if !strings.Contains(decision.Line, "did not run") {
		t.Errorf("the line does not say the measurement failed: %s", decision.Line)
	}
}
