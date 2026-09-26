package stats

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Item 2ji (a): the sums are checked against RETAINED JOURNALS that already hold
// real runs, not against fixtures written to agree with them. rel-1.17.0/W0
// established that the item's own acceptance — three runs each on the operator's
// connections — is generation work this order forbids, so the journals are what
// stands in for it, and they are the same material the projector's pins use.

type journalEvent struct {
	Type      string         `json:"type"`
	TS        string         `json:"ts"`
	SessionID string         `json:"session_id"`
	RunID     string         `json:"run_id"`
	Data      map[string]any `json:"data"`
}

func journalDir() string {
	return filepath.Join("..", "projection", "testdata", "pins", "sources")
}

func readJournal(t *testing.T, name string) []journalEvent {
	t.Helper()
	path := filepath.Join(journalDir(), name)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var reader io.Reader = file
	if strings.HasSuffix(name, ".gz") {
		unzipped, err := gzip.NewReader(file)
		if err != nil {
			t.Fatal(err)
		}
		defer unzipped.Close()
		reader = unzipped
	}
	events := []journalEvent{}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1<<20), 32<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var event journalEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func journalNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(journalDir())
	if err != nil {
		t.Skipf("no journal pins: %v", err)
	}
	names := []string{}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".events") {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		t.Skip("no journal pins")
	}
	return names
}

// The fold's sums are recomputed here by a second, dumber pass over the same
// journal. Two independent walks agreeing is evidence; the fold agreeing with
// itself is not.
func TestRunTimeSumsMatchTheJournals2ji(t *testing.T) {
	folded := 0
	for _, name := range journalNames(t) {
		events := readJournal(t, name)
		tally := NewRunTally()
		// The dumb pass, per run.
		type plain struct {
			modelMS, toolMS, compactionMS int64
			retries, compactions          int
			responses, withTimings        int
		}
		expected := map[string]*plain{}
		for _, event := range events {
			key := event.SessionID + "\x00" + event.RunID
			if event.RunID == "" {
				continue
			}
			if expected[key] == nil {
				expected[key] = &plain{}
			}
			switch event.Type {
			case "model.response":
				expected[key].modelMS += int64Value(event.Data["duration_ms"])
				expected[key].responses++
				if timings := mapField(event.Data["timings"]); timings != nil {
					if _, ok := floatValue(timings["predicted_ms"]); ok {
						expected[key].withTimings++
					}
				}
			case "tool.result":
				expected[key].toolMS += int64Value(event.Data["ms"])
			case "compaction":
				expected[key].compactions++
			case "compaction.summary":
				expected[key].compactionMS += int64Value(event.Data["duration_ms"])
			case "model.retry":
				expected[key].retries++
			}
		}

		stopped := map[string]bool{}
		for _, event := range events {
			if event.Type == "run.stopped" && event.RunID != "" {
				stopped[event.SessionID+"\x00"+event.RunID] = true
			}
			fields := tally.Observe(event.Type, event.SessionID, event.RunID, event.TS, event.Data)
			if event.Type != "run.stopped" {
				if fields != nil {
					t.Fatalf("%s: %s produced run.stopped fields", name, event.Type)
				}
				continue
			}
			folded++
			key := event.SessionID + "\x00" + event.RunID
			want := expected[key]
			if want == nil {
				t.Fatalf("%s: a run stopped that the dumb pass never saw", name)
			}
			var outcome RunOutcome
			encoded, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &outcome); err != nil {
				t.Fatal(err)
			}
			where := name + " run " + event.RunID
			if outcome.Time.ModelMS != want.modelMS {
				t.Errorf("%s: model %d ms, journal says %d", where, outcome.Time.ModelMS, want.modelMS)
			}
			if outcome.Time.ToolMS != want.toolMS {
				t.Errorf("%s: tools %d ms, journal says %d", where, outcome.Time.ToolMS, want.toolMS)
			}
			if outcome.Time.CompactionMS != want.compactionMS {
				t.Errorf("%s: compaction %d ms, journal says %d", where, outcome.Time.CompactionMS, want.compactionMS)
			}
			if outcome.Compactions != want.compactions {
				t.Errorf("%s: %d compactions, journal says %d", where, outcome.Compactions, want.compactions)
			}
			if outcome.Retries != want.retries {
				t.Errorf("%s: %d retries, journal says %d", where, outcome.Retries, want.retries)
			}
			// The narrowing, on real material: a run whose server reported no
			// timings carries NO prompt or generation figure at all. Not a zero.
			if want.withTimings == 0 && (outcome.Time.PromptMS != nil || outcome.Time.GenerationMS != nil) {
				t.Errorf("%s: no journalled timings, yet the fold reported prompt/generation", where)
			}
			if want.withTimings > 0 && (outcome.Time.PromptMS == nil || outcome.Time.GenerationMS == nil) {
				t.Errorf("%s: %d responses carried timings, yet the fold reported none", where, want.withTimings)
			}
			// Nothing may exceed the wall clock except tool time, which is a sum of
			// spans that overlap when tools run in parallel.
			if outcome.Time.TotalMS > 0 {
				for label, value := range map[string]int64{
					"model": outcome.Time.ModelMS, "waiting": outcome.Time.WaitingMS,
					"compaction": outcome.Time.CompactionMS, "unaccounted": outcome.Time.UnaccountedMS,
				} {
					if value > outcome.Time.TotalMS {
						t.Errorf("%s: %s is %d ms of a %d ms run", where, label, value, outcome.Time.TotalMS)
					}
				}
			}
			if outcome.Time.TotalMS < 0 || outcome.Time.UnaccountedMS < 0 {
				t.Errorf("%s: negative time %+v", where, outcome.Time)
			}
			// The figures themselves, so a reader of the log sees what a real run
			// actually looked like rather than only that the sums agreed.
			t.Logf("%s: total=%dms model=%dms (prompt=%s generation=%s) tools=%dms waiting=%dms compaction=%dms unaccounted=%dms retries=%d compactions=%d empty=%d repeated=%d",
				where, outcome.Time.TotalMS, outcome.Time.ModelMS,
				reported(outcome.Time.PromptMS), reported(outcome.Time.GenerationMS),
				outcome.Time.ToolMS, outcome.Time.WaitingMS, outcome.Time.CompactionMS, outcome.Time.UnaccountedMS,
				outcome.Retries, outcome.Compactions, outcome.EmptyReplies, outcome.RepeatedCalls)
		}
		// A run the tally saw stop is forgotten, so a long-lived process does not
		// carry an accumulator per run forever. Anything still in the map must be a
		// run this journal never shows stopping.
		for key := range tally.runs {
			if stopped[key] {
				t.Errorf("%s: run %q stopped and is still accumulating", name, strings.ReplaceAll(key, "\x00", "/"))
			}
		}
	}
	if folded == 0 {
		t.Fatal("no run in any retained journal stopped, so nothing was checked")
	}
	t.Logf("folded %d stopped runs out of the retained journals", folded)
}

