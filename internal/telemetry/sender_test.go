package telemetry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReceiverRefusalIsCountedDroppedAndNeverRetried2ov(t *testing.T) {
	requests := 0
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(w, "rate_limited", http.StatusTooManyRequests)
	}))
	defer receiver.Close()
	root := t.TempDir()
	sender := New(Options{Endpoint: receiver.URL, InstallID: "id", DataRoot: root, AgentVersion: "v1.51.0"})
	defer sender.Close()
	sender.Observe("tool.result", time.Now().UTC().Format(time.RFC3339), map[string]any{"name": "read_file", "ok": true, "ms": 1})
	sender.Flush()
	sender.drainQueue()
	if requests != 1 || sender.Refused() != 1 {
		t.Fatalf("requests=%d refused=%d", requests, sender.Refused())
	}
	if entries, err := os.ReadDir(filepath.Join(root, queueDirectory)); err == nil && len(entries) != 0 {
		t.Fatalf("refused batch was queued: %v", entries)
	}
}

type capture struct {
	mu     sync.Mutex
	bodies [][]byte
	fail   bool
}

func (c *capture) transport(_ context.Context, body []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return errUnreachable
	}
	c.bodies = append(c.bodies, append([]byte(nil), body...))
	return nil
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.bodies)
}

var errUnreachable = &unreachable{}

type unreachable struct{}

func (*unreachable) Error() string { return "receiver unreachable" }

func newSender(t *testing.T, sink *capture) (*Sender, string) {
	t.Helper()
	root := t.TempDir()
	sender := New(Options{
		InstallID: "11111111-2222-4333-8444-555555555555",
		DataRoot:  root, AgentVersion: "v1.18.0",
		Transport: sink.transport,
		Now:       func() time.Time { return time.Date(2026, 9, 26, 17, 0, 0, 0, time.UTC) },
	})
	if sender == nil {
		t.Fatal("the sender did not start")
	}
	t.Cleanup(sender.Close)
	return sender, root
}

// Item 2jg (d): OFF MEANS OFF, and the proof is an absence.
//
// The temptation with a switch is to test the flag. A flag says what the code
// believes about itself. What this asserts instead is that with telemetry off
// there is no sender at all — so nothing can be collected, nothing can be
// queued, and nothing can be handed to the transport, whatever any flag says.
func TestOffMeansThereIsNoSender2jg(t *testing.T) {
	sink := &capture{}
	// No endpoint and no transport is the configuration a fresh install has, and
	// the only honest answer is nothing at all rather than a sender with nowhere
	// to send.
	if sender := New(Options{DataRoot: t.TempDir()}); sender != nil {
		t.Fatal("a sender started with no endpoint; it would collect into a queue nobody drains")
	}
	// And when one has been running, closing it takes the queue with it.
	sender, root := newSender(t, sink)
	sink.mu.Lock()
	sink.fail = true
	sink.mu.Unlock()
	sender.Observe("run.stopped", "2026-09-26T17:00:00Z", map[string]any{"reason": "done", "turns": 3})
	sender.Flush()
	queue := filepath.Join(root, queueDirectory)
	if entries, err := os.ReadDir(queue); err != nil || len(entries) == 0 {
		t.Fatalf("an unreachable receiver should have queued the batch: %v", err)
	}
	sender.Close()
	if _, err := os.Stat(queue); !os.IsNotExist(err) {
		t.Fatalf("the queue survived the switch going off: %v", err)
	}
}

// An event the allow-list drops never reaches a batch, so "nothing is collected"
// is true of content even while the sender is running.
// Item 2pw: a receiver that refuses the recorder's types refuses their batch
// only, never the older events'.
func TestTheRecorderTypesTravelInABatchOfTheirOwn2pw(t *testing.T) {
	sink := &capture{}
	sender, _ := newSender(t, sink)
	sender.Observe("tool.result", "2026-10-04T17:00:00Z", map[string]any{"name": "read_file", "ok": true, "ms": 3})
	sender.Observe("run.summary", "2026-10-04T17:00:00Z", map[string]any{"inference_calls": 1, "stop_reason": "done"})
	sender.Flush()
	if sink.count() != 2 || strings.Contains(string(sink.bodies[0]), "run.summary") || !strings.Contains(string(sink.bodies[1]), "run.summary") {
		t.Fatalf("batches: %q", sink.bodies)
	}
}

func TestADroppedTypeNeverReachesABatch2jg(t *testing.T) {
	sink := &capture{}
	sender, _ := newSender(t, sink)
	sender.Observe("model.response", "2026-09-26T17:00:00Z", map[string]any{"content": "the model said this"})
	sender.Observe("message.appended", "2026-09-26T17:00:00Z", map[string]any{"text": "what the operator typed"})
	sender.Flush()
	if sink.count() != 0 {
		t.Fatalf("a dropped type produced %d batch(es)", sink.count())
	}
}

