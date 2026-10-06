package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
)

func TestMeasurementRunsTenBriefsAndPersistsProvenance(t *testing.T) {
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content any `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		brief, _ := body.Messages[len(body.Messages)-1].Content.(string)
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"tool_calls": []any{map[string]any{"id": "c", "type": "function", "function": map[string]any{"name": "inspect_workspace", "arguments": `{"request":` + quoted(brief) + `}`}}}}, "finish_reason": "tool_calls"}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	defer model.Close()
	defer model.CloseClientConnections()
	root := t.TempDir()
	cfg := config.Defaults(root)
	connection := runnableTestConnection("measured")
	connection.Label, connection.BaseURL, connection.Model, connection.RequestTimeoutS = "Measured", model.URL, "fake", 2
	// Item 2ih (f): without a reasoning control the on-arm cannot mean anything
	// and is not run at all, which TestAConnectionThatCannotExpressTheSwitchIsNotMeasured2ih
	// covers. This test is the two-arm path, so the connection can hear the switch.
	connection.Reasoning.Control = "chat_template_kwargs"
	connection.ProbeMode = "off"
	cfg.Connections = []config.Connection{connection}
	cfg.Agents = []config.Agent{{Name: "Measured", B: "measured", Toolset: config.FullToolset()}}
	path := filepath.Join(root, "harness.json")
	server := New(&cfg, path, root, RuntimeRoots{Application: root, Data: root, Workspace: root}, events.NewBus())
	server.runEvaluation(context.Background(), "measured", cfg.Connections[0])
	state := server.measurements["measured"]
	// Item 2ih (b), wired at rel-1.22.0: TWO ARMS, so twenty briefs and two
	// trials, and the state line is the DECISION rather than a pass count --
	// (c) requires every write to be one line saying what it decided on.
	if state.Error != "" || state.Result == nil || state.Result.Total != 10 || state.Result.BriefsRun != 20 || state.Result.Provenance != measurementProvenance || state.Result.Trials != 2 {
		t.Fatalf("state=%+v", state)
	}
	if got := server.ConfigSnapshot().Connections[0].Capabilities; got.ProbedAt == "" || len(got.Findings) == 0 {
		t.Fatalf("Eval did not store capability findings: %+v", got)
	}
	if state.Result.ReasoningOn == nil || state.Result.ReasoningOff == nil || state.Result.Decision == nil {
		t.Fatalf("a two-arm measurement did not record both arms and its decision: %+v", state.Result)
	}
	if state.Result.ReasoningOn.Passed != 10 || state.Result.ReasoningOff.Passed != 10 {
		t.Fatalf("arms on=%+v off=%+v", state.Result.ReasoningOn, state.Result.ReasoningOff)
	}
	// Both arms passed everything, so the rule keeps reasoning OFF -- a tie is
	// not enough to justify it -- and says so in one line naming the numbers.
	if state.Result.Decision.Enabled || !strings.Contains(state.Result.Decision.Line, "10/10 with thinking vs 10/10") {
		t.Fatalf("decision=%+v", state.Result.Decision)
	}
	if state.Text != state.Result.Decision.Line {
		t.Fatalf("the state line is not the decision: %q", state.Text)
	}
	if calls.Load() != 20 {
		t.Fatalf("calls=%d", calls.Load())
	}
	raw, err := os.ReadFile(path)
	if err != nil || !containsAll(string(raw), `"measurement"`, measurementProvenance) {
		t.Fatalf("persisted=%s err=%v", raw, err)
	}
}

func TestStoppingMeasurementCancelsCurrentBriefAndStoresPartialCount(t *testing.T) {
	reached := make(chan struct{}, 1)
	release := make(chan struct{})
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case reached <- struct{}{}:
		default:
		}
		<-release
	}))
	defer model.Close()
	defer model.CloseClientConnections()
	root := t.TempDir()
	cfg := config.Defaults(root)
	connection := runnableTestConnection("measured")
	connection.Label, connection.BaseURL, connection.Model, connection.RequestTimeoutS = "Measured", model.URL, "fake", 60
	cfg.Connections = []config.Connection{connection}
	cfg.Agents = []config.Agent{{Name: "Measured", B: "measured", Toolset: config.FullToolset()}}
	path := filepath.Join(root, "harness.json")
	server := New(&cfg, path, root, RuntimeRoots{Application: root, Data: root, Workspace: root}, events.NewBus())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	server.measurements[connection.ID] = measureState{Running: true, Text: "Starting ten briefs"}
	server.measureCancels[connection.ID] = cancel
	go server.runMeasurement(ctx, connection.ID, connection)
	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		t.Fatal("measurement did not begin its first brief")
	}
	response := httptest.NewRecorder()
	server.measureConnection(response, httptest.NewRequest(http.MethodDelete, "/api/eval/measure?connection_id=measured", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("stop status=%d body=%s", response.Code, response.Body.String())
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		server.measureMu.RLock()
		state := server.measurements[connection.ID]
		server.measureMu.RUnlock()
		if !state.Running {
			if state.Error != "" || state.Result == nil || !state.Result.Stopped || state.Result.BriefsRun != 1 || state.Result.Total != 10 {
				t.Fatalf("stopped state=%+v", state)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("measurement did not stop within the current brief")
}

func quoted(value string) string { raw, _ := json.Marshal(value); return string(raw) }
func containsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}

// Item 2ih (c): THE HARNESS SETS. The decision is not a suggestion in a report;
// it is written onto the connection it measured, and the cap is written only
// when one was observed.
//
// The model here answers every brief correctly WITHOUT thinking and returns an
// empty turn WITH it, which is the shape rel-1.21.0 measured on a real 0.8B
// model: the veto decides before the pass rates are compared.
func TestTheHarnessWritesTheDecisionItMeasured2ih(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content any `json:"content"`
			} `json:"messages"`
			Thinking     bool `json:"thinking"`
			ChatTemplate struct {
				EnableThinking *bool `json:"enable_thinking"`
			} `json:"chat_template_kwargs"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		thinking := body.Thinking || (body.ChatTemplate.EnableThinking != nil && *body.ChatTemplate.EnableThinking)
		if thinking {
			// A turn that finished with neither a call nor prose.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{"content": ""}, "finish_reason": "stop"}},
			})
			return
		}
		brief, _ := body.Messages[len(body.Messages)-1].Content.(string)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"tool_calls": []any{map[string]any{"id": "c", "type": "function", "function": map[string]any{"name": "inspect_workspace", "arguments": `{"request":` + quoted(brief) + `}`}}}}, "finish_reason": "tool_calls"}},
		})
	}))
	defer model.Close()
	defer model.CloseClientConnections()

	root := t.TempDir()
	cfg := config.Defaults(root)
	connection := runnableTestConnection("measured")
	connection.Label, connection.BaseURL, connection.Model, connection.RequestTimeoutS = "Measured", model.URL, "fake", 2
	connection.Reasoning.Enabled = true // the state the measurement must change
	connection.Reasoning.MaxTokens = 321
	connection.Reasoning.Control = "chat_template_kwargs" // how this server hears the switch
	connection.Context.NCtx, connection.Context.ReserveOutput = 8192, 1024
	cfg.Connections = []config.Connection{connection}
	cfg.Agents = []config.Agent{{Name: "Measured", B: "measured", Toolset: config.FullToolset()}}
	path := filepath.Join(root, "harness.json")
	server := New(&cfg, path, root, RuntimeRoots{Application: root, Data: root, Workspace: root}, events.NewBus())
	server.runMeasurement(context.Background(), "measured", cfg.Connections[0])

	result := server.measurements["measured"].Result
	if result == nil || result.Decision == nil {
		t.Fatal("no decision was recorded")
	}
	if result.ReasoningOn.EmptyReplies != 10 || result.ReasoningOff.Passed != 10 {
		t.Fatalf("the arms did not measure what the fixture served: on=%+v off=%+v", result.ReasoningOn, result.ReasoningOff)
	}
	if result.Decision.Enabled || !strings.Contains(result.Decision.Line, "empty") {
		t.Fatalf("the empty-reply veto did not decide: %+v", result.Decision)
	}
	// 2qw: Eval records the decision but never changes a setting.
	if !server.ConfigSnapshot().Connections[0].Reasoning.Enabled {
		t.Error("Eval changed the operator's reasoning switch")
	}
	if got := server.ConfigSnapshot().Connections[0]; got.Reasoning.MaxTokens != 321 || got.Context.NCtx != 8192 {
		t.Errorf("Eval changed cap/context: cap=%d context=%d", got.Reasoning.MaxTokens, got.Context.NCtx)
	}
	// (e): the window the model will run with is on the same record.
	if result.NCtx != 8192 || result.WindowTokens != 7168 {
		t.Errorf("the window is not recorded: n_ctx=%d window=%d", result.NCtx, result.WindowTokens)
	}
	// @keep: no cap was observed, so the operator's value is left alone.
	if result.Decision.ReasoningCap != 0 {
		t.Errorf("a cap was invented from an arm that produced no reasoning: %d", result.Decision.ReasoningCap)
	}
}