// Two cards open at once is ONE wait. Summing them would report more waiting
// than the run took, which is the kind of number that discredits a summary.
func TestOverlappingCardsAreOneWait2ji(t *testing.T) {
	at := func(seconds int) string {
		return time.Date(2026, 9, 26, 12, 0, seconds, 0, time.UTC).Format(time.RFC3339)
	}
	tally := NewRunTally()
	tally.Observe("run.started", "s", "r", at(0), nil)
	tally.Observe("approval.required", "s", "r", at(10), map[string]any{"call_id": "a"})
	tally.Observe("approval.required", "s", "r", at(12), map[string]any{"call_id": "b"})
	tally.Observe("approval.decided", "s", "r", at(20), map[string]any{"call_id": "a"})
	tally.Observe("approval.decided", "s", "r", at(22), map[string]any{"call_id": "b"})
	fields := tally.Observe("run.stopped", "s", "r", at(30), map[string]any{"reason": "done"})
	timeFields, _ := fields["time"].(map[string]any)
	if got := int64Value(timeFields["waiting_ms"]); got != 12_000 {
		t.Fatalf("two overlapping cards, 10s to 22s, should be 12s of waiting; got %d ms", got)
	}
	if got := int64Value(timeFields["total_ms"]); got != 30_000 {
		t.Fatalf("total %d ms, want 30000", got)
	}
}

// A card still open when the run stopped was open until it stopped, and the queue
// span before the run started is waiting too.
func TestWaitingCoversTheQueueAndAnOpenCard2ji(t *testing.T) {
	at := func(seconds int) string {
		return time.Date(2026, 9, 26, 12, 0, seconds, 0, time.UTC).Format(time.RFC3339)
	}
	tally := NewRunTally()
	tally.Observe("run.queued", "s", "r", at(0), nil)
	tally.Observe("run.started", "s", "r", at(5), nil)
	tally.Observe("approval.required", "s", "r", at(8), map[string]any{"call_id": "a"})
	fields := tally.Observe("run.stopped", "s", "r", at(20), map[string]any{"reason": "user_stop"})
	timeFields, _ := fields["time"].(map[string]any)
	// 5s queued plus 12s with a card open and nobody answering.
	if got := int64Value(timeFields["waiting_ms"]); got != 17_000 {
		t.Fatalf("waiting %d ms, want 17000", got)
	}
	// And the total spans from QUEUED, because that is when he started waiting.
	if got := int64Value(timeFields["total_ms"]); got != 20_000 {
		t.Fatalf("total %d ms, want 20000", got)
	}
}