func TestInvalidCrashTreeIsDroppedAndCounted2p7(t *testing.T) {
	sink := &capture{}
	sender, _ := newSender(t, sink)
	tree := validCrashTree2p7()
	tree["function"] = "main.secretFunction"
	sender.Observe("error", "2026-09-26T17:00:00Z", map[string]any{"where": "host_window", "class": "crash", "stack_tree": tree})
	sender.Flush()
	if sink.count() != 0 || sender.InvalidDropped() != 1 {
		t.Fatalf("sent=%d invalid_dropped=%d", sink.count(), sender.InvalidDropped())
	}
}

// The acceptance line: a run.stopped arrives as one allow-listed event with the
// reason and the counts and nothing else.
func TestARunStoppedArrivesAsCountsAndNothingElse2jg(t *testing.T) {
	sink := &capture{}
	sender, _ := newSender(t, sink)
	sender.Observe("run.stopped", "2026-09-26T17:00:00Z", map[string]any{
		"reason": "done", "turns": 7, "model_class": "local",
		"total_ms": 130000, "model_ms": 100000, "tool_ms": 18000, "waiting_ms": 12000,
		"compaction_ms": 0, "retries": 1, "compactions": 0, "empty_replies": 0, "repeated_calls": 0,
		// Everything below must not survive.
		"run_id": "r42", "detail": "the workspace at C:\work\acme\\project",
		"armed_detectors": []any{"novel_action"}, "queue_held": false,
	})
	sender.Flush()
	if sink.count() != 1 {
		t.Fatalf("expected one batch, got %d", sink.count())
	}
	body := string(sink.bodies[0])
	for _, forbidden := range []string{"r42", "acme", "novel_action", "queue_held", "detail"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("%q left the machine:\n%s", forbidden, body)
		}
	}
	var batch struct {
		Schema    int              `json:"schema"`
		InstallID string           `json:"install_id"`
		Events    []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(sink.bodies[0], &batch); err != nil {
		t.Fatal(err)
	}
	if batch.Schema != SchemaVersion || batch.InstallID == "" || len(batch.Events) != 1 {
		t.Fatalf("envelope: %+v", batch)
	}
	event := batch.Events[0]
	if event["reason"] != "done" || event["model_class"] != "local" {
		t.Errorf("event: %v", event)
	}
	if _, present := event["prompt_ms"]; present {
		t.Error("prompt_ms was absent in the run and the batch invented it")
	}
}

// Offline then online: the queue drains in order, and a batch older than the
// backlog window is dropped rather than sent stale.
func TestTheQueueDrainsInOrderAndExpires2jg(t *testing.T) {
	sink := &capture{}
	now := time.Date(2026, 9, 26, 17, 0, 0, 0, time.UTC)
	root := t.TempDir()
	sender := New(Options{
		InstallID: "id", DataRoot: root, Transport: sink.transport,
		Now: func() time.Time { return now },
	})
	if sender == nil {
		t.Fatal("no sender")
	}
	defer sender.Close()

	sink.mu.Lock()
	sink.fail = true
	sink.mu.Unlock()
	for index := 0; index < 3; index++ {
		sender.Observe("run.stopped", "2026-09-26T17:00:00Z", map[string]any{"reason": "done", "turns": index})
		sender.Flush()
		now = now.Add(time.Second)
	}
	queue := filepath.Join(root, queueDirectory)
	entries, err := os.ReadDir(queue)
	if err != nil || len(entries) != 3 {
		t.Fatalf("expected three queued batches, got %d (%v)", len(entries), err)
	}

	// Age the oldest past the window, then come back online.
	oldest := filepath.Join(queue, entries[0].Name())
	stale := now.Add(-BacklogMaxAge - time.Hour)
	if err := os.Chtimes(oldest, stale, stale); err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	sink.fail = false
	sink.mu.Unlock()
	sender.drainQueue()

	if sink.count() != 2 {
		t.Fatalf("expected the two live batches to send, got %d", sink.count())
	}
	if remaining, _ := os.ReadDir(queue); len(remaining) != 0 {
		t.Fatalf("%d batch(es) left in the queue", len(remaining))
	}
	// In order: turn 1 then turn 2, the stale turn 0 dropped.
	for index, body := range sink.bodies {
		var batch struct {
			Events []map[string]any `json:"events"`
		}
		if err := json.Unmarshal(body, &batch); err != nil {
			t.Fatal(err)
		}
		if got := batch.Events[0]["turns"]; got != float64(index+1) {
			t.Errorf("batch %d carries turn %v, want %d", index, got, index+1)
		}
	}
}

