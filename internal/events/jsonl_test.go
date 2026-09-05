package events

import (
	"os"
	"strings"
	"testing"
)

func TestCloseSessionReleasesWriter(t *testing.T) {
	writers, err := NewWriters(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	path, err := writers.OpenSession("main")
	if err != nil {
		t.Fatal(err)
	}
	if err := writers.Write(New(ToolResult, "main", "run", map[string]any{"ok": true})); err != nil {
		t.Fatal(err)
	}
	if err := writers.CloseSession("main"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("session log remained open: %v", err)
	}
}

func TestWriteReturnsMarshalFailure(t *testing.T) {
	writers, err := NewWriters(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	if err := writers.Write(New(Error, "", "", make(chan int))); err == nil {
		t.Fatal("unsupported event data was reported as persisted")
	}
}

func TestWritersIndexCompactedHistoryWithoutRetainingBodies(t *testing.T) {
	writers, err := NewWriters(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	if _, err := writers.OpenSession("main"); err != nil {
		t.Fatal(err)
	}
	ok := true
	call := Message{ID: "m-1", Role: "assistant", Category: "history", Turn: 1, ToolCalls: []ToolCall{{ID: "call-1", Name: "read_file", Arguments: `{"path":"app.css","offset":1,"content":"secret"}`}}}
	result := Message{ID: "m-2", Role: "tool", Category: "files", Turn: 1, ToolCallID: "call-1", Name: "read_file", Content: "complete recorded body", OK: &ok}
	for _, message := range []Message{call, result} {
		if err := writers.Write(New(MessageAppended, "main", "run", map[string]any{"message": message})); err != nil {
			t.Fatal(err)
		}
	}
	if err := writers.Write(New(MessageUpdated, "main", "run", map[string]any{"id": "m-2", "patch": map[string]any{"content": "[elided]", "elided": true}})); err != nil {
		t.Fatal(err)
	}
	if err := writers.Write(New(Compaction, "main", "run", map[string]any{"kind": "summarize", "summary_message_id": "m-3", "affected_ids": []string{"m-1", "m-2"}})); err != nil {
		t.Fatal(err)
	}

	manifest, err := writers.ResolveHistory("main", "latest")
	if err != nil || manifest.Kind != "manifest" || len(manifest.Entries) != 2 {
		t.Fatalf("manifest=%+v err=%v", manifest, err)
	}
	if got := manifest.Entries[0].Calls[0].Arguments; strings.Contains(got, "secret") || !strings.Contains(got, "[omitted]") {
		t.Fatalf("manifest arguments were not sanitized: %s", got)
	}
	lookup, err := writers.ResolveHistory("main", "m-2")
	if err != nil || lookup.Message.Content != "complete recorded body" || lookup.Message.Elided {
		t.Fatalf("lookup=%+v err=%v", lookup, err)
	}
	for _, record := range writers.history["main"].records {
		if record.meta.Content != "" || record.full != nil {
			t.Fatalf("active index retained a message body: %+v", record)
		}
	}
}

func TestOpenSessionResetsHistoryIndex(t *testing.T) {
	writers, err := NewWriters(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	if _, err := writers.OpenSession("main"); err != nil {
		t.Fatal(err)
	}
	if err := writers.Write(New(MessageAppended, "main", "run", map[string]any{"message": Message{ID: "m-1", Role: "tool", Content: "old"}})); err != nil {
		t.Fatal(err)
	}
	if _, err := writers.OpenSession("main"); err != nil {
		t.Fatal(err)
	}
	if _, err := writers.ResolveHistory("main", "m-1"); err == nil {
		t.Fatal("history survived session log reset")
	}
}

func TestHistorySummaryRefExcludesUnrelatedElision(t *testing.T) {
	index := newHistoryIndex()
	for _, message := range []Message{
		{ID: "m-1", Role: "tool", Content: "first"},
		{ID: "m-2", Role: "tool", Content: "second"},
		{ID: "m-3", Role: "tool", Content: "unrelated"},
		{ID: "s-1", Role: "user", Category: "summary", Content: "first summary"},
		{ID: "s-2", Role: "user", Category: "summary", Content: "second summary"},
	} {
		index.append(message, historyLocation{}, true)
	}
	index.compact("s-1", []string{"m-1"})
	index.compact("s-2", []string{"s-1", "m-2"})
	index.elided["m-3"] = true

	branch, err := index.resolveReplay("s-1")
	if err != nil || len(branch.Entries) != 1 || branch.Entries[0].Ref != "m-1" {
		t.Fatalf("summary branch=%+v err=%v", branch, err)
	}
	latest, err := index.resolveReplay("latest")
	if err != nil || len(latest.Entries) != 4 {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
}
