package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

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
	root := t.TempDir()
	cfg := config.Defaults(root)
	profile := runnableTestProfile("measured")
	profile.Label, profile.BaseURL, profile.Model, profile.RequestTimeoutS = "Measured", model.URL, "fake", 2
	cfg.Servers = []config.Profile{profile}
	cfg.Agents = []config.Agent{{Name: "Measured", B: "measured", Toolset: config.FullToolset()}}
	path := filepath.Join(root, "harness.json")
	server := New(&cfg, path, root, RuntimeRoots{Application: root, Data: root, Workspace: root}, events.NewBus())
	server.runMeasurement("measured", cfg.Servers[0])
	state := server.measurements["measured"]
	if state.Error != "" || state.Result == nil || state.Result.Passed != 10 || state.Result.Total != 10 || state.Result.Provenance != measurementProvenance || state.Result.Trials != 1 {
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

func quoted(value string) string { raw, _ := json.Marshal(value); return string(raw) }
func containsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}
