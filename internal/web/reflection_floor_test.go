package web

import (
	"testing"

	"harness/internal/config"
	"harness/internal/telemetry"
)

// Item 2ls: the floor, and the run it must never skip.
//
// This is item 2iy's measurement being spent rather than re-taken. 2iy found
// that reflection costs one model call per finished run, that seven of eleven
// runs made two calls or fewer, and that for those the cost doubled. The floor
// is the smallest change that spends that finding, and (d) is the part that
// keeps it honest: the runs worth reading about are the ones that went wrong,
// and they are often the shortest.
func floorServer(t *testing.T, modelCalls, toolCalls int) *Server {
	t.Helper()
	cfg := config.Config{Reflection: config.Reflection{
		Floor: config.ReflectionFloor{ModelCalls: modelCalls, ToolCalls: toolCalls},
	}}
	return &Server{cfg: &cfg}
}

func TestTheFloorSkipsASmallCleanRun2ls(t *testing.T) {
	server := floorServer(t, 3, 1)
	skip, counts := server.belowReflectionFloor(map[string]any{
		"reason": "done", "model_calls": 1, "tool_calls": 0,
	})
	if !skip {
		t.Fatal("a one-call run with no tool calls cleared a floor of three and one")
	}
	// (c): the accounting names the counts that skipped it, and the floor that
	// did the skipping, so a wrongly-skipped run is findable rather than absent.
	for _, key := range []string{"model_calls", "tool_calls", "floor_model_calls", "floor_tool_calls"} {
		if _, present := counts[key]; !present {
			t.Errorf("the accounting line omits %s", key)
		}
	}
}

// (a): three model calls OR one tool call, whichever is reached first. Either
// one clears it on its own.
func TestEitherCountClearsTheFloor2ls(t *testing.T) {
	server := floorServer(t, 3, 1)
	for _, probe := range []struct {
		name                  string
		modelCalls, toolCalls int
		wantSkip              bool
	}{
		{"nothing at all", 0, 0, true},
		{"one model call", 1, 0, true},
		{"two model calls", 2, 0, true},
		{"three model calls", 3, 0, false},
		{"one tool call clears it alone", 1, 1, false},
		{"a big run", 40, 12, false},
	} {
		skip, _ := server.belowReflectionFloor(map[string]any{
			"reason": "done", "model_calls": probe.modelCalls, "tool_calls": probe.toolCalls,
		})
		if skip != probe.wantSkip {
			t.Errorf("%s (%d model, %d tool): skip=%v, want %v", probe.name, probe.modelCalls, probe.toolCalls, skip, probe.wantSkip)
		}
	}
}

// Item 2ls (d), and the reason the floor is defensible at all: A SMALL RUN THAT
// ENDED BADLY STILL REFLECTS. A two-call run that failed is exactly the one
// somebody goes back to read, and a floor that skipped it would be saving money
// on the only runs worth the summary.
func TestASmallRunThatEndedBadlyStillReflects2ls(t *testing.T) {
	server := floorServer(t, 3, 1)
	for _, reason := range []string{
		"tool_errors", "model_error", "model_unreachable", "wall_clock", "turn_ceiling",
		"tool_budget", "context_exhausted", "context_ceiling", "length", "cycle",
		"reply_empty", "reply_empty_reasoning_shown", "announced_action_and_stopped",
		"connection_not_runnable", "aborted_mid_tool", "aborted_mid_model", "aborted_mid_run",
	} {
		skip, counts := server.belowReflectionFloor(map[string]any{
			"reason": reason, "model_calls": 1, "tool_calls": 0,
		})
		if skip {
			t.Errorf("a one-call run that stopped on %q was skipped by the floor", reason)
		}
		if counts["kept_because"] != reason {
			t.Errorf("%q: the accounting does not say why it was kept: %v", reason, counts["kept_because"])
		}
	}
	// The negative control: the ordinary reasons ARE skippable, or the clause
	// above would be vacuous and the floor would never skip anything.
	for _, reason := range []string{"done", "user_stop", "cancellation_requested", "mailbox_stop"} {
		if skip, _ := server.belowReflectionFloor(map[string]any{
			"reason": reason, "model_calls": 1, "tool_calls": 0,
		}); !skip {
			t.Errorf("a one-call run that stopped on %q was not skipped, so the floor skips nothing", reason)
		}
	}
}

