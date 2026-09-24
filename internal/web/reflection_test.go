package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/projection"
	"harness/internal/reflection"
	"harness/internal/session"
)

// Item 17-i, v1.1.0/W5. Console reads the latest overview and report as text.
// The endpoint is read-only and says plainly when reflection is off.
func TestTheReflectionEndpointIsReadOnlyTextAndSaysWhenItIsOff(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	server := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, events.NewBus())

	recorder := httptest.NewRecorder()
	server.reflectionEndpoint(recorder, httptest.NewRequest(http.MethodGet, "/api/reflection", nil))
	var off reflectionResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &off); err != nil {
		t.Fatal(err)
	}
	if off.Enabled || off.Overview != "" {
		t.Fatalf("reflection reported as on: %+v", off)
	}

	server.StartReflection(time.Hour)
	if server.reflection == nil {
		t.Fatal("reflection did not start")
	}
	defer server.StopReflection()
	store := server.reflection.store
	if _, err := store.PutOverview(reflection.Overview{PlanID: "p1", Tier: "tier 1 (Go)", Text: "Reflection on p1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutReport(reflection.Report{Text: "Tool candidates"}); err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	server.reflectionEndpoint(recorder, httptest.NewRequest(http.MethodGet, "/api/reflection", nil))
	var on reflectionResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &on); err != nil {
		t.Fatal(err)
	}
	if !on.Enabled || on.Overview != "Reflection on p1" || on.Report != "Tool candidates" || on.Tier != "tier 1 (Go)" {
		t.Fatalf("endpoint = %+v", on)
	}

	// Read-only: anything but GET is refused.
	recorder = httptest.NewRecorder()
	server.reflectionEndpoint(recorder, httptest.NewRequest(http.MethodPost, "/api/reflection", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST answered %d", recorder.Code)
	}
}

// The store lives under the data root, which is what the installer's roots and
// the retention rules already cover.
func TestTheReflectionStoreLivesUnderTheDataRoot(t *testing.T) {
	root := t.TempDir()
	store, err := reflection.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := os.Stat(filepath.Join(root, "reflection", "reflection.db")); err != nil {
		t.Fatalf("the database is not under the data root: %v", err)
	}
}

// The summary call goes through the ordinary model client. Proved here on a
// fake OpenAI-compatible server: a closed run is summarised into the store,
// and a failing call leaves the run's own state exactly as it was.
func TestAClosedRunIsSummarisedThroughTheModelClientOnAFakeServer(t *testing.T) {
	root := t.TempDir()
	asked := make(chan string, 4)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		asked <- string(body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"files_read\":[\"guard.go\"],\"files_written\":[],\"changed\":\"read the guard\",\"open\":\"\",\"text\":\"Looked at the guard.\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	defer fake.Close()

	cfg := config.Defaults(root)
	cfg.Connections = []config.Connection{{ID: "fake", Label: "Fake", BaseURL: fake.URL, Model: "test", RequestTimeoutS: 30}}
	cfg.Agents = []config.Agent{{Name: "Tester", B: "fake", Toolset: config.FullToolset()}}
	bus := events.NewBus()
	server := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, bus)
	writers, err := events.NewWriters(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	projector := projection.NewStore()
	bus.SetDurableSink(writers.WriteRecord, projector.Apply, projector.MarkStale)
	server.SetProjection(projector, writers)
	registry := session.NewRegistry(bus, writers, server.Connection, cfg.Run.MaxTurns, server.ConfigSnapshot)
	server.SetRegistry(registry)
	item, err := registry.Create("chat", config.AgentID("Tester"), cfg.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writers.Close() })
	server.StartReflection(time.Hour)
	defer server.StopReflection()

	// One recorded run in this chat's log.
	for _, event := range []events.Event{
		events.New("tool.call", item.ID, "r1", map[string]any{"call_id": "1", "name": "read_file", "args": map[string]any{"path": "guard.go"}}),
		events.New("tool.result", item.ID, "r1", map[string]any{"call_id": "1", "name": "read_file", "ok": true}),
	} {
		bus.Publish(event)
	}
	before := item.Snapshot().Run
	waitForRecordedCall(t, server, item.ID)

	server.reflectOnClosedRun(item.ID, "r1")
	select {
	case body := <-asked:
		if !strings.Contains(body, "guard.go") {
			t.Fatalf("the summary call did not carry the run: %s", body)
		}
	default:
		t.Fatal("no summary call was made")
	}
	summaries, err := server.reflection.store.Summaries(time.Time{}, 0)
	if err != nil || len(summaries) != 1 {
		t.Fatalf("summaries=%+v err=%v", summaries, err)
	}
	if summaries[0].Changed != "read the guard" || summaries[0].Connection != "fake" {
		t.Fatalf("summary = %+v", summaries[0])
	}
	if !strings.Contains(summaries[0].Text, "no aux connection is configured") {
		t.Fatalf("summary text = %q", summaries[0].Text)
	}
	if after := item.Snapshot().Run; !reflect.DeepEqual(after, before) {
		t.Fatalf("the run's state changed: %+v -> %+v", before, after)
	}
}

func TestAFailingSummaryLeavesTheRunsOutcomeAlone(t *testing.T) {
	root := t.TempDir()
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model is down", http.StatusInternalServerError)
	}))
	defer fake.Close()
	cfg := config.Defaults(root)
	cfg.Connections = []config.Connection{{ID: "fake", Label: "Fake", BaseURL: fake.URL, Model: "test", RequestTimeoutS: 5}}
	cfg.Agents = []config.Agent{{Name: "Tester", B: "fake", Toolset: config.FullToolset()}}
	bus := events.NewBus()
	server := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, bus)
	writers, err := events.NewWriters(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	projector := projection.NewStore()
	bus.SetDurableSink(writers.WriteRecord, projector.Apply, projector.MarkStale)
	server.SetProjection(projector, writers)
	registry := session.NewRegistry(bus, writers, server.Connection, cfg.Run.MaxTurns, server.ConfigSnapshot)
	server.SetRegistry(registry)
	item, err := registry.Create("chat", config.AgentID("Tester"), cfg.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writers.Close() })
	server.StartReflection(time.Hour)
	defer server.StopReflection()
	before := item.Snapshot()
	server.reflectOnClosedRun(item.ID, "r1")
	summaries, err := server.reflection.store.Summaries(time.Time{}, 0)
	if err != nil || len(summaries) != 1 || summaries[0].Failed == "" {
		t.Fatalf("summaries=%+v err=%v", summaries, err)
	}
	after := item.Snapshot()
	if !reflect.DeepEqual(after.Run, before.Run) {
		t.Fatalf("the run changed: %+v -> %+v", before.Run, after.Run)
	}
}

// waitForRecordedCall waits for the durable sink to have written the run's
// events: reflection reads the JSONL, which is the authority.
func waitForRecordedCall(t *testing.T, server *Server, sessionID string) {
	t.Helper()
	for attempt := 0; attempt < 100; attempt++ {
		for _, path := range server.reflectionSessionLogs(sessionID) {
			if body, err := os.ReadFile(path); err == nil && strings.Contains(string(body), "tool.call") {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the run's events were never written")
}
