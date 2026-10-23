package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/memory"
	"harness/internal/session"
)

type recallResult struct {
	Entries []struct {
		Source  string          `json:"source"`
		Layer   string          `json:"layer"`
		Note    string          `json:"note"`
		Message *events.Message `json:"message"`
	} `json:"entries"`
	Total int `json:"total"`
}

func recallWithRecord(t *testing.T, messages ...string) (*Remember, *Recall, *session.Session) {
	t.Helper()
	base := t.TempDir()
	writers, err := events.NewWriters(filepath.Join(base, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	for index, content := range messages {
		message := events.Message{ID: fmt.Sprintf("m%d", index+1), Role: "tool", Category: "files", Name: "read_file", Content: content}
		if err := writers.Write(events.New(events.MessageAppended, "chat-1", "run", map[string]any{"message": message})); err != nil {
			t.Fatal(err)
		}
	}
	workspace := filepath.Join(base, "workspace")
	cfg := config.Defaults(workspace)
	cfg.Memory.Dir = filepath.Join(base, "memory")
	manager := memory.New(base, func() config.Config { return cfg }, nil)
	recall := NewRecall(manager, func() ChatHistoryReader { return writers })
	return NewRemember(manager, events.NewBus()), recall, &session.Session{ID: "chat-1", AgentID: "coder", Workspace: workspace, ToolsEnabled: map[string]bool{"recall": true}}
}

func callRecall(t *testing.T, recall *Recall, item *session.Session, args map[string]any) recallResult {
	t.Helper()
	outcome := New(recall).CallDetailed(context.Background(), item, "recall", args)
	if !outcome.OK {
		t.Fatalf("recall failed: %s", outcome.Content)
	}
	var result recallResult
	if err := json.Unmarshal([]byte(outcome.Content), &result); err != nil {
		t.Fatalf("recall=%s: %v", outcome.Content, err)
	}
	return result
}

// Item 2wd CHECK 2: with no arguments recall returns saved notes exactly as it
// did; with a query it finds this chat's record and saved notes, each labelled.
func TestRecallFindsChatRecordAndNotesLabelledBySource2wd(t *testing.T) {
	remember, recall, item := recallWithRecord(t, "port 4711 is the fixture port", "unrelated result", "port 4712 replaced it")
	item.Run.LastRunID = "r1"
	if _, err := remember.Call(context.Background(), item, map[string]any{"note": "fixture port lives in settings", "scope": "user"}); err != nil {
		t.Fatal(err)
	}
	if plain, err := recall.Call(context.Background(), item, nil); err != nil || !strings.HasPrefix(plain, "Agent memory:\n") || !strings.Contains(plain, "fixture port lives in settings") {
		t.Fatalf("no-argument recall=%q err=%v", plain, err)
	}
	result := callRecall(t, recall, item, map[string]any{"query": "PORT"})
	if result.Total != 3 || len(result.Entries) != 3 {
		t.Fatalf("result=%+v", result)
	}
	if result.Entries[0].Source != "chat" || result.Entries[0].Message.ID != "m3" || result.Entries[1].Message.ID != "m1" {
		t.Fatalf("chat entries are not newest first: %+v", result.Entries)
	}
	if note := result.Entries[2]; note.Source != "note" || note.Layer != "agent" || !strings.Contains(note.Note, "fixture port lives in settings") {
		t.Fatalf("note entry=%+v", note)
	}
}

// Item 2wd CHECK 2: offset and limit page the matches as chat_history's did.
func TestRecallRangesMatchesAsChatHistoryDid2wd(t *testing.T) {
	_, recall, item := recallWithRecord(t, "row 1", "row 2", "row 3", "row 4")
	page := callRecall(t, recall, item, map[string]any{"offset": float64(1), "limit": float64(2)})
	if page.Total != 4 || len(page.Entries) != 2 || page.Entries[0].Message.ID != "m3" || page.Entries[1].Message.ID != "m2" {
		t.Fatalf("page=%+v", page)
	}
	if outcome := New(recall).CallDetailed(context.Background(), item, "recall", map[string]any{"offset": float64(-1)}); outcome.OK {
		t.Fatalf("negative offset accepted: %s", outcome.Content)
	}
	if outcome := New(recall).CallDetailed(context.Background(), item, "recall", map[string]any{"limit": 2.5}); outcome.OK {
		t.Fatalf("fractional limit accepted: %s", outcome.Content)
	}
	if full := callRecall(t, recall, item, map[string]any{"limit": float64(500)}); len(full.Entries) != 4 {
		t.Fatalf("limit cap=%+v", full)
	}
	missing := &session.Session{ID: "chat-without-journal", Workspace: item.Workspace, ToolsEnabled: item.ToolsEnabled}
	if none := callRecall(t, recall, missing, map[string]any{"query": "row"}); none.Total != 0 || len(none.Entries) != 0 {
		t.Fatalf("missing journal=%+v", none)
	}
}

// Item 2qh CHECK 2, carried by 2wd: an earlier result removed from live context
// remains findable through recall, backed by this chat's own journal.
func TestRecallFindsPreFreshToolResult2qh(t *testing.T) {
	_, recall, item := recallWithRecord(t, "needle-before-fresh-boundary")
	result := callRecall(t, recall, item, map[string]any{"query": "needle-before-fresh-boundary", "limit": float64(5)})
	if len(result.Entries) != 1 || result.Entries[0].Message.ID != "m1" || !strings.Contains(result.Entries[0].Message.Content, "needle-before-fresh-boundary") {
		t.Fatalf("result=%+v", result)
	}
}

// Item 2wd CHECK 3: the description is the planner's wording, verbatim.
func TestRecallDescriptionSaysWhenToReachForIt2wd(t *testing.T) {
	want := "A detail from earlier in this chat or your saved notes is not in front of you — a path, a decision, an error message. Find it here instead of guessing."
	if got := NewRecall(nil, nil).Description(); got != want {
		t.Fatalf("description=%q", got)
	}
}