// A repeated call is the same tool with the same arguments. A different argument
// is a different call, and counting otherwise would make the figure meaningless.
func TestRepeatedCallsAndEmptyReplies2ji(t *testing.T) {
	tally := NewRunTally()
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	tally.Observe("run.started", "s", "r", now, nil)
	read := func(path string) map[string]any {
		return map[string]any{"name": "read_file", "args": map[string]any{"path": path}}
	}
	tally.Observe("tool.call", "s", "r", now, read("NOTES.md"))
	tally.Observe("tool.call", "s", "r", now, read("PLAN.md"))
	tally.Observe("tool.call", "s", "r", now, read("NOTES.md"))
	tally.Observe("tool.call", "s", "r", now, read("NOTES.md"))
	// An empty reply is nothing said and nothing called. A reply that called a
	// tool is not empty, however little text it carried.
	tally.Observe("model.response", "s", "r", now, map[string]any{"content": "", "tool_calls": []any{map[string]any{"id": "1"}}, "duration_ms": 10})
	tally.Observe("model.response", "s", "r", now, map[string]any{"content": "   ", "duration_ms": 10})
	tally.Observe("model.response", "s", "r", now, map[string]any{"content": "here you go", "duration_ms": 10})
	tally.Observe("model.retry", "s", "r", now, nil)
	fields := tally.Observe("run.stopped", "s", "r", now, map[string]any{"reason": "done"})
	if got := int64Value(fields["repeated_calls"]); got != 2 {
		t.Errorf("repeated calls %d, want 2 (NOTES.md twice more)", got)
	}
	if got := int64Value(fields["empty_replies"]); got != 1 {
		t.Errorf("empty replies %d, want 1", got)
	}
	if got := int64Value(fields["retries"]); got != 1 {
		t.Errorf("retries %d, want 1", got)
	}
	// And the run is forgotten once it has stopped.
	if _, still := tally.runs["s\x00r"]; still {
		t.Error("a stopped run is still accumulating")
	}
}

// The narrowing, stated as a test: a server that reports nothing gets no figure,
// and one that reports something gets that figure and not a rounding of it.
func TestABucketAServerCannotSupplyIsAbsent2ji(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	silent := NewRunTally()
	silent.Observe("run.started", "s", "r", now, nil)
	silent.Observe("model.response", "s", "r", now, map[string]any{"content": "hi", "duration_ms": 900})
	fields := silent.Observe("run.stopped", "s", "r", now, nil)
	timeFields, _ := fields["time"].(map[string]any)
	if _, present := timeFields["prompt_ms"]; present {
		t.Error("a server that reported no timings must supply no prompt_ms, not a zero")
	}
	if _, present := timeFields["generation_ms"]; present {
		t.Error("a server that reported no timings must supply no generation_ms, not a zero")
	}
	if got := int64Value(timeFields["model_ms"]); got != 900 {
		t.Errorf("model_ms %d, want 900 -- what WE measured is always available", got)
	}

	// llm.Timings is a named map type, which no assertion to map[string]any
	// matches. This is the negative control for that: the fold must read it.
	type namedTimings map[string]any
	reporting := NewRunTally()
	reporting.Observe("run.started", "s", "r", now, nil)
	reporting.Observe("model.response", "s", "r", now, map[string]any{
		"content": "hi", "duration_ms": 900,
		"timings": namedTimings{"prompt_ms": 671.79, "predicted_ms": 1884.797},
	})
	fields = reporting.Observe("run.stopped", "s", "r", now, nil)
	timeFields, _ = fields["time"].(map[string]any)
	if got := int64Value(timeFields["prompt_ms"]); got != 672 {
		t.Errorf("prompt_ms %d, want 672", got)
	}
	if got := int64Value(timeFields["generation_ms"]); got != 1885 {
		t.Errorf("generation_ms %d, want 1885", got)
	}
}

// reported prints a bucket the server may not have supplied. "not reported" is
// the words the hover uses too, deliberately: absent is a state, not a zero.
func reported(value *int64) string {
	if value == nil {
		return "not reported"
	}
	return strconv.FormatInt(*value, 10) + "ms"
}

// The negative control for the trap that actually bit: live, tool_calls is
// []events.ToolCall and timings is llm.Timings, both NAMED types that no
// assertion to []any or map[string]any matches. The journals decode to the
// unnamed forms, so a journal test cannot catch this. A screenshot did.
func TestNamedTypesOffTheLiveBusAreRead2ji(t *testing.T) {
	type toolCall struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	tally := NewRunTally()
	tally.Observe("run.started", "s", "r", now, nil)
	// A turn that called a tool. It said nothing, and it is NOT an empty reply.
	tally.Observe("model.response", "s", "r", now, map[string]any{
		"content":     "",
		"tool_calls":  []toolCall{{ID: "1", Name: "read_file"}},
		"duration_ms": 10,
	})
	// A turn that said nothing and called nothing. That one is.
	tally.Observe("model.response", "s", "r", now, map[string]any{
		"content": "", "tool_calls": []toolCall{}, "duration_ms": 10,
	})
	fields := tally.Observe("run.stopped", "s", "r", now, map[string]any{"reason": "done"})
	if got := int64Value(fields["empty_replies"]); got != 1 {
		t.Fatalf("empty replies %d, want 1 -- a tool-calling turn is not an empty reply", got)
	}
}