// Zero disables. A floor of nothing is the behaviour before this item, which is
// what makes the row in Settings a real control rather than a display.
func TestAZeroFloorSummarisesEverything2ls(t *testing.T) {
	if skip, _ := floorServer(t, 0, 0).belowReflectionFloor(map[string]any{
		"reason": "done", "model_calls": 0, "tool_calls": 0,
	}); skip {
		t.Fatal("a floor of zero and zero skipped a run")
	}
	// One half off, the other still enforced.
	if skip, _ := floorServer(t, 0, 2).belowReflectionFloor(map[string]any{
		"reason": "done", "model_calls": 99, "tool_calls": 1,
	}); !skip {
		t.Fatal("with the model half off, one tool call cleared a floor of two")
	}
	if skip, _ := floorServer(t, 3, 0).belowReflectionFloor(map[string]any{
		"reason": "done", "model_calls": 3, "tool_calls": 0,
	}); skip {
		t.Fatal("with the tool half off, three model calls did not clear a floor of three")
	}
}

// The counts arrive off the wire as float64 from JSON and as int in the
// process. Both must read, or the floor works in tests and not in the product —
// which is the same named-type trap item 2ji was bitten by twice.
func TestTheCountsReadInEitherForm2ls(t *testing.T) {
	server := floorServer(t, 3, 1)
	fromJSON := map[string]any{"reason": "done", "model_calls": float64(3), "tool_calls": float64(0)}
	if skip, _ := server.belowReflectionFloor(fromJSON); skip {
		t.Error("three model calls as float64 did not clear the floor")
	}
	fromProcess := map[string]any{"reason": "done", "model_calls": 3, "tool_calls": 0}
	if skip, _ := server.belowReflectionFloor(fromProcess); skip {
		t.Error("three model calls as int did not clear the floor")
	}
	// And a run whose event carries no counts at all — every run journalled
	// before item 2ls — is treated as below the floor only if its reason is
	// ordinary, which is the same rule as any other small run.
	if skip, _ := server.belowReflectionFloor(map[string]any{"reason": "done"}); !skip {
		t.Error("a run with no counts cleared the floor")
	}
	if skip, _ := server.belowReflectionFloor(map[string]any{"reason": "tool_errors"}); skip {
		t.Error("a run with no counts that ended badly was skipped")
	}
}

// Item 2lx (e): THE TWO COUNTS TELEMETRY SENDS ARE THE TWO COUNTS THE FLOOR
// READ, and this proves it by giving one payload to both readers.
//
// The reason to prove it here rather than in either package is that a
// disagreement between them would be invisible from inside either one: the floor
// would skip on numbers telemetry never saw, or telemetry would report a run
// size the harness never acted on, and each would look correct alone. One
// payload, two readers, the same integers.
func TestTelemetrySendsTheCountsTheFloorRead2lx(t *testing.T) {
	// A run that the floor SKIPS -- the interesting direction, because a skipped
	// run makes no model call and is exactly the run a receiver would otherwise
	// have no record of.
	data := map[string]any{
		"reason": "done", "turns": 2, "model_calls": 2, "tool_calls": 0,
		"run_id": "r7", "model_class": "local",
	}
	server := floorServer(t, 3, 1)
	skip, counts := server.belowReflectionFloor(data)
	if !skip {
		t.Fatal("a two-call run with no tool calls cleared a floor of three and one")
	}

	class, known := telemetry.Classify("run.stopped")
	if !known || !class.Sent {
		t.Fatal("run.stopped is not sent")
	}
	sent := telemetry.Pick(class, data)
	for _, key := range []string{"model_calls", "tool_calls"} {
		if sent[key] != counts[key] {
			t.Errorf("%s: telemetry sends %v, the floor read %v -- they came from different places",
				key, sent[key], counts[key])
		}
	}
	// And the floor's own numbers -- what it was comparing against -- stay local.
	// The receiver learns how big the run was, not how this machine is configured.
	for _, key := range []string{"floor_model_calls", "floor_tool_calls", "run_id"} {
		if _, present := sent[key]; present {
			t.Errorf("%s left the machine", key)
		}
	}
}
