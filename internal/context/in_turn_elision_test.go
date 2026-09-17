package contextmgr

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"harness/internal/events"
	"harness/internal/session"
)

func ok() *bool { value := true; return &value }

// s7r2Turn is the shape of s7's run r2 (2026-09-17): the user's question, one
// assistant message calling six reads, and their results, with token weights
// from the tape. Before item 2et every result was pinned, so nothing inside the
// turn could shrink and the run stopped context_exhausted.
func s7r2Turn() []events.Message {
	messages := []events.Message{
		{ID: "m-40", Role: "user", Category: "history", Content: "work on the runbook", Tokens: 20},
		{ID: "m-41", Role: "tool", Name: "read_file", Category: "files", ToolCallID: "old", Content: "old read", Tokens: 3000, OK: ok()},
		{ID: "m-60", Role: "user", Category: "history", Content: "more wondering about the rpg we were working on", Tokens: 12},
	}
	assistant := events.Message{ID: "m-61", Role: "assistant", Category: "history", Content: "reading", Tokens: 40}
	reads := []struct {
		path   string
		tokens int
	}{{"game.js", 3100}, {"index.html", 70}, {"map.js", 160}, {"blade-quest/index.html", 8200}, {"map.js", 2900}, {"index.html", 1650}}
	results := []events.Message{}
	for index, read := range reads {
		id := fmt.Sprintf("call-%d", index)
		args, _ := json.Marshal(map[string]any{"path": read.path})
		assistant.ToolCalls = append(assistant.ToolCalls, events.ToolCall{ID: id, Name: "read_file", Arguments: string(args)})
		results = append(results, events.Message{ID: fmt.Sprintf("m-%d", 62+index), Role: "tool", Name: "read_file", Category: "files", ToolCallID: id, Content: strings.Repeat("x", read.tokens*3), Tokens: read.tokens, OK: ok()})
	}
	messages = append(messages, assistant)
	return append(messages, results...)
}

func TestInTurnResultsElideOldestFirstKeepingTheTaskAndTheRecentWindow(t *testing.T) {
	messages := s7r2Turn()
	s := &session.Session{ID: "s7"}
	s.ReplaceMessages(messages)
	s.SetRunPin("m-60")
	used := tokenSum(messages)
	count := func(text string) (int, bool) { return len(text) / 3, true }
	changed, after := New(events.NewBus()).ElideOld(s, "r2", used, used-6000, 16384, count)
	if !changed || after > used-6000 {
		t.Fatalf("in-turn elision did not make room: changed=%t used %d → %d", changed, used, after)
	}
	byID := map[string]events.Message{}
	for _, message := range s.MessagesCopy() {
		byID[message.ID] = message
	}
	for _, id := range []string{"m-60", "m-61"} {
		if byID[id].Elided {
			t.Fatalf("%s (the task or the model's own message) was elided", id)
		}
	}
	for _, id := range []string{"m-64", "m-65", "m-66", "m-67"} {
		if byID[id].Elided {
			t.Fatalf("%s is inside the recent window of %d and was elided", id, RecentToolWindow)
		}
	}
	if !byID["m-41"].Elided || !byID["m-62"].Elided {
		t.Fatalf("the oldest results were not elided first: m-41=%t m-62=%t", byID["m-41"].Elided, byID["m-62"].Elided)
	}
	if !strings.HasPrefix(byID["m-62"].Content, "[elided: read_file") {
		t.Fatalf("an in-turn result must become a stub, never a summary: %q", byID["m-62"].Content)
	}
}

func TestAMissingPinStillProtectsEverything(t *testing.T) {
	messages := s7r2Turn()
	s := &session.Session{ID: "s7"}
	s.ReplaceMessages(messages)
	s.SetRunPin("m-does-not-exist")
	used := tokenSum(messages)
	if changed, _ := New(events.NewBus()).ElideOld(s, "r2", used, 0, 16384, func(text string) (int, bool) { return len(text) / 3, true }); changed {
		t.Fatal("a pin that is set but missing must protect everything")
	}
}

func TestRestoreStubsResultsOlderThanTheLastUserTurn(t *testing.T) {
	restored := StubOlderResults(s7r2Turn(), 16384)
	for _, message := range restored {
		switch message.ID {
		case "m-41":
			if !message.Elided || !strings.HasPrefix(message.Content, "[elided: read_file") {
				t.Fatalf("an older result was restored with its bytes: %+v", message)
			}
		case "m-62", "m-65":
			if message.Elided {
				t.Fatalf("%s belongs to the last turn and must stay verbatim", message.ID)
			}
		}
	}
}
