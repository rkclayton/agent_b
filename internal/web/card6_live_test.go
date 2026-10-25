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

// Build-tagged because it needs a model. GPU rules apply where the model is on a
// GPU: this one is the local CPU llama-server that was already running.
func TestCard6MeasurementAgainstALiveModel(t *testing.T) {
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
	server := newTestServer(t, &cfg, path, root, RuntimeRoots{Application: root, Data: root, Workspace: root}, events.NewBus())

	server.runMeasurement(context.Background(), "live", cfg.Connections[0])

	state := server.pageMeasurements.measurements["live"]
	if state.Result == nil {
		t.Fatalf("no result from a live run: %+v", state)
	}
	t.Logf("LIVE measurement against %s (%s): passed %d/%d, tool errors %d, p95 %d ms", baseURL, model, state.Result.Passed, state.Result.Total, state.Result.ToolErrors, state.Result.CompletionMS)
	t.Logf("  window: %d of %d tokens", state.Result.WindowTokens, state.Result.NCtx)
	if !server.ConfigSnapshot().Connections[0].Reasoning.Enabled || state.Result.Trials != 1 {
		t.Errorf("measurement changed thinking or ran more than once")
	}
}
