package recorder

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"harness/internal/events"
	"harness/internal/telemetry"
)

// emissions collects what the recorder hands its sink, by type.
type emissions struct {
	mu   sync.Mutex
	byID map[string][]map[string]any
	all  []emitted
}

func (e *emissions) sink(kind string, data map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.byID[kind] = append(e.byID[kind], data)
	e.all = append(e.all, emitted{kind, data})
}

// settle waits until nothing new has arrived for a while.
func (e *emissions) settle() {
	last := -1
	for {
		time.Sleep(60 * time.Millisecond)
		e.mu.Lock()
		count := len(e.all)
		e.mu.Unlock()
		if count == last {
			return
		}
		last = count
	}
}

func recorded() (*Recorder, *events.Bus, *emissions) {
	recorder, bus, out := New(), events.NewBus(), &emissions{byID: map[string][]map[string]any{}}
	recorder.Settle = 10 * time.Millisecond
	recorder.SetSink(out.sink)
	bus.SetObserver(recorder.Observe)
	return recorder, bus, out
}

// replay feeds a journal reduced to the recorder's input (testdata, made from
// the operator's chats of 2026-10-02 with every word of text removed).
func replay(t *testing.T, name string) *emissions {
	t.Helper()
	file, err := os.Open(filepath.Join("testdata", name+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	_, bus, out := recorded()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event events.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		bus.Publish(event)
	}
	out.settle()
	return out
}

// CHECK 1: each failure he relayed by hand appears on its own.
func TestTheRelayedFailuresOf20261002AppearOnTheirOwn2q6(t *testing.T) {
	t.Run("s48 over-window refusal", func(t *testing.T) {
		out := replay(t, "s48")
		refused := out.byID[events.ModelRefused]
		if len(refused) != 1 || refused[0]["status"] != 400 || refused[0]["error_type"] != "exceed_context_size_error" || refused[0]["count"] != 1 {
			t.Fatalf("model.refused = %v", refused)
		}
		reasons := map[any]bool{}
		for _, summary := range out.byID[events.RunSummary] {
			reasons[summary["stop_reason"]] = true
		}
		if len(reasons) != 2 || !reasons["model_error"] || !reasons["done"] {
			t.Fatalf("run.summary stop reasons = %v", reasons)
		}
	})
	t.Run("s51 approval wait, compaction storm, summarizer refusals", func(t *testing.T) {
		out := replay(t, "s51")
		var longest map[string]any
		for _, wait := range out.byID[events.ApprovalWait] {
			if longest == nil || wait["seconds"].(int) > longest["seconds"].(int) {
				longest = wait
			}
		}
		if longest == nil || longest["seconds"] != 21409 || longest["outcome"] != "dismissed" || longest["card_kind"] != "shell.operator_override" {
			t.Fatalf("the 5 h 57 m wait = %v", longest)
		}
		// 55 compactions over three runs, 45 of them in the last; the summarizer
		// failed its fit check 42 times in all (7 + 1 + 34).
		storm, compactions, failed := map[string]any{}, 0, 0
		for _, compaction := range out.byID[events.Compaction] {
			compactions += compaction["count"].(int)
			failed += compaction["outcomes"].(map[string]int)["error"]
			if compaction["count"].(int) > number(storm["count"]) {
				storm = compaction
			}
		}
		if compactions != 55 || storm["count"] != 45 || failed != 42 {
			t.Fatalf("compactions %d, storm %v, summarizer failures %d", compactions, storm, failed)
		}
	})
	t.Run("s52 thinking share and cache after compaction", func(t *testing.T) {
		out := replay(t, "s52")
		perf := out.byID[events.ModelPerf]
		// Thinking is 76% of output tokens by the server's counts (the 78% relayed
		// was measured another way); the first call after each of five
		// compactions found 21% of its prompt cached, against 53% over the run.
		if len(perf) != 1 || perf[0]["reasoning_share_pct"] != 76 || perf[0]["cache_hit_pct"] != 53 || perf[0]["cache_hit_pct_after_compaction"] != 21 {
			t.Fatalf("model.perf = %v", perf)
		}
	})
}

// CHECK 3 (and 2pw's CHECK 6 for the new types): every vector in the document
// has exactly the fields the builder makes, and passes the allow-list whole.
func TestTheNewTypesVectorsValidateAgainstTheBuilder2q6(t *testing.T) {
	built := map[string]map[string]bool{}
	collect := func(out *emissions) {
		for _, item := range out.all {
			if built[item.kind] == nil {
				built[item.kind] = map[string]bool{}
			}
			for key := range item.data {
				built[item.kind][key] = true
			}
		}
	}
	for _, name := range []string{"s48", "s51", "s52"} {
		collect(replay(t, name))
	}
	_, bus, out := recorded()
	everySpan(bus)
	bus.Publish(events.New(events.ConnectionHealth, "", "", map[string]any{"connection_id": "a", "health": map[string]any{"lamp": "alarm", "word": "unreachable"}}))
	bus.Publish(events.New(events.UpdateChanged, "", "", map[string]any{"current_version": "v1.60.11", "checked_at": "x", "available": true, "version": "v1.60.12",
		"outcome": map[string]any{"at": "y", "ok": false, "phase": "verify", "version": "v1.60.12"}}))
	out.settle()
	collect(out)
	collect(&emissions{all: appEmissions(t)})
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "telemetry-trace.md"))
	if err != nil {
		t.Fatal(err)
	}
	vectors := documentVectors(t, string(body))
	for kind := range telemetry.RecorderTypes {
		if kind == events.Trace || kind == events.SettingsShape {
			continue // the trace is checked span by span; settings.shape in internal/web
		}
		vector := vectors[kind]
		if vector == nil {
			t.Errorf("no vector for %s", kind)
			continue
		}
		documented := map[string]bool{}
		for key := range vector {
			if key != "type" && key != "at" {
				documented[key] = true
			}
		}
		if got, want := describe(map[string]map[string]bool{kind: built[kind]}), describe(map[string]map[string]bool{kind: documented}); got != want {
			t.Errorf("the builder makes %s; the document says %s", got, want)
		}
		if picked := telemetry.Pick(mustClassify(t, kind), vector); len(picked) != len(documented) {
			t.Errorf("%s: the allow-list keeps %d of the vector's %d fields", kind, len(picked), len(documented))
		}
	}
}

