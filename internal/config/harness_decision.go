package config

import "fmt"

// Item 2ih: the Evaluation Harness sets the profile, and shows its work.
//
// The two decisions the harness makes live here, as functions over numbers,
// rather than inside the measurement loop. That is deliberate: the loop needs a
// live model and ten briefs, and these need neither, so the RULES can be checked
// exactly while the run that feeds them is exercised separately.

// SmallModelParameters is item 2ih (a)'s threshold: at or under four billion
// parameters, a model starts with reasoning OFF.
//
// It is a starting point and nothing more. (b)'s two arms still run for a small
// model, because the point of measuring is that assumption by size decides where
// to begin and the numbers decide where to end.
const SmallModelParameters uint64 = 4_000_000_000

// ReasoningDefaultForSize is (a). A zero count means UNKNOWN -- not every GGUF
// carries general.parameter_count and an API connection has no artifact at all
// -- and an unknown size applies no default rather than guessing one.
func ReasoningDefaultForSize(parameters uint64) (enabled bool, applied bool) {
	if parameters == 0 {
		return false, false
	}
	if parameters <= SmallModelParameters {
		return false, true
	}
	return true, true
}

// MeasurementArm is one arm of (b): the same ten briefs, with thinking on or
// off.
type MeasurementArm struct {
	Passed       int     `json:"passed"`
	Total        int     `json:"total"`
	ToolErrors   int     `json:"tool_errors"`
	EmptyReplies int     `json:"empty_replies"`
	FirstTokenMS int64   `json:"first_token_ms"`
	CompletionMS int64   `json:"completion_ms"`
	TokensPerSec float64 `json:"tokens_per_second"`
	ReasoningP95 int     `json:"reasoning_p95,omitempty"`
	Ran          bool    `json:"ran"`
}

// ReasoningDecision is what (c) writes, with the one line (c) requires.
type ReasoningDecision struct {
	Enabled      bool   `json:"enabled"`
	ReasoningCap int    `json:"reasoning_cap,omitempty"`
	Line         string `json:"line"`
}

// DecideReasoning is item 2ih (c), exactly as the item states it: the arm with
// the higher pass rate wins; OFF on a tie or within one brief; and OFF whenever
// the on-arm produced any empty reply at all.
//
// The empty-reply clause is not a tie-breaker, it is a veto. A model that
// finishes a turn with reasoning and no reply is failing in the way that is
// hardest to see from a pass count, and one occurrence is enough -- which is why
// it is checked before the pass rates rather than after.
func DecideReasoning(on, off MeasurementArm) ReasoningDecision {
	if !on.Ran || !off.Ran {
		return ReasoningDecision{Enabled: false, Line: "set reasoning off: both arms did not run, so nothing was measured"}
	}
	if on.EmptyReplies > 0 {
		return ReasoningDecision{
			Enabled: false,
			Line: fmt.Sprintf("set reasoning off: %d/%d with thinking vs %d/%d without, %d empty %s with thinking",
				on.Passed, on.Total, off.Passed, off.Total, on.EmptyReplies, plural(on.EmptyReplies, "reply", "replies")),
		}
	}
	if on.Passed-off.Passed <= 1 {
		return ReasoningDecision{
			Enabled: false,
			Line: fmt.Sprintf("set reasoning off: %d/%d with thinking vs %d/%d without, not enough to justify it",
				on.Passed, on.Total, off.Passed, off.Total),
		}
	}
	decision := ReasoningDecision{
		Enabled: true,
		Line: fmt.Sprintf("set reasoning on: %d/%d with thinking vs %d/%d without",
			on.Passed, on.Total, off.Passed, off.Total),
	}
	// The cap comes from what was OBSERVED, not from a constant. Left alone when
	// the arm could not report it, and the report says so.
	if on.ReasoningP95 > 0 {
		decision.ReasoningCap = on.ReasoningP95
		decision.Line += fmt.Sprintf("; reasoning cap %d from the 95th percentile observed", on.ReasoningP95)
	}
	return decision
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}
