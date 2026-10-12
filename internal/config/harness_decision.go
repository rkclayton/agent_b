package config

// MeasurementArm and ReasoningDecision remain readable so measurement records
// written by older releases can still be loaded and shown as historical data.
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
	// Item 2ih (f), wired at rel-1.22.0: an arm that could not finish says which
	// way it ended. BriefsRun is how far it got, which is not always Total.
	BriefsRun int  `json:"briefs_run"`
	Capped    bool `json:"capped,omitempty"`
	Stopped   bool `json:"stopped,omitempty"`
}

type ReasoningDecision struct {
	Enabled      bool   `json:"enabled"`
	ReasoningCap int    `json:"reasoning_cap,omitempty"`
	Line         string `json:"line"`
}