// A batch is bounded. 200 events force a flush, and the byte cap splits what is
// still too big rather than posting it.
func TestBatchesAreBounded2jg(t *testing.T) {
	sink := &capture{}
	sender, _ := newSender(t, sink)
	for index := 0; index < BatchEventCap; index++ {
		sender.Observe("run.stopped", "2026-09-26T17:00:00Z", map[string]any{"reason": "done", "turns": index})
	}
	if sink.count() == 0 {
		t.Fatal("200 events did not force a batch")
	}
	for _, body := range sink.bodies {
		if len(body) > BatchByteCap {
			t.Errorf("a batch of %d bytes exceeded the %d cap", len(body), BatchByteCap)
		}
	}
}

// A new install id cannot be joined to the old one.
func TestEachInstallIDIsNewAndWellFormed2jg(t *testing.T) {
	seen := map[string]bool{}
	for index := 0; index < 50; index++ {
		id := NewInstallID()
		if len(id) != 36 || strings.Count(id, "-") != 4 || id[14] != '4' {
			t.Fatalf("not a v4 UUID: %q", id)
		}
		if seen[id] {
			t.Fatalf("a repeated install id after %d draws", index)
		}
		seen[id] = true
	}
}

// Item 2lx: the two counts the reflection floor reads are the two counts
// telemetry sends — and (d) is the whole justification: they are COUNTS and
// nothing more.
func TestTheTwoCountsAreSentAndCarryNothingElse2lx(t *testing.T) {
	sink := &capture{}
	sender, _ := newSender(t, sink)
	sender.Observe("run.stopped", "2026-09-26T20:00:00Z", map[string]any{
		"reason": "done", "turns": 7,
		"model_calls": 12, "tool_calls": 9,
		// Everything a count must not drag along with it.
		"run_id": "r42", "detail": "read the file secret.go in the operator workspace",
		"armed_detectors": []any{"novel_action"},
	})
	sender.Flush()
	if sink.count() != 1 {
		t.Fatalf("expected one batch, got %d", sink.count())
	}
	body := string(sink.bodies[0])
	var batch struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(sink.bodies[0], &batch); err != nil {
		t.Fatal(err)
	}
	event := batch.Events[0]
	// (a): both arrived.
	if event["model_calls"] != float64(12) || event["tool_calls"] != float64(9) {
		t.Fatalf("the counts did not arrive: model=%v tool=%v", event["model_calls"], event["tool_calls"])
	}
	// (d): counts and nothing more. No tool name, no model name, no argument
	// content — asserted against the SERIALIZED batch rather than the struct,
	// because the bytes are what leaves the machine.
	for _, forbidden := range []string{"r42", "acme", "secret.go", "novel_action", "project"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("%q left the machine beside the counts:\n%s", forbidden, body)
		}
	}
}

// (b): the allow-list is STILL the authority. This item adds two entries to it
// and no way to send anything not on it.
func TestTheAllowListIsStillTheAuthority2lx(t *testing.T) {
	class, known := Classify("run.stopped")
	if !known || !class.Sent {
		t.Fatal("run.stopped is not sent")
	}
	picked := Pick(class, map[string]any{
		"model_calls": 3, "tool_calls": 1,
		"a_field_nobody_allow_listed": "should not travel",
	})
	if _, present := picked["a_field_nobody_allow_listed"]; present {
		t.Error("a field outside the allow-list was picked")
	}
	if picked["model_calls"] != 3 || picked["tool_calls"] != 1 {
		t.Errorf("the two counts were not picked: %v", picked)
	}
	// And a run journalled before item 2ls has neither — they are ABSENT rather
	// than zero, the same rule item 2ji's buckets live by, so a receiver can
	// tell "no calls" from "this build did not count them".
	older := Pick(class, map[string]any{"reason": "done", "turns": 2})
	for _, key := range []string{"model_calls", "tool_calls"} {
		if _, present := older[key]; present {
			t.Errorf("%s was invented for a run that never carried it", key)
		}
	}
}

// (c): off still means off, and the proof extends to the new fields — they are
// computed for the floor and never queued.
func TestTheCountsAreNotQueuedWhenTelemetryIsOff2lx(t *testing.T) {
	// No endpoint and no transport is a machine with telemetry off. The sender
	// does not exist, so there is nothing that could queue anything.
	if sender := New(Options{DataRoot: t.TempDir()}); sender != nil {
		t.Fatal("a sender started with telemetry off")
	}
	// And with it on, a DROPPED event carrying the same words never reaches a
	// batch either — the allow-list decides by type first.
	sink := &capture{}
	sender, _ := newSender(t, sink)
	sender.Observe("model.response", "2026-09-26T20:00:00Z", map[string]any{
		"model_calls": 12, "tool_calls": 9, "content": "the model said this",
	})
	sender.Flush()
	if sink.count() != 0 {
		t.Fatalf("a dropped type carrying the counts produced %d batch(es)", sink.count())
	}
}