// 2q8 CHECK 2: the phone extends the shared document, not the PC builder. Its
// vectors still have a closed allow-list, and every enum refuses an unknown
// value before the broker or either client implements the schema.
func TestOnDeviceVectorsAreClosedAndWhole2q8(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "telemetry-trace.md"))
	if err != nil {
		t.Fatal(err)
	}
	vectors := documentVectors(t, string(body))
	for _, name := range []string{"ondevice.chat", "ondevice.invoke", "ondevice.condensed", "ondevice.run", "ondevice.sizes"} {
		vector := vectors[name]
		if vector == nil {
			t.Fatalf("no vector for %s", name)
		}
		if err := validateOnDeviceVector(name, vector); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		vector["free_text"] = "must be refused"
		if err := validateOnDeviceVector(name, vector); err == nil {
			t.Fatalf("%s accepted a field outside its allow-list", name)
		}
		delete(vector, "free_text")
	}

	closed := []struct {
		name  string
		field string
	}{
		{"ondevice.chat", "ondevice.size_unit"},
		{"ondevice.chat", "error.type"},
		{"ondevice.invoke", "ondevice.availability"},
		{"ondevice.condensed", "unit"},
		{"ondevice.run", "availability"},
	}
	for _, test := range closed {
		vector := vectors[test.name]
		prior := vector[test.field]
		vector[test.field] = "outside_the_set"
		if err := validateOnDeviceVector(test.name, vector); err == nil {
			t.Errorf("%s accepted %s outside its closed set", test.name, test.field)
		}
		vector[test.field] = prior
	}

	run := vectors["ondevice.run"]
	for _, field := range []string{"errors", "routes"} {
		values := run[field].(map[string]any)
		values["outside_the_set"] = float64(1)
		if err := validateOnDeviceVector("ondevice.run", run); err == nil {
			t.Errorf("ondevice.run accepted %s key outside its closed set", field)
		}
		delete(values, "outside_the_set")
	}
	picture := run["pictures"].([]any)[0].(map[string]any)
	for _, field := range []string{"outcome", "cause", "style"} {
		prior := picture[field]
		picture[field] = "outside_the_set"
		if err := validateOnDeviceVector("ondevice.run", run); err == nil {
			t.Errorf("ondevice.run accepted picture.%s outside its closed set", field)
		}
		picture[field] = prior
	}
}