// Item 2ih (f): a connection with no way to express the thinking switch cannot
// be measured for it, and says so rather than comparing two identical arms.
func TestAConnectionThatCannotExpressTheSwitchIsNotMeasured2ih(t *testing.T) {
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content any `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls.Add(1)
		brief, _ := body.Messages[len(body.Messages)-1].Content.(string)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"tool_calls": []any{map[string]any{"id": "c", "type": "function", "function": map[string]any{"name": "inspect_workspace", "arguments": `{"request":` + quoted(brief) + `}`}}}}, "finish_reason": "tool_calls"}},
		})
	}))
	defer model.Close()
	defer model.CloseClientConnections()

	root := t.TempDir()
	cfg := config.Defaults(root)
	connection := runnableTestConnection("plain")
	connection.Label, connection.BaseURL, connection.Model, connection.RequestTimeoutS = "Plain", model.URL, "fake", 2
	connection.Reasoning.Control = "none"
	connection.Reasoning.Enabled = true
	cfg.Connections = []config.Connection{connection}
	cfg.Agents = []config.Agent{{Name: "Plain", B: "plain", Toolset: config.FullToolset()}}
	path := filepath.Join(root, "harness.json")
	server := New(&cfg, path, root, RuntimeRoots{Application: root, Data: root, Workspace: root}, events.NewBus())
	server.runMeasurement(context.Background(), "plain", cfg.Connections[0])

	result := server.measurements["plain"].Result
	if result == nil || result.ReasoningOn == nil || result.ReasoningOn.Ran {
		t.Fatalf("the on-arm claimed to have run against a connection that cannot hear the switch: %+v", result)
	}
	// Ten briefs, not twenty: the arm that could mean nothing was not spent.
	if calls.Load() != 10 {
		t.Errorf("the unmeasurable arm was run anyway: %d calls", calls.Load())
	}
	if !strings.Contains(result.Decision.Line, "did not run") {
		t.Errorf("the line does not say the measurement could not run: %q", result.Decision.Line)
	}
	// And nothing was written: the operator's own setting survives.
	if !server.ConfigSnapshot().Connections[0].Reasoning.Enabled {
		t.Error("a measurement that did not run still wrote the switch")
	}
}
