package agent

import (
	"testing"
)

// Item 2la (d). The acceptance line is "a repeated identical failure stops the
// run with the detector's reason instead of cycling", and the run guards already
// do exactly that — so this pins it rather than inventing a second mechanism on a
// guessed threshold, which this order's DO-NOT list forbids. The seven
// internal/progress detectors stay in shadow because GuessedThresholds is the only
// source for every one of them; the report names them.
func TestAnIdenticalRepeatedFailureStopsTheRun2la(t *testing.T) {
	guards := newRunGuards(8, 3)
	args := map[string]any{"language": "python", "source": "print(1)"}
	failure := "error: fork/exec C:\\Users\\x\\python.exe: The directory name is invalid"
	if reason, _, _ := guards.Observe("c1", "run_script", args, failure, false); reason != "" {
		t.Fatalf("the first failure stopped the run: %s", reason)
	}
	reason, detail, prior := guards.Observe("c2", "run_script", args, failure, false)
	if reason != "cycle" {
		t.Fatalf("an identical repeated failure did not stop the run: reason=%q", reason)
	}
	if detail == "" || prior != "c1" {
		t.Fatalf("the stop does not name what it observed: detail=%q prior=%q", detail, prior)
	}
}

// A different result is progress, and is not stopped.
func TestADifferentResultIsNotACycle2la(t *testing.T) {
	guards := newRunGuards(8, 3)
	args := map[string]any{"language": "python", "source": "print(1)"}
	if reason, _, _ := guards.Observe("c1", "run_script", args, "1", true); reason != "" {
		t.Fatalf("a first call stopped the run: %s", reason)
	}
	if reason, _, _ := guards.Observe("c2", "run_script", args, "2", true); reason != "" {
		t.Fatalf("a different result was treated as a cycle: %s", reason)
	}
}