func validateOnDeviceVector(name string, vector map[string]any) error {
	sets := map[string]map[string]bool{
		"unit":         {"tokens": true, "chars": true, "entries": true},
		"error":        {"exceeded_context_window": true, "guardrail_violation": true, "decoding_failure": true, "unsupported_language": true, "assets_unavailable": true, "rate_limited": true, "concurrent_requests": true, "refusal": true, "other": true},
		"availability": {"available": true, "device_not_eligible": true, "apple_intelligence_not_enabled": true, "model_not_ready": true, "other": true},
		"outcome":      {"made": true, "failed": true, "declined": true},
		"cause":        {"none": true, "unsupported_style": true, "guardrail": true, "unavailable": true, "timeout": true, "other": true},
		"style":        {"illustration": true, "animation": true, "sketch": true, "other": true},
		"route":        {"on_device": true, "pc_link": true, "api": true, "other": true},
	}
	allowed := map[string]map[string]bool{
		"ondevice.chat":      keys("span", "gen_ai.provider.name", "ondevice.context_size", "ondevice.size_unit", "ondevice.instructions", "ondevice.tool_schemas", "ondevice.transcript", "ondevice.prompt", "ondevice.slots", "ondevice.schema_in_prompt", "ondevice.prewarmed", "ondevice.model_load_ms", "tok_s", "error.type"),
		"ondevice.invoke":    keys("span", "run", "gen_ai.provider.name", "ondevice.availability"),
		"ondevice.condensed": keys("span", "seq", "kind", "trigger", "before", "after", "unit"),
		"ondevice.run":       keys("type", "at", "availability", "errors", "pictures", "prewarm", "routes", "sizes"),
		"ondevice.sizes":     keys("type", "at", "sizes"),
	}[name]
	for field := range vector {
		if !allowed[field] {
			return fmt.Errorf("field %s is not allow-listed", field)
		}
	}
	check := func(field, set string) error {
		if value, ok := vector[field].(string); ok && !sets[set][value] {
			return fmt.Errorf("%s=%q is outside %s", field, value, set)
		}
		return nil
	}
	for _, pair := range [][2]string{{"ondevice.size_unit", "unit"}, {"error.type", "error"}, {"ondevice.availability", "availability"}, {"unit", "unit"}, {"availability", "availability"}} {
		if err := check(pair[0], pair[1]); err != nil {
			return err
		}
	}
	if slots, ok := vector["ondevice.slots"].(map[string]any); ok {
		if err := exactNestedKeys(slots, keys("goal", "facts", "notes", "recent", "recall")); err != nil {
			return fmt.Errorf("slots: %v", err)
		}
	}
	if errors, ok := vector["errors"].(map[string]any); ok {
		for value := range errors {
			if !sets["error"][value] {
				return fmt.Errorf("errors contains %q", value)
			}
		}
	}
	if routes, ok := vector["routes"].(map[string]any); ok {
		for value := range routes {
			if !sets["route"][value] {
				return fmt.Errorf("routes contains %q", value)
			}
		}
	}
	if prewarm, ok := vector["prewarm"].(map[string]any); ok {
		if err := exactNestedKeys(prewarm, keys("hit", "miss")); err != nil {
			return fmt.Errorf("prewarm: %v", err)
		}
	}
	if sizes, ok := vector["sizes"].(map[string]any); ok {
		if err := exactNestedKeys(sizes, keys("context_size", "instructions", "tool_schemas", "transcript", "prompt", "goal", "facts", "notes", "recent", "recall")); err != nil {
			return fmt.Errorf("sizes: %v", err)
		}
		for field, raw := range sizes {
			value, ok := raw.(float64)
			if !ok || value < 1 || int64(value)&(int64(value)-1) != 0 {
				return fmt.Errorf("sizes.%s is not a power of two", field)
			}
		}
	}
	if pictures, ok := vector["pictures"].([]any); ok {
		for _, raw := range pictures {
			picture := raw.(map[string]any)
			if err := exactNestedKeys(picture, keys("outcome", "cause", "style", "count")); err != nil {
				return fmt.Errorf("picture: %v", err)
			}
			for _, field := range []string{"outcome", "cause", "style"} {
				if !sets[field][picture[field].(string)] {
					return fmt.Errorf("picture.%s is outside its closed set", field)
				}
			}
		}
	}
	return nil
}

func keys(names ...string) map[string]bool {
	out := map[string]bool{}
	for _, name := range names {
		out[name] = true
	}
	return out
}

func exactNestedKeys(got map[string]any, allowed map[string]bool) error {
	for key := range got {
		if !allowed[key] {
			return fmt.Errorf("field %s is not allow-listed", key)
		}
	}
	return nil
}

