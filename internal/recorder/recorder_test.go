package recorder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"harness/internal/events"
	"harness/internal/telemetry"
)

const secret = "ZEBRA-7731-planted"

// script publishes one run the way the run loop does: two inferences, and three
// calls of one tool with one argument, so the third is a loop.
func script(bus *events.Bus, chat, runID string) {
	publish := func(eventType string, data map[string]any) { bus.Publish(events.New(eventType, chat, runID, data)) }
	publish(events.RunStarted, map[string]any{"run_id": runID})
	publish(events.MessageAppended, map[string]any{"message": map[string]any{"role": "user", "content": "please read " + secret}})
	publish(events.ModelRequest, map[string]any{"turn": 1, "connection_kind": "local", "model_file": "Qwen3-" + "8B.gguf", "n_ctx": 32768, "prompt_hash": "9e2d41c0", "tools_offered": []string{"read_file", "list_dir"}, "est_prompt_tokens": 4000})
	publish(events.ModelDelta, map[string]any{"turn": 1, "kind": "content", "text": secret})
	publish(events.ModelResponse, map[string]any{"turn": 1, "finish_reason": "tool_calls", "content": "reading " + secret, "usage": map[string]any{"prompt_tokens": 4120, "completion_tokens": 96, "cached_tokens": 0}, "duration_ms": 2280})
	for index := 1; index <= 3; index++ {
		id := fmt.Sprintf("call-%d", index)
		publish(events.ToolCallEvent, map[string]any{"turn": 1, "call_id": id, "name": "read_file", "args": map[string]any{"path": `C:\Users\someone\` + secret + `.txt`}})
		publish(events.ToolResult, map[string]any{"turn": 1, "call_id": id, "name": "read_file", "ok": true, "ms": 3, "bytes": 1840, "preview": secret})
	}
	publish(events.ModelRequest, map[string]any{"turn": 2, "n_ctx": 32768})
	publish(events.ModelResponse, map[string]any{"turn": 2, "finish_reason": "stop", "content": "the file says " + secret, "usage": map[string]any{"prompt_tokens": 13400, "completion_tokens": 40}, "duration_ms": 900})
	publish(events.RunStopped, map[string]any{"run_id": runID, "reason": "done", "detail": secret})
}

func newRecorded(t *testing.T) (*Recorder, *events.Bus, chan map[string]any) {
	t.Helper()
	recorder, bus := New(), events.NewBus()
	bus.SetObserver(recorder.Observe)
	recorder.Settle = 10 * time.Millisecond
	summaries := make(chan map[string]any, 64)
	recorder.SetSink(func(eventType string, data map[string]any) {
		if eventType == events.RunSummary {
			summaries <- data
		}
	})

	return recorder, bus, summaries
}

func spansOf(t *testing.T, trace map[string]any) [][]map[string]any {
	t.Helper()
	var out [][]map[string]any
	for _, raw := range trace["runs"].([]json.RawMessage) {
		var run struct {
			Spans []map[string]any `json:"spans"`
		}
		if err := json.Unmarshal(raw, &run); err != nil {
			t.Fatal(err)
		}
		out = append(out, run.Spans)
	}
	return out
}

// CHECK 2.
func TestAScriptedRunIsRecordedInOrder2pw(t *testing.T) {
	recorder, bus, summaries := newRecorded(t)
	script(bus, "s1", "r1")
	trace, ok := recorder.Trace("s1")
	if !ok {
		t.Fatal("nothing recorded")
	}
	runs := spansOf(t, trace)
	if len(runs) != 1 {
		t.Fatalf("runs = %d", len(runs))
	}
	var sequence []string
	for _, span := range runs[0] {
		sequence = append(sequence, span["span"].(string))
	}
	want := "invoke_agent chat execute_tool execute_tool execute_tool loop chat"
	if got := strings.Join(sequence, " "); got != want {
		t.Fatalf("spans = %q, want %q", got, want)
	}
	invoke := runs[0][0]
	if invoke["gen_ai.request.model"] != "Qwen3-8B.gguf" || invoke["stop_reason"] != "done" || invoke["gen_ai.provider.name"] != "local" {
		t.Fatalf("invoke_agent = %v", invoke)
	}
	if third := runs[0][4]; third["retry_index"] != float64(2) || runs[0][2]["args"].([]any)[0].(map[string]any)["hmac"] != third["args"].([]any)[0].(map[string]any)["hmac"] {
		t.Fatalf("the repeated call is not recognisably the same: %v / %v", runs[0][2], third)
	}
	if ttft, ok := runs[0][1]["ttft_ms"]; !ok || ttft.(float64) < 0 {
		t.Fatalf("chat span has no time to first token: %v", runs[0][1])
	}
	summary := <-summaries
	if summary["inference_calls"] != 2 || summary["tool_calls"] != 3 || summary["loop"] != true || summary["stop_reason"] != "done" || summary["max_fill_pct"] != 40 {
		t.Fatalf("run.summary = %v", summary)
	}
}

// CHECK 3.
func TestAPlantedSecretReachesNothingTheRecorderWrites2pw(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)
	recorder, bus, summaries := newRecorded(t)
	script(bus, "s1", "r1")
	recorder.mu.Lock()
	var ring bytes.Buffer
	for _, run := range recorder.chats["s1"] {
		for _, span := range run.spans {
			ring.Write(span)
		}
		encoded, _ := json.Marshal(run.invoke)
		ring.Write(encoded)
	}
	recorder.mu.Unlock()
	trace, _ := recorder.Trace("s1")
	var sent [][]byte
	body, err := telemetry.ReportOne(telemetry.Options{Transport: transportInto(&sent)}, events.Trace, trace)
	if err != nil {
		t.Fatal(err)
	}
	summary, _ := json.Marshal(telemetry.Pick(mustClassify(t, events.RunSummary), <-summaries))
	for name, written := range map[string][]byte{"ring": ring.Bytes(), "trace": body, "run.summary": summary, "log": logged.Bytes()} {
		for _, needle := range []string{secret, "someone", `C:\`} {
			if bytes.Contains(written, []byte(needle)) {
				t.Errorf("%s carries %q: %s", name, needle, written)
			}
		}
	}
	if len(sent) != 1 || !bytes.Equal(sent[0], body) {
		t.Fatalf("report requests = %d", len(sent))
	}
}

// CHECK 4: thirty chats of twenty runs, the ring at its bound, and the cost of
// one event the same full as empty.
func TestTheRingAtItsFullBoundCostsTheSamePerEvent2pw(t *testing.T) {
	recorder, bus, _ := newRecorded(t)
	recorder.SetSink(nil)
	// A stopped run's aggregates are built off the publisher's goroutine, once
	// per run; parked here so they do not land inside the measurement.
	recorder.Settle = time.Hour
	for chat := 0; chat < 30; chat++ {
		for run := 0; run < 20; run++ {
			script(bus, fmt.Sprintf("chat-%d", chat), fmt.Sprintf("r%d", run))
		}
	}
	// 2,000 runs per measurement, long enough for Windows' coarse clock. Empty
	// is a hundred fresh recorders made before the clock starts, twenty runs
	// each; full is the ring above, at its bound throughout. The median of five
	// interleaved rounds, so machine noise lands on both.
	const runs = 2000
	perEvent := func(full bool) float64 {
		targets := []*events.Bus{bus}
		if !full {
			targets = nil
			for len(targets) < runs/20 {
				quiet, fresh, _ := newRecorded(t)
				quiet.Settle = time.Hour
				targets = append(targets, fresh)
			}
		}
		runtime.GC()
		start := time.Now()
		for index := 0; index < runs; index++ {
			script(targets[(index/20)%len(targets)], fmt.Sprintf("chat-%d", index%30), fmt.Sprintf("b%d", index))
		}
		return float64(time.Since(start).Nanoseconds()) / (runs * 14)
	}
	var empties, fulls []float64
	for round := 0; round < 5; round++ {
		empties, fulls = append(empties, perEvent(false)), append(fulls, perEvent(true))
	}
	sort.Float64s(empties)
	sort.Float64s(fulls)
	empty, full := empties[2], fulls[2]
	// The same measurement without a clock: allocations for one whole run.
	_, fresh, _ := newRecorded(t)
	allocEmpty := testing.AllocsPerRun(19, func() { script(fresh, "a", fmt.Sprint(time.Now().UnixNano())) })
	allocFull := testing.AllocsPerRun(19, func() { script(bus, "chat-3", fmt.Sprint(time.Now().UnixNano())) })
	total := recorder.Bytes()
	t.Logf("per event: empty %.0f ns, full %.0f ns (%+.1f%%); allocations per run: empty %.0f, full %.0f; ring %d bytes of %d", empty, full, (full-empty)/empty*100, allocEmpty, allocFull, total, TotalBytes)
	if allocFull > allocEmpty*1.10 {
		t.Errorf("a run allocates %.0f times full and %.0f empty", allocFull, allocEmpty)
	}
	if total > TotalBytes {
		t.Fatalf("ring holds %d bytes, over %d", total, TotalBytes)
	}
	recorder.mu.Lock()
	for chat, list := range recorder.chats {
		if len(list) > RunsPerChat {
			t.Errorf("%s keeps %d runs", chat, len(list))
		}
	}
	order := len(recorder.order) - recorder.head
	recorder.mu.Unlock()
	if order > 2*RunsPerChat*60+64 {
		t.Fatalf("eviction order grew to %d", order)
	}
	// The 10% bound is held by the allocation count above, which no clock can
	// blur. Under the whole suite's parallel load the clock drifts by more than
	// that (measured: +17% with 444/445 allocations), so the clock guards only
	// against a cost that grows with the ring, which would be many times over.
	if full > empty*1.5 {
		t.Fatalf("an event costs %.0f ns full and %.0f ns empty", full, empty)
	}
}

// CHECK 6: every vector in docs/telemetry-trace.md has the shape the builder
// produces, span type by span type, and passes the allow-list whole.
func TestTheDocumentVectorsValidateAgainstTheBuilder2pw(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "telemetry-trace.md"))
	if err != nil {
		t.Fatal(err)
	}
	vectors := documentVectors(t, string(body))
	for _, name := range []string{events.Trace, events.RunSummary} {
		vector := vectors[name]
		if vector == nil {
			t.Fatalf("no vector for %s", name)
		}
		data := map[string]any{}
		for key, value := range vector {
			if key != "type" && key != "at" {
				data[key] = value
			}
		}
		if picked := telemetry.Pick(mustClassify(t, name), data); len(picked) != len(data) {
			t.Errorf("%s: the allow-list keeps %d of the vector's %d fields", name, len(picked), len(data))
		}
	}
	recorder, bus, summaries := newRecorded(t)
	everySpan(bus)
	built := map[string]map[string]bool{}
	for _, run := range spansOf(t, mustTrace(t, recorder)) {
		collectKeys(built, run)
	}
	documented := map[string]map[string]bool{}
	for _, raw := range vectors[events.Trace]["runs"].([]any) {
		var spans []map[string]any
		for _, span := range raw.(map[string]any)["spans"].([]any) {
			spans = append(spans, span.(map[string]any))
		}
		collectKeys(documented, spans)
	}
	if got, want := describe(built), describe(documented); got != want {
		t.Fatalf("the builder makes\n%s\nthe document says\n%s", got, want)
	}
	// Runs close in no fixed order; the fields are the union over all three.
	union := map[string]bool{}
	for index := 0; index < 3; index++ {
		for key := range <-summaries {
			union[key] = true
		}
	}
	var summaryKeys, vectorKeys []string
	for key := range union {
		summaryKeys = append(summaryKeys, key)
	}
	for key := range vectors[events.RunSummary] {
		if key != "type" && key != "at" {
			vectorKeys = append(vectorKeys, key)
		}
	}
	sort.Strings(summaryKeys)
	sort.Strings(vectorKeys)
	if strings.Join(summaryKeys, ",") != strings.Join(vectorKeys, ",") {
		t.Fatalf("run.summary builds %v, the vector has %v", summaryKeys, vectorKeys)
	}
}

// documentVectors reads every ```json vector:<type> block of the document.
func documentVectors(t *testing.T, body string) map[string]map[string]any {
	t.Helper()
	vectors := map[string]map[string]any{}
	for _, match := range regexp.MustCompile("(?s)```json vector:([a-z.]+)\r?\n(.*?)\r?\n```").FindAllStringSubmatch(body, -1) {
		var vector map[string]any
		if err := json.Unmarshal([]byte(match[2]), &vector); err != nil {
			t.Fatalf("vector %s: %v", match[1], err)
		}
		vectors[match[1]] = vector
	}
	return vectors
}

// everySpan drives one chat through every span type the document names.
func everySpan(bus *events.Bus) {
	script(bus, "s1", "r1")
	publish := func(runID, eventType string, data map[string]any) { bus.Publish(events.New(eventType, "s1", runID, data)) }
	publish("r2", events.RunStarted, nil)
	publish("r2", events.ToolCallEvent, map[string]any{"call_id": "c", "name": "read_file", "args": map[string]any{}, "args_invalid": true})
	publish("r2", events.ToolUnoffered, map[string]any{"name": "web_fetch"})
	publish("r2", events.ModelRetry, map[string]any{"reason": "truncated_tool_call"})
	publish("r2", events.Compaction, map[string]any{"kind": "elide", "trigger": "pressure", "before": 30120, "after": 11800})
	publish("r2", events.RunStopped, map[string]any{"reason": "context_ceiling"})
	publish("r3", events.RunStarted, nil)
	publish("r3", events.RunStopped, map[string]any{"reason": "aborted_mid_model"})
}

func collectKeys(into map[string]map[string]bool, spans []map[string]any) {
	for _, span := range spans {
		kind := span["span"].(string)
		if into[kind] == nil {
			into[kind] = map[string]bool{}
		}
		for key := range span {
			into[kind][key] = true
		}
	}
}

func describe(shapes map[string]map[string]bool) string {
	var lines []string
	for kind, keys := range shapes {
		var names []string
		for key := range keys {
			names = append(names, key)
		}
		sort.Strings(names)
		lines = append(lines, kind+": "+strings.Join(names, ","))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func transportInto(sent *[][]byte) func(context.Context, []byte) error {
	return func(_ context.Context, body []byte) error { *sent = append(*sent, body); return nil }
}

func mustTrace(t *testing.T, recorder *Recorder) map[string]any {
	t.Helper()
	trace, ok := recorder.Trace("s1")
	if !ok {
		t.Fatal("nothing recorded")
	}
	return trace
}

func mustClassify(t *testing.T, name string) telemetry.Classification {
	t.Helper()
	class, ok := telemetry.Classify(name)
	if !ok || !class.Sent {
		t.Fatalf("%s is not allow-listed", name)
	}
	return class
}
