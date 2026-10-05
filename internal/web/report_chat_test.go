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
)

// 2qa CHECKS 1-4: reporting is automatic only while telemetry is on, every
// named struggle qualifies, a clean run does not, and only six traces fit in
// one rolling hour. The removed route must stay absent even for a recorded chat.
func TestStrugglingRunsReportThemselvesAndTheButtonRouteIsGone2qa(t *testing.T) {
	bus := events.NewBus()
	root := t.TempDir()
	cfg := &config.Config{Telemetry: config.Telemetry{Enabled: true, Endpoint: "https://receiver.invalid/ingest"}}
	server := New(cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Data: root, Profile: root}, bus)
	server.recorder.Settle = time.Millisecond
	var mu sync.Mutex
	var sent [][]byte
	server.telemetry.transport = func(body []byte) error {
		mu.Lock()
		sent = append(sent, append([]byte(nil), body...))
		mu.Unlock()
		return nil
	}
	server.applyTelemetry(*cfg)
	t.Cleanup(func() { server.telemetry.mu.Lock(); server.stopTelemetryLocked(); server.telemetry.mu.Unlock() })

	traceCount := func() int {
		t.Helper()
		time.Sleep(20 * time.Millisecond)
		server.telemetry.mu.Lock()
		server.telemetry.state.sender.Flush()
		server.telemetry.mu.Unlock()
		mu.Lock()
		defer mu.Unlock()
		count := 0
		for _, body := range sent {
			var batch struct {
				Events []map[string]any `json:"events"`
			}
			if err := json.Unmarshal(body, &batch); err != nil {
				t.Fatal(err)
			}
			for _, event := range batch.Events {
				if event["type"] == events.Trace {
					count++
				}
			}
		}
		return count
	}
	run := func(id, reason string, extra func()) {
		bus.Publish(events.New(events.RunStarted, "s1", id, nil))
		if extra != nil {
			extra()
		}
		bus.Publish(events.New(events.RunStopped, "s1", id, map[string]any{"reason": reason}))
	}

	run("clean", "done", nil)
	if got := traceCount(); got != 0 {
		t.Fatalf("clean done run sent %d trace(s)", got)
	}
	run("context", "context_exhausted", nil)
	if got := traceCount(); got != 1 {
		t.Fatalf("context stop trace count=%d", got)
	}
	run("detector", "done", func() {
		bus.Publish(events.New(events.ProgressShadow, "s1", "detector", map[string]any{"would_fire": true, "detector": "loop"}))
	})
	if got := traceCount(); got != 2 {
		t.Fatalf("detector trace count=%d", got)
	}
	run("tools", "done", func() {
		for _, call := range []string{"a", "b"} {
			bus.Publish(events.New(events.ToolCallEvent, "s1", "tools", map[string]any{"call_id": call, "name": "read_file", "args": map[string]any{"path": call}}))
			bus.Publish(events.New(events.ToolResult, "s1", "tools", map[string]any{"call_id": call, "name": "read_file", "ok": false, "class": "not_found"}))
		}
	})
	if got := traceCount(); got != 3 {
		t.Fatalf("two tool errors trace count=%d", got)
	}
	run("stopped", "aborted_mid_run", nil)
	if got := traceCount(); got != 4 {
		t.Fatalf("operator stop trace count=%d", got)
	}
	run("unoffered", "done", func() {
		bus.Publish(events.New(events.ToolUnoffered, "s1", "unoffered", map[string]any{"name": "missing"}))
	})
	run("refused", "done", func() {
		bus.Publish(events.New(events.ModelRefused, "s1", "refused", map[string]any{"status": 400, "error_type": "refusal"}))
	})
	if got := traceCount(); got != 6 {
		t.Fatalf("six qualifying runs sent %d traces", got)
	}
	run("seventh", "done", func() {
		bus.Publish(events.New(events.ToolCallEvent, "s1", "seventh", map[string]any{"call_id": "bad", "name": "read_file", "args_invalid": true}))
	})
	if got := traceCount(); got != 6 {
		t.Fatalf("seventh qualifying run sent a trace: %d", got)
	}

	response := httptest.NewRecorder()
	server.routes().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/sessions/s1/report", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("removed report route answered %d: %s", response.Code, response.Body)
	}
}

func TestStrugglingRunsSendNothingWithTelemetryOff2qa(t *testing.T) {
	bus, root := events.NewBus(), t.TempDir()
	cfg := &config.Config{Telemetry: config.Telemetry{Enabled: false}}
	server := New(cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Data: root, Profile: root}, bus)
	server.recorder.Settle = time.Millisecond
	var sent [][]byte
	server.telemetry.transport = func(body []byte) error { sent = append(sent, body); return nil }
	server.applyTelemetry(*cfg)
	bus.Publish(events.New(events.RunStarted, "s1", "r1", nil))
	bus.Publish(events.New(events.RunStopped, "s1", "r1", map[string]any{"reason": "context_exhausted"}))
	time.Sleep(20 * time.Millisecond)
	if len(sent) != 0 || server.TelemetryRunning() {
		t.Fatalf("switch off: running=%t sends=%d", server.TelemetryRunning(), len(sent))
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