// CHECK 5: a forty-call run adds at most twelve events and 32 KiB, including
// the one bounded automatic trace emitted because this fixture has tool errors.
func TestAFortyCallRunAddsAtMostTwelveEvents2q6(t *testing.T) {
	_, bus, out := recorded()
	publish := func(eventType string, data map[string]any) { bus.Publish(events.New(eventType, "s1", "r1", data)) }
	publish(events.RunStarted, nil)
	for call := 0; call < 40; call++ {
		publish(events.ModelRequest, map[string]any{"est_prompt_tokens": 9000 + call*100, "n_ctx": 32768, "connection_kind": "local"})
		publish(events.ModelDelta, map[string]any{"kind": "content"})
		publish(events.ModelResponse, map[string]any{"finish_reason": "tool_calls", "content": "", "tool_calls": []any{map[string]any{}}, "reasoning_tokens": 300,
			"usage": map[string]any{"prompt_tokens": 9000 + call*110, "completion_tokens": 400, "cached_tokens": 8000}, "timings": map[string]any{"prompt_ms": 900.5, "predicted_per_second": 41.2}})
		tool := []string{"read_file", "search", "shell", "list_dir"}[call%4]
		publish(events.ToolCallEvent, map[string]any{"call_id": fmt.Sprint(call), "name": tool, "args": map[string]any{"path": fmt.Sprint(call % 7)}})
		publish(events.ToolResult, map[string]any{"call_id": fmt.Sprint(call), "name": tool, "ok": call%9 != 0, "class": "timeout", "ms": 20 + call, "bytes": 3000})
		if call%10 == 9 {
			publish(events.Compaction, map[string]any{"kind": "elide", "trigger": "soft_pct", "before": 30000, "after": 12000})
			publish(events.ModelRefused, map[string]any{"status": 400, "error_type": "exceed_context_size_error", "connection_kind": "local"})
		}
	}
	publish(events.RunStopped, map[string]any{"reason": "done"})
	out.settle()
	var batch []telemetry.Event
	for _, item := range out.all {
		class, _ := telemetry.Classify(item.kind)
		batch = append(batch, telemetry.Event{Type: item.kind, At: "2026-10-04T21:00:00Z", Data: telemetry.Pick(class, item.data)})
	}
	encoded, _ := json.Marshal(batch)
	t.Logf("a 40-call run adds %d events, %d bytes", len(batch), len(encoded))
	if len(batch) > 12 || len(encoded) > 32<<10 {
		t.Fatalf("a 40-call run adds %d events and %d bytes", len(batch), len(encoded))
	}
}

// appSession is 2q7 CHECK 1's scripted session at the recorder: start, the
// window, three settings pages, two chats with a message each and a tool, a phone
// join through the link's own record, one update check and one install.
func appSession(t *testing.T) (*App, map[string]map[string]any) {
	t.Helper()
	clock := time.Date(2026, 10, 4, 21, 0, 0, 0, time.UTC)
	app := NewApp(clock)
	app.now = func() time.Time { return clock }
	app.Resources = func() map[string]any {
		return map[string]any{"data_bytes": int64(3_000_000_000), "chats_bytes": int64(900_000_000), "chats": int64(40), "ram_bytes": int64(32_000_000_000), "os_version": "windows 10.0.26200"}
	}
	bus := events.NewBus()
	bus.SetObserver(app.Observe)
	publish := func(eventType, session string, data map[string]any) {
		bus.Publish(events.New(eventType, session, "r1", data))
	}
	app.NoteListening(851, "clean")
	app.NoteLifecycleStart("session_end")
	app.NoteLifecycleExit("installer", 123)
	clock = clock.Add(1420 * time.Millisecond)
	app.NoteWindowShown()
	app.NotePage(640, map[string]int{"connections": 2, "about": 1})
	app.NoteState(29_300_000, 2400*time.Millisecond)
	for _, chat := range []string{"s1", "s2"} {
		publish(events.SessionCreated, chat, map[string]any{"created_by": "operator"})
		publish(events.MessageAppended, chat, map[string]any{"message": map[string]any{"role": "user", "content": "ZEBRA secret", "attachments": []any{map[string]any{"kind": "image", "path": `C:\Users\someone\a.png`}}}})
		publish(events.ToolResult, chat, map[string]any{"name": "read_file", "ok": true})
	}
	clock = clock.Add(3810 * time.Millisecond)
	publish(events.ModelResponse, "s1", map[string]any{"content": "answer"})
	publish(events.NavigationMeasured, "", map[string]any{"end_to_end_ms": 120})
	publish(events.UIError, "", map[string]any{"kind": "unhandled exception", "message": "ZEBRA secret", "name": "TypeError", "file": "shell.js", "line": 412, "repeat_count": 3})
	publish(events.ApprovalRequired, "s1", map[string]any{"call_id": "c1", "name": "shell.operator_override"})
	publish(events.ApprovalDecided, "s1", map[string]any{"call_id": "c1", "decision": "folder"})
	app.Link("connected")
	app.NoteJoin(35_100_000)
	app.Link("disconnected reason=read: unexpected EOF ZEBRA")
	clock = clock.Add(1200 * time.Millisecond)
	app.Link("connected")
	app.Link("ERROR code=queue_full detail=ZEBRA secret")
	app.Link("PUSH kind=run_stopped answer=accepted")
	publish(events.UpdateChanged, "", map[string]any{"current_version": "v1.60.12", "checked_at": "2026-10-04T21:00:30Z", "available": true, "version": "v1.60.13"})
	publish(events.UpdateChanged, "", map[string]any{"current_version": "v1.60.12", "outcome": map[string]any{"at": "2026-10-04T21:05:00Z", "ok": false, "phase": "verify", "version": "v1.60.13"}})
	byType := map[string]map[string]any{}
	for _, item := range app.Flush() {
		byType[item.kind] = item.data
	}
	return app, byType
}

