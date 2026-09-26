package agent

import (
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/llm"
)

// Item 2l9. Production chat s28, run r410: `reason: "length"`,
// `detail: "model output was truncated"`, 672,376 ms on one turn, and the
// operator was shown "The run stopped because of length." The stop must name the
// cap, its value and where it is set.
func TestATruncatedAnswerNamesTheCapAndWhereItIsSet2l9(t *testing.T) {
	connection := &config.Connection{ID: "server", Label: "Slumberland", Context: config.Context{NCtx: 200000, ReserveOutput: 10240}}
	detail := truncationDetail(connection, llm.Response{Content: "a partial answer", FinishReason: "length"})
	for _, want := range []string{"10240", "reserve_output", "Slumberland", "cut short"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("the stop line does not say %q: %s", want, detail)
		}
	}
	if detail == "model output was truncated" {
		t.Fatal("the old bare line survived")
	}
}

// (d): thinking taking the whole allowance is a different sentence from the
// answer being cut short, because the operator fixes them differently.
func TestThinkingUsingTheBudgetSaysSo2l9(t *testing.T) {
	connection := &config.Connection{ID: "server", Label: "Slumberland", Reasoning: config.Reasoning{Enabled: true}, Context: config.Context{NCtx: 200000, ReserveOutput: 10240}}
	detail := truncationDetail(connection, llm.Response{Reasoning: "a very long think", FinishReason: "length"})
	if !strings.Contains(detail, "thinking used the output allowance") {
		t.Fatalf("thinking exhausting the budget is not distinguished: %s", detail)
	}
	if !strings.Contains(detail, "5120") {
		t.Fatalf("the stop line does not name thinking's own share: %s", detail)
	}
}

// (c): a new connection on a large window does not get the fixed 10,240.
func TestTheProposedOutputReserveFollowsTheWindow2l9(t *testing.T) {
	if got := config.ReserveOutputFor(262144); got != config.MaxProposedReserveOutput {
		t.Fatalf("a 262,144-token window proposes %d, want %d", got, config.MaxProposedReserveOutput)
	}
	if got := config.ReserveOutputFor(200000); got != 25000 {
		t.Fatalf("a 200,000-token window proposes %d, want 25000", got)
	}
	if got := config.ReserveOutputFor(4096); got != config.DefaultReserveOutput {
		t.Fatalf("a small window proposes %d, want the old default %d", got, config.DefaultReserveOutput)
	}
	if got := config.ReserveOutputFor(0); got != config.DefaultReserveOutput {
		t.Fatalf("an unknown window proposes %d, want the old default", got)
	}
}

// (e3): the one hard limit. A reserve that starves the prompt is refused at save,
// with its reason, and the operator's own saved values are untouched by (c).
func TestAReserveBeyondTheSanityBoundIsRefusedAtSave2l9(t *testing.T) {
	good := config.Connection{ID: "c", Label: "c", BaseURL: "http://127.0.0.1:1", Model: "m", ProbeMode: "off", AttachmentHandling: "auto", RequestTimeoutS: 60, Reasoning: config.Reasoning{Control: "auto", Effort: "medium"}, Context: config.Context{NCtx: 100000, ReserveOutput: 50000}}
	bad := good
	bad.Context.ReserveOutput = 50001
	cfg := config.Defaults(".")
	cfg.Connections = []config.Connection{good}
	cfg.Agents = []config.Agent{{Name: "A", B: "c", C: "c", D: "c", Toolset: []string{"read_file"}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a reserve of exactly half the window was refused: %v", err)
	}
	cfg.Connections = []config.Connection{bad}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("a reserve larger than half the window was accepted")
	}
	for _, want := range []string{"reserve_output", "50001", "100000", "50000"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not say %q: %v", want, err)
		}
	}
}

// (e2): the ceiling is a soft, per-answer value and defaults to no ceiling.
func TestTheAnswerCeilingIsOptionalAndNamedWhenItFires2l9(t *testing.T) {
	connection := &config.Connection{ID: "server", Label: "Slumberland"}
	if answerCeiling(connection) != 0 {
		t.Fatal("an unset ceiling is not no ceiling")
	}
	connection.Context.AnswerCeilingSeconds = 600
	if answerCeiling(connection) != 600*time.Second {
		t.Fatalf("ceiling %v, want 10m", answerCeiling(connection))
	}
	detail := answerCeilingDetail(connection, 672*time.Second)
	for _, want := range []string{"600-second", "672", "answer_ceiling_seconds", "kept above"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("the ceiling stop line does not say %q: %s", want, detail)
		}
	}
}

// (d) again, at the request: thinking's share is only sent where the server
// accepted a reasoning budget, and it never exceeds the whole allowance.
func TestThinkingShareIsHalfTheAllowance2l9(t *testing.T) {
	if got := config.ReasoningShareFor(10240); got != 5120 {
		t.Fatalf("share %d, want 5120", got)
	}
	if got := config.ReasoningShareFor(0); got != 0 {
		t.Fatalf("share %d, want 0 when there is no allowance", got)
	}
	connection := &config.Connection{ID: "c", Model: "m", Reasoning: config.Reasoning{Control: "top_level", Enabled: true}, Capabilities: config.Capabilities{Findings: []string{"server reasoning budget: accepted"}}}
	body := llm.BuildRequest(connection, llm.Request{Thinking: true, MaxTokens: 10240, ReasoningMaxTokens: 5120, Messages: []llm.Message{{Role: "user", Content: "x"}}}, true)
	if body["reasoning_budget"] != 5120 {
		t.Fatalf("thinking's share was not sent: %v", body["reasoning_budget"])
	}
	plain := &config.Connection{ID: "c", Model: "m", Reasoning: config.Reasoning{Control: "top_level", Enabled: true}}
	if body := llm.BuildRequest(plain, llm.Request{Thinking: true, MaxTokens: 10240, ReasoningMaxTokens: 5120, Messages: []llm.Message{{Role: "user", Content: "x"}}}, true); body["reasoning_budget"] != nil {
		t.Fatalf("a share was sent to a server that never accepted one: %v", body["reasoning_budget"])
	}
}
