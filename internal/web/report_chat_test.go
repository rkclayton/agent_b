package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/telemetry"
)

// CHECK 5 of item 2pw: with the anonymous switch OFF, Report this chat sends
// exactly one request, within its bound, and answers with the id it sent.
func TestReportThisChatSendsOneRequestWithTheSwitchOff2pw(t *testing.T) {
	bus := events.NewBus()
	root := t.TempDir()
	cfg := &config.Config{Telemetry: config.Telemetry{Enabled: false}}
	server := New(cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Data: root, Profile: root}, bus)
	var sent [][]byte
	server.telemetry.transport = func(body []byte) error { sent = append(sent, body); return nil }
	server.applyTelemetry(*cfg)
	if server.TelemetryRunning() {
		t.Fatal("the switch is off and telemetry runs")
	}
	publish := func(eventType string, data map[string]any) { bus.Publish(events.New(eventType, "s1", "r1", data)) }
	publish(events.RunStarted, nil)
	publish(events.ModelRequest, map[string]any{"n_ctx": 8192, "model_file": "m.gguf", "connection_kind": "local"})
	publish(events.ModelResponse, map[string]any{"finish_reason": "stop", "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 5}})
	publish(events.RunStopped, map[string]any{"reason": "done"})
	if len(sent) != 0 {
		t.Fatalf("the run itself sent %d request(s) with the switch off", len(sent))
	}

	response := httptest.NewRecorder()
	server.reportChat(response, "s1")
	if response.Code != http.StatusOK {
		t.Fatalf("report answered %d: %s", response.Code, response.Body)
	}
	var answer struct {
		ReportID string `json:"report_id"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &answer)
	if len(sent) != 1 || len(sent[0]) > telemetry.ReportByteCap {
		t.Fatalf("report made %d request(s); want exactly one within %d bytes", len(sent), telemetry.ReportByteCap)
	}
	var batch struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(sent[0], &batch); err != nil || len(batch.Events) != 1 {
		t.Fatalf("report body is not one event: %s", sent[0])
	}
	if event := batch.Events[0]; event["type"] != "trace" || event["report_id"] != answer.ReportID || len(answer.ReportID) != 8 {
		t.Fatalf("the id answered (%q) is not the id sent: %v", answer.ReportID, event)
	}

	missing := httptest.NewRecorder()
	server.reportChat(missing, "nobody")
	if missing.Code != http.StatusNotFound || len(sent) != 1 {
		t.Fatalf("a chat with nothing recorded answered %d and sent %d", missing.Code, len(sent))
	}
}