// 2q7 CHECK 1.
func TestTheAppAroundTheRunsIsReported2q7(t *testing.T) {
	_, got := appSession(t)
	expect := map[string]map[string]any{
		events.AppStart:   {"listen_ms": int64(851), "window_ms": int64(1420), "first_answer_ms": int64(5230), "previous_exit": "clean"},
		events.PageHealth: {"state_bytes": int64(33554432), "longest_freeze_ms": 640},
		events.LinkHealth: {"connects": 2, "join_bytes": int64(67108864)},
		events.Install:    {"step": "verify", "class": "failed", "from": "v1.60.12", "to": "v1.60.13"},
		events.Resource:   {"data_bytes": int64(4294967296), "chats_bytes": int64(1073741824), "chats": int64(64), "ram_bytes": int64(34359738368), "os_version": "windows 10.0.26200"},
		events.FeatureUse: {"chats_created": 2, "messages_sent": 2, "voice": 0},
	}
	for kind, fields := range expect {
		for field, want := range fields {
			if got[kind][field] != want {
				t.Errorf("%s.%s = %#v, want %#v", kind, field, got[kind][field], want)
			}
		}
	}
	link := got[events.LinkHealth]
	if link["drops"].(map[string]int)["eof"] != 1 || link["refused"].(map[string]int)["queue_full"] != 1 || link["pushes"].(map[string]int)["accepted"] != 1 || link["reconnect_ms"].(map[string]int)["p50"] != 1200 {
		t.Errorf("link.health = %v", link)
	}
	use := got[events.FeatureUse]
	if use["settings_pages"].(map[string]int)["connections"] != 2 || use["tools"].(map[string]int)["read_file"] != 2 || use["attachments"].(map[string]int)["image"] != 2 || use["approvals"].(map[string]int)["shell.operator_override.folder"] != 1 {
		t.Errorf("feature.use = %v", use)
	}
	if errors := got[events.PageHealth]["js_errors"].([]map[string]any); len(errors) != 1 || errors[0]["name"] != "TypeError" || errors[0]["file"] != "shell.js" || errors[0]["line"] != 412 || errors[0]["count"] != 3 {
		t.Errorf("js_errors = %v", errors)
	}
	encoded, _ := json.Marshal(got)
	for _, needle := range []string{"ZEBRA", "someone", "secret"} {
		if strings.Contains(string(encoded), needle) {
			t.Fatalf("the app's events carry %q: %s", needle, encoded)
		}
	}
}

