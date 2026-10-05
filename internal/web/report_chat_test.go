package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

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

// telemetryRun drives one chat through a run carrying a planted secret in every
// place 2q6 CHECK 2 names, with the recorder settling quickly, and returns what
// left. The connection's address and label carry it too.
func telemetryRun(t *testing.T, enabled bool) [][]byte {
	t.Helper()
	const secret = "ZEBRA-7731-planted"
	bus, root := events.NewBus(), t.TempDir()
	cfg := &config.Config{Telemetry: config.Telemetry{Enabled: enabled, Endpoint: "https://receiver.invalid/ingest"},
		Connections: []config.Connection{{ID: "c1", Label: secret + " label", BaseURL: "http://" + secret + ".example:8080", Model: `C:\Users\someone\` + secret + `\model.gguf`}}}
	server := New(cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Data: root, Profile: root}, bus)
	server.recorder.Settle = 10 * time.Millisecond
	var mu sync.Mutex
	var sent [][]byte
	server.telemetry.transport = func(body []byte) error { mu.Lock(); sent = append(sent, body); mu.Unlock(); return nil }
	server.applyTelemetry(*cfg)
	publish := func(eventType string, data map[string]any) { bus.Publish(events.New(eventType, "s1", "r1", data)) }
	publish(events.RunStarted, nil)
	publish(events.MessageAppended, map[string]any{"message": map[string]any{"role": "user", "content": secret}})
	publish(events.ModelRequest, map[string]any{"n_ctx": 8192, "model_file": "model.gguf", "connection_kind": "api", "est_prompt_tokens": 90})
	publish(events.ModelRefused, map[string]any{"status": 400, "error_type": "exceed_context_size_error", "connection_kind": "api"})
	publish(events.ModelResponse, map[string]any{"finish_reason": "tool_calls", "content": secret, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 5}})
	publish(events.ToolCallEvent, map[string]any{"call_id": "a", "name": "read_file", "args": map[string]any{"path": `C:\Users\someone\` + secret + `.txt`}})
	publish(events.ToolResult, map[string]any{"call_id": "a", "name": "read_file", "ok": false, "class": "not_found", "preview": secret, "ms": 2, "bytes": 40})
	publish(events.Compaction, map[string]any{"kind": "elide", "trigger": "soft_pct", "before": 900, "after": 300, "affected_ids": []string{secret}})
	publish(events.RunStopped, map[string]any{"reason": "done", "detail": secret})
	bus.Publish(events.New(events.ConnectionHealth, "", "", map[string]any{"connection_id": "c1", "health": map[string]any{"lamp": "alarm", "word": "unreachable"}}))
	time.Sleep(300 * time.Millisecond)
	server.telemetry.mu.Lock()
	if state := server.telemetry.state; state != nil {
		state.sender.Flush()
	}
	server.telemetry.mu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	for _, body := range sent {
		for _, needle := range []string{secret, "someone", "example", "label"} {
			if strings.Contains(string(body), needle) {
				t.Errorf("a batch carries %q: %s", needle, body)
			}
		}
	}
	return sent
}

// 2q6 CHECK 2, with its positive control: the new events did leave.
func TestAPlantedSecretReachesNoBatchByte2q6(t *testing.T) {
	sent := telemetryRun(t, true)
	all := ""
	for _, body := range sent {
		all += string(body)
	}
	for _, kind := range []string{"run.summary", "model.perf", "tool.perf", "model.behaviour", "budget.drift", "compaction", "model.refused", "connection.state", "settings.shape"} {
		if !strings.Contains(all, `"type":"`+kind+`"`) {
			t.Errorf("%s never left, so its absence of secrets proves nothing", kind)
		}
	}
	// settings.shape against its vector in docs/telemetry-trace.md.
	document, err := os.ReadFile(filepath.Join("..", "..", "docs", "telemetry-trace.md"))
	if err != nil {
		t.Fatal(err)
	}
	vector := regexp.MustCompile("(?s)```json vector:settings.shape\r?\n(.*?)\r?\n```").FindSubmatch(document)
	var documented, built map[string]any
	_ = json.Unmarshal(vector[1], &documented)
	for _, body := range sent {
		var batch struct{ Events []map[string]any }
		_ = json.Unmarshal(body, &batch)
		for _, event := range batch.Events {
			if event["type"] == "settings.shape" {
				built = event
			}
		}
	}
	keys := func(value map[string]any) string {
		var out []string
		for key := range value {
			out = append(out, key)
		}
		for key := range value["connections"].([]any)[0].(map[string]any) {
			out = append(out, "connections."+key)
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	if built == nil || keys(built) != keys(documented) {
		t.Fatalf("settings.shape builds %v; the document says %v", built, documented)
	}
}

// 2q6 CHECK 4: with the switch off, nothing at all.
func TestTheSwitchOffSendsNothing2q6(t *testing.T) {
	if sent := telemetryRun(t, false); len(sent) != 0 {
		t.Fatalf("the switch is off and %d request(s) left", len(sent))
	}
}

// 2q7 CHECK 2: the app's hourly events carry none of a planted secret — in a
// chat, a path, a connection's address and label, or a JavaScript error's
// message — and, the positive control, all six did leave.
func TestTheAppsEventsCarryNoPlantedSecret2q7(t *testing.T) {
	const secret = "ZEBRA-7731-planted"
	bus, root := events.NewBus(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, secret+".txt"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Telemetry: config.Telemetry{Enabled: true, Endpoint: "https://receiver.invalid/ingest"},
		Connections: []config.Connection{{ID: "c1", Label: secret + " label", BaseURL: "http://" + secret + ".example:8080", Model: `C:\Users\someone\` + secret + `\model.gguf`}}}
	server := New(cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Data: root, Profile: root}, bus)
	var mu sync.Mutex
	var sent [][]byte
	server.telemetry.transport = func(body []byte) error { mu.Lock(); sent = append(sent, body); mu.Unlock(); return nil }
	server.applyTelemetry(*cfg)
	server.app.NoteListening(851, "clean")
	post := func(path, body string) {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		switch path {
		case "/api/ui-errors":
			server.uiError(response, request)
		case "/api/page-health":
			server.pageHealth(response, request)
		}
		if response.Code >= 300 {
			t.Fatalf("%s answered %d: %s", path, response.Code, response.Body)
		}
	}
	post("/api/ui-errors", `{"kind":"unhandled exception","message":"`+secret+` at C:\\Users\\someone","stack":"TypeError: `+secret+`\n at http://127.0.0.1/js/shell.js:412:9","location":"http://127.0.0.1/chat","repeat_count":1,"name":"TypeError","file":"shell.js","line":412}`)
	post("/api/ui-errors", `{"kind":"console.error","message":"x","stack":"","location":"","repeat_count":1,"name":"`+secret+`","file":"C:\\`+secret+`.js","line":1}`)
	post("/api/page-health", `{"freeze_ms":640,"settings_pages":{"connections":2,"`+secret+`":1}}`)
	state := httptest.NewRecorder()
	server.state(state, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	bus.Publish(events.New(events.SessionCreated, "s1", "", map[string]any{"workspace_dir": `C:\Users\someone\` + secret}))
	bus.Publish(events.New(events.MessageAppended, "s1", "r1", map[string]any{"message": map[string]any{"role": "user", "content": secret, "attachments": []any{map[string]any{"kind": "image", "path": `C:\Users\someone\` + secret + `.png`}}}}))
	bus.Publish(events.New(events.ModelResponse, "s1", "r1", map[string]any{"content": secret}))
	bus.Publish(events.New(events.UpdateChanged, "", "", map[string]any{"current_version": "v1.60.12", "outcome": map[string]any{"at": "2026-10-04T21:05:00Z", "ok": false, "phase": "verify", "version": "v1.60.13", "error": secret, "transcript": `C:\Users\someone\` + secret}}))
	server.app.Link("connected")
	server.app.Link("disconnected reason=" + secret + " EOF")
	server.app.Link("ERROR code=queue_full detail=" + secret)
	server.app.NoteReport()
	server.app.SendNow()
	server.telemetry.mu.Lock()
	server.telemetry.state.sender.Flush()
	server.telemetry.mu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	all := ""
	for _, body := range sent {
		all += string(body)
	}
	for _, needle := range []string{secret, "someone", "example", "label"} {
		if strings.Contains(all, needle) {
			t.Errorf("a batch carries %q", needle)
		}
	}
	for _, kind := range []string{"app.start", "page.health", "link.health", "install", "resource", "feature.use"} {
		if !strings.Contains(all, `"type":"`+kind+`"`) {
			t.Errorf("%s never left, so its absence of secrets proves nothing", kind)
		}
	}
	if !strings.Contains(all, `"name":"TypeError"`) || !strings.Contains(all, `"file":"shell.js"`) || !strings.Contains(all, `"state_bytes":`) {
		t.Errorf("the page's own facts did not arrive: %s", all)
	}
}
