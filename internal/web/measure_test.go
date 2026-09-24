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
	cfg.Connections = []config.Connection{connection}
	cfg.Agents = []config.Agent{{Name: "Measured", B: "measured", Toolset: config.FullToolset()}}
	path := filepath.Join(root, "harness.json")
	server := New(&cfg, path, root, RuntimeRoots{Application: root, Data: root, Workspace: root}, events.NewBus())
	server.runMeasurement(context.Background(), "measured", cfg.Connections[0])
	state := server.measurements["measured"]
	if state.Error != "" || state.Result == nil || state.Result.Passed != 10 || state.Result.Total != 10 || state.Result.BriefsRun != 10 || state.Result.Provenance != measurementProvenance || state.Result.Trials != 1 {
		t.Fatalf("state=%+v", state)
	}
	if calls.Load() != 10 {
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