func TestLifecycleEnvelopesAreContentFree2qq(t *testing.T) {
	app := NewApp(time.Date(2026, 10, 6, 14, 0, 0, 0, time.UTC))
	app.NoteLifecycleStart("session_end")
	app.NoteLifecycleExit("installer", 123)
	got := app.Flush()
	var lifecycle []map[string]any
	for _, item := range got {
		if item.kind == events.AppLifecycle {
			lifecycle = append(lifecycle, item.data)
		}
	}
	if len(lifecycle) != 2 || lifecycle[0]["phase"] != "start" || lifecycle[0]["previous_exit"] != "session_end" || lifecycle[1]["phase"] != "exit" || lifecycle[1]["cause"] != "installer" || lifecycle[1]["uptime_s"] != int64(123) {
		t.Fatalf("lifecycle = %#v", lifecycle)
	}
	encoded, _ := json.Marshal(lifecycle)
	for _, forbidden := range []string{"path", "pid", "message", "detail", "ZEBRA"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("lifecycle exposed %q: %s", forbidden, encoded)
		}
	}
}

func TestChatsCreatedCountsOnlyOperatorCreatedChats2qd(t *testing.T) {
	app := NewApp(time.Now())
	for index := 0; index < 64; index++ {
		app.Observe(events.New(events.SessionCreated, fmt.Sprintf("s%d", index), "", map[string]any{"restored": true}))
	}
	for index := 0; index < 2; index++ {
		app.Observe(events.New(events.SessionCreated, fmt.Sprintf("new%d", index), "", map[string]any{"created_by": "operator"}))
	}
	byType := map[string]map[string]any{}
	for _, item := range app.Flush() {
		byType[item.kind] = item.data
	}
	if got := byType[events.FeatureUse]["chats_created"]; got != 2 {
		t.Fatalf("chats_created = %v, want 2", got)
	}
}

// 2q7 CHECK 3: every bucketed field is a power of two, whatever went in.
func TestEveryBucketIsAPowerOfTwo2q7(t *testing.T) {
	for _, n := range []int64{1, 2, 3, 5, 1000, 29_300_000, 1 << 40, (1 << 40) + 1} {
		if bucket := Bucket(n); bucket < n || bucket&(bucket-1) != 0 || bucket >= 2*n {
			t.Errorf("Bucket(%d) = %d", n, bucket)
		}
	}
	if Bucket(0) != 0 {
		t.Error("Bucket(0) is not 0")
	}
	_, got := appSession(t)
	for kind, fields := range map[string][]string{events.PageHealth: {"state_bytes"}, events.LinkHealth: {"join_bytes"},
		events.Resource: {"memory_peak_bytes", "data_bytes", "chats_bytes", "chats", "ram_bytes"}} {
		for _, field := range fields {
			value, _ := got[kind][field].(int64)
			if value <= 0 || value&(value-1) != 0 {
				t.Errorf("%s.%s = %v, not a power of two", kind, field, got[kind][field])
			}
		}
	}
}

// 2q7 CHECK 5: an hour of ordinary use adds at most 20 events and 16 KiB.
func TestAnHourOfOrdinaryUseIsSmall2q7(t *testing.T) {
	app, _ := appSession(t)
	bus := events.NewBus()
	bus.SetObserver(app.Observe)
	for index := 0; index < 60; index++ {
		session := fmt.Sprintf("s%d", index%6)
		bus.Publish(events.New(events.MessageAppended, session, "r", map[string]any{"message": map[string]any{"role": "user"}}))
		for _, tool := range []string{"read_file", "search", "shell", "edit_file", "list_dir"} {
			bus.Publish(events.New(events.ToolResult, session, "r", map[string]any{"name": tool, "ok": true}))
		}
		bus.Publish(events.New(events.NavigationMeasured, "", "", map[string]any{"end_to_end_ms": 100 + index}))
		app.NoteState(int64(1_000_000+index*1000), time.Duration(index)*time.Millisecond)
		app.Link("PUSH kind=run_stopped answer=accepted")
	}
	var batch []telemetry.Event
	for _, item := range app.Flush() {
		class, _ := telemetry.Classify(item.kind)
		batch = append(batch, telemetry.Event{Type: item.kind, At: "2026-10-04T22:00:00Z", Data: telemetry.Pick(class, item.data)})
	}
	encoded, _ := json.Marshal(batch)
	t.Logf("an hour of ordinary use adds %d events, %d bytes", len(batch), len(encoded))
	if len(batch) > 20 || len(encoded) > 16<<10 {
		t.Fatalf("an hour adds %d events and %d bytes", len(batch), len(encoded))
	}
}

// appEmissions feeds the vector test the app's six types, each field present.
func appEmissions(t *testing.T) []emitted {
	t.Helper()
	_, got := appSession(t)
	out := make([]emitted, 0, len(got))
	for kind, data := range got {
		out = append(out, emitted{kind, data})
	}
	return out
}
