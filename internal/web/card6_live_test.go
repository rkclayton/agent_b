//go:build card6live

package web

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
)

// rel-1.23.0 card 6: the two-arm loop, against a LIVE model.
//
// The rule was fed live numbers by hand at rel-1.21.0 and the product loop that
// writes the decision has only ever met fixtures. This closes that: the same
// runMeasurement the Evaluation Harness calls, against a real server, with the
// decision it writes read back off the connection.
//
// Build-tagged because it needs a model. GPU rules apply where the model is on a
// GPU: this one is the local CPU llama-server that was already running.
func TestCard6TwoArmLoopAgainstALiveModel(t *testing.T) {
	baseURL := os.Getenv("AGENTB_LIVE_BASE_URL")
	model := os.Getenv("AGENTB_LIVE_MODEL")
	if baseURL == "" || model == "" {
		t.Skip("set AGENTB_LIVE_BASE_URL and AGENTB_LIVE_MODEL")
	}
	root := t.TempDir()
	cfg := config.Defaults(root)
	connection := runnableTestConnection("live")
	connection.Label, connection.BaseURL, connection.Model, connection.RequestTimeoutS = "Live", baseURL, model, 60
	connection.Reasoning.Control = "chat_template_kwargs"
	connection.Reasoning.Enabled = true
	connection.Context.NCtx, connection.Context.ReserveOutput = 8192, 1024
	cfg.Connections = []config.Connection{connection}
	cfg.Agents = []config.Agent{{Name: "Live", B: "live", Toolset: config.FullToolset()}}
	path := filepath.Join(root, "harness.json")
	server := New(&cfg, path, root, RuntimeRoots{Application: root, Data: root, Workspace: root}, events.NewBus())

	server.runMeasurement(context.Background(), "live", cfg.Connections[0])

	state := server.measurements["live"]
	if state.Result == nil || state.Result.Decision == nil {
		t.Fatalf("no decision from a live run: %+v", state)
	}
	on, off := state.Result.ReasoningOn, state.Result.ReasoningOff
	t.Logf("LIVE two-arm run against %s (%s)", baseURL, model)
	t.Logf("  thinking ON : passed %d/%d, empty %d, tool errors %d, p95 %d ms, ran=%v",
		on.Passed, on.Total, on.EmptyReplies, on.ToolErrors, on.CompletionMS, on.Ran)
	t.Logf("  thinking OFF: passed %d/%d, empty %d, tool errors %d, p95 %d ms, ran=%v",
		off.Passed, off.Total, off.EmptyReplies, off.ToolErrors, off.CompletionMS, off.Ran)
	t.Logf("  DECISION: %s", state.Result.Decision.Line)
	t.Logf("  written onto the connection: reasoning.enabled=%v cap=%d",
		server.ConfigSnapshot().Connections[0].Reasoning.Enabled, server.ConfigSnapshot().Connections[0].Reasoning.MaxTokens)
	t.Logf("  window: %d of %d tokens", state.Result.WindowTokens, state.Result.NCtx)

	// The gap card 6 names is that the loop has never run live. It is closed when
	// both arms ran and the decision reached the connection.
	if !on.Ran || !off.Ran {
		t.Fatalf("an arm did not run, so the gap is not closed: on=%v off=%v", on.Ran, off.Ran)
	}
	if server.ConfigSnapshot().Connections[0].Reasoning.Enabled != state.Result.Decision.Enabled {
		t.Errorf("the decision was not written onto the connection")
	}
}
