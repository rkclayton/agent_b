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

func TestInTurnResultsElideKeepingTheTaskAndTheRecentWindow(t *testing.T) {
	messages := s7r2Turn()
	s := &session.Session{ID: "s7"}
	s.ReplaceMessages(messages)
	s.SetRunPin("m-60")
	used := tokenSum(messages)
	count := func(text string) (int, bool) { return len(text) / 3, true }
	changed, after := New(events.NewBus()).ElideOld(s, "r2", "test", used, used-6000, 16384, count)
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
		t.Fatalf("the eligible results outside the window were not elided: m-41=%t m-62=%t", byID["m-41"].Elided, byID["m-62"].Elided)
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
	if changed, _ := New(events.NewBus()).ElideOld(s, "r2", "test", used, 0, 16384, func(text string) (int, bool) { return len(text) / 3, true }); changed {
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

// Item 2ey: one pass frees the most by taking the largest stale result first.
// One 8k result among ten 200-token ones is elided first, and alone suffices.
func TestElideTakesTheLargestStaleResultFirst(t *testing.T) {
	messages := []events.Message{{ID: "m-1", Role: "user", Category: "history", Content: "task", Tokens: 5}}
	assistant := events.Message{ID: "m-2", Role: "assistant", Category: "history", Content: "reading", Tokens: 5}
	results := []events.Message{}
	sizes := []int{200, 200, 200, 8000, 200, 200, 200, 200, 200, 200, 200}
	for index, size := range sizes {
		id := fmt.Sprintf("call-%d", index)
		args, _ := json.Marshal(map[string]any{"path": fmt.Sprintf("f%d.txt", index)})
		assistant.ToolCalls = append(assistant.ToolCalls, events.ToolCall{ID: id, Name: "read_file", Arguments: string(args)})
		results = append(results, events.Message{ID: fmt.Sprintf("m-%d", 10+index), Role: "tool", Name: "read_file", Category: "files", ToolCallID: id, Content: strings.Repeat("x", size*3), Tokens: size, OK: ok()})
	}
	messages = append(append(messages, assistant), results...)
	messages = append(messages, events.Message{ID: "m-99", Role: "user", Category: "history", Content: "next", Tokens: 2})
	s := &session.Session{ID: "s"}
	s.ReplaceMessages(messages)
	used := tokenSum(messages)
	var trigger any
	bus := events.NewBus()
	events_, cancel := bus.Subscribe()
	defer cancel()
	changed, after := New(bus).ElideOld(s, "r", "soft_pct", used, used-7000, 16384, func(text string) (int, bool) { return len(text) / 3, true })
	if !changed || after > used-7000 {
		t.Fatalf("no room made: changed=%t %d → %d", changed, used, after)
	}
	elided := []string{}
	for _, message := range s.MessagesCopy() {
		if message.Elided {
			elided = append(elided, message.ID)
		}
	}
	if len(elided) != 1 || elided[0] != "m-13" {
		t.Fatalf("expected only the 8k result m-13 elided, got %v", elided)
	}
	for len(events_) > 0 {
		if event := <-events_; event.Type == events.Compaction {
			trigger = event.Data.(map[string]any)["trigger"]
		}
	}
	if trigger != "soft_pct" {
		t.Fatalf("compaction event trigger = %v", trigger)
	}
}
