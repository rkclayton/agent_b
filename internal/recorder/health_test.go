package recorder

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// CHECK 5: a forty-call run adds at most twelve events and 8 KiB.
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
	if len(batch) > 12 || len(encoded) > 8<<10 {
		t.Fatalf("a 40-call run adds %d events and %d bytes", len(batch), len(encoded))
	}
}
