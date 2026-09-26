package main

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Item 2lv (e): a real retained journal as stored, a JSON array, a file with a
// corrupt line in the middle, and empty input. Those are the item's four cases
// and they are the four here.

func journal(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", "internal", "projection", "testdata", "pins", "sources", name)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no journal pin %s: %v", name, err)
	}
	if strings.HasSuffix(name, ".gz") {
		reader, err := gzip.NewReader(strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		plain := &strings.Builder{}
		buffer := make([]byte, 1<<16)
		for {
			read, err := reader.Read(buffer)
			plain.Write(buffer[:read])
			if err != nil {
				break
			}
		}
		return plain.String()
	}
	return string(body)
}

// (a): the only form a journal is ever stored in.
func TestARetainedJournalAsStoredIsRead2lv(t *testing.T) {
	text := journal(t, "approval-parallel-turn2.events")
	result, err := read(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.events) == 0 {
		t.Fatal("a real journal yielded no events, which is the whole defect this item fixes")
	}
	if result.isArray {
		t.Error("a JSONL journal was read as an array")
	}
	if len(result.bad) != 0 {
		t.Errorf("a good journal reported %d unreadable line(s): %v", len(result.bad), result.bad)
	}
	// Every event must have a type, or the detectors are being fed noise.
	for index, event := range result.events {
		if event.Type == "" {
			t.Fatalf("event %d has no type", index)
		}
	}
	t.Logf("read %d events from %d lines (%d wrapped, %d bare)", len(result.events), result.linesRead, result.wrapped, result.bare)
}

// And a compressed pin, decompressed, is still a journal — the same shape at
// far greater length, which is where a buffer limit would show.
func TestALargeJournalIsRead2lv(t *testing.T) {
	text := journal(t, "known-good-live.events.gz")
	result, err := read(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.events) < 1000 {
		t.Fatalf("a 2,828-record journal yielded %d events", len(result.events))
	}
	if len(result.bad) != 0 {
		t.Errorf("reported %d unreadable line(s): %v", len(result.bad), result.bad[:min(3, len(result.bad))])
	}
}

// (b): the array form, kept because rel-1.19.0/W0 found tests/progress-replay.mjs
// feeding it one. It is decided BY LOOKING, not by a flag.
func TestAJSONArrayIsStillRead2lv(t *testing.T) {
	result, err := read(strings.NewReader(`[{"type":"tool.result","run_id":"r1","data":{"ok":true}},{"type":"model.response","run_id":"r1","data":{}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.isArray {
		t.Error("an array was not recognised as one")
	}
	if len(result.events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(result.events))
	}
	// Leading whitespace must not change the answer, because a file written by
	// something else may well have some.
	spaced, err := read(strings.NewReader("\n  \t [{\"type\":\"stage\"}]"))
	if err != nil {
		t.Fatal(err)
	}
	if !spaced.isArray || len(spaced.events) != 1 {
		t.Fatalf("leading whitespace changed the decision: %+v", spaced)
	}
}

// (c): a corrupt line in the MIDDLE is reported with its number and does not
// abandon the run. A journal being diagnosed is often a journal that went
// wrong, so stopping at the first bad line defeats the tool.
func TestACorruptLineIsNamedAndSkipped2lv(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"run.started","run_id":"r1"}`,
		`{"type":"model.request","run_id":"r1"}`,
		`{"type":"tool.result","run_id":"r1",`, // truncated mid-object
		`not json at all`,
		`{"type":"model.response","run_id":"r1"}`,
		`{"run_id":"r1"}`, // valid JSON, no type
		`{"type":"run.stopped","run_id":"r1"}`,
	}, "\n")
	result, err := read(strings.NewReader(input))
	if err != nil {
		t.Fatalf("a corrupt line ended the read: %v", err)
	}
	// The four good lines survived.
	if len(result.events) != 4 {
		t.Fatalf("expected the 4 readable events, got %d", len(result.events))
	}
	if len(result.bad) != 3 {
		t.Fatalf("expected 3 unreadable lines, got %d: %v", len(result.bad), result.bad)
	}
	// (c): WITH ITS LINE NUMBER. A count of failures is not a diagnosis; the
	// line number is what lets somebody open the file and look.
	for _, want := range []string{"line 3:", "line 4:", "line 6:"} {
		found := false
		for _, message := range result.bad {
			if strings.HasPrefix(message, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no report for %s; got %v", want, result.bad)
		}
	}
	// And the last good line after the corruption was still read, which is the
	// difference between skipping and abandoning.
	if result.events[len(result.events)-1].Type != "run.stopped" {
		t.Errorf("the run did not continue past the corruption: last event is %q", result.events[len(result.events)-1].Type)
	}
}

// (e): empty input. Not a crash, and not silence either.
func TestEmptyInputIsNeitherACrashNorSilence2lv(t *testing.T) {
	for _, probe := range []string{"", "\n", "   \n\t\n"} {
		result, err := read(strings.NewReader(probe))
		if err != nil {
			t.Errorf("empty input errored: %v", err)
		}
		if len(result.events) != 0 {
			t.Errorf("empty input produced %d events", len(result.events))
		}
	}
	// A file of nothing but blank lines is empty, not corrupt: blank lines are
	// skipped without being reported, or a journal with a trailing newline would
	// report a defect it does not have.
	result, _ := read(strings.NewReader("\n\n\n"))
	if len(result.bad) != 0 {
		t.Errorf("blank lines were reported as unreadable: %v", result.bad)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
