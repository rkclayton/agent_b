package contextmgr

import (
	"encoding/json"
	"os"
	"testing"

	"harness/internal/events"
	"harness/internal/session"
)

type tapeMessage struct {
	ID         string   `json:"id"`
	Role       string   `json:"role"`
	Category   string   `json:"category"`
	Turn       int      `json:"turn"`
	ToolCallID string   `json:"tool_call_id"`
	Calls      []string `json:"calls"`
	Tokens     int      `json:"tokens"`
	Elided     bool     `json:"elided"`
}

func loadS7(t *testing.T) []events.Message {
	t.Helper()
	raw, err := os.ReadFile("testdata/s7-r10-messages.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var tape []tapeMessage
	if err := json.Unmarshal(raw, &tape); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	messages := make([]events.Message, 0, len(tape))
	for _, item := range tape {
		message := events.Message{ID: item.ID, Role: item.Role, Category: item.Category, Turn: item.Turn, ToolCallID: item.ToolCallID, Tokens: item.Tokens, Elided: item.Elided}
		for _, call := range item.Calls {
			message.ToolCalls = append(message.ToolCalls, events.ToolCall{ID: call})
		}
		messages = append(messages, message)
	}
	return messages
}

// The s7 tape's r10 is the reported defect: compaction event seq 3822 listed
// affected_ids m-14 … m-49, and m-49 is the message the run was answering.
func TestS7R10ReproducesTheDefectWithoutThePin(t *testing.T) {
	messages := loadS7(t)
	if len(messages) != 43 {
		t.Fatalf("fixture drifted: %d messages", len(messages))
	}
	if messages[0].ID != "m-13" {
		t.Fatalf("first message is %s, expected the immortal m-13", messages[0].ID)
	}
	foldEnd, ok := SummarizeSpan(messages, "")
	if !ok {
		t.Fatal("unpinned span should exist")
	}
	folded := map[string]bool{}
	for index := 1; index < foldEnd; index++ {
		folded[messages[index].ID] = true
	}
	if !folded["m-49"] {
		t.Fatal("without the pin m-49 must still be folded; the fixture no longer reproduces the defect")
	}
	if folded["m-13"] {
		t.Fatal("m-13 is index 0 and is never folded")
	}
}

// With the run pinned at m-49 the span stops before it, which is the whole item.
func TestS7R10SurvivesWithThePin(t *testing.T) {
	messages := loadS7(t)
	foldEnd, ok := SummarizeSpan(messages, "m-49")
	if !ok {
		t.Fatal("a span still exists before the pin")
	}
	var last string
	for index := 1; index < foldEnd; index++ {
		if messages[index].ID == "m-49" {
			t.Fatal("m-49 was folded despite the pin")
		}
		last = messages[index].ID
	}
	if last != "m-48" {
		t.Fatalf("span ends at %s, expected m-48", last)
	}
	for index := foldEnd; index < len(messages); index++ {
		if messages[index].ID == "m-49" {
			return
		}
	}
	t.Fatal("m-49 is not in the kept tail")
}

// Everything from the pin onward is the task, not only the pinned message.
func TestPinKeepsEverythingAfterTheTask(t *testing.T) {
	messages := loadS7(t)
	foldEnd, _ := SummarizeSpan(messages, "m-49")
	for index := 0; index < len(messages); index++ {
		if messages[index].ID != "m-49" {
			continue
		}
		if foldEnd > index {
			t.Fatalf("fold end %d reaches the pin at %d", foldEnd, index)
		}
		return
	}
	t.Fatal("pin not present")
}

func TestPinMissingRefusesToTouchAnything(t *testing.T) {
	messages := loadS7(t)
	if _, ok := SummarizeSpan(messages, "m-does-not-exist"); ok {
		t.Fatal("a pin that is not present must protect everything, not nothing")
	}
}

func TestNoRunLeavesHistoryTouchable(t *testing.T) {
	messages := loadS7(t)
	if _, ok := SummarizeSpan(messages, ""); !ok {
		t.Fatal("outside a run the whole history stays compactible")
	}
}

// A summarize span may not fold a tool call while keeping its result.
func TestSummarizeSpanKeepsCallAndResultTogether(t *testing.T) {
	ok := true
	messages := []events.Message{
		{ID: "m-0", Role: "user", Category: "history"},
	}
	for index := 1; index <= 8; index++ {
		messages = append(messages, events.Message{ID: "filler-" + string(rune('a'+index)), Role: "assistant", Category: "history"})
	}
	messages = append(messages,
		events.Message{ID: "call", Role: "assistant", Category: "history", ToolCalls: []events.ToolCall{{ID: "c-1"}}},
		events.Message{ID: "result", Role: "tool", Category: "files", ToolCallID: "c-1", OK: &ok},
	)
	for index := 0; index < 4; index++ {
		messages = append(messages, events.Message{ID: "tail-" + string(rune('a'+index)), Role: "assistant", Category: "history"})
	}
	foldEnd, found := SummarizeSpan(messages, "")
	if !found {
		t.Fatal("span expected")
	}
	callIndex, resultIndex := -1, -1
	for index, message := range messages {
		if message.ID == "call" {
			callIndex = index
		}
		if message.ID == "result" {
			resultIndex = index
		}
	}
	if (callIndex < foldEnd) != (resultIndex < foldEnd) {
		t.Fatalf("call at %d and result at %d straddle the fold end %d", callIndex, resultIndex, foldEnd)
	}
}

// Elide replaces a result's body in place, so a call can never lose its result.
// Proving it structurally is cheap and stops a later refactor from removing it.
func TestElideKeepsEveryCallWithItsResultAndRespectsThePin(t *testing.T) {
	ok := true
	messages := []events.Message{{ID: "m-0", Role: "user", Category: "history", Tokens: 10}}
	for index := 0; index < 6; index++ {
		call := events.ToolCall{ID: "c-" + string(rune('a'+index)), Name: "read_file", Arguments: `{"path":"x.go"}`}
		messages = append(messages,
			events.Message{ID: "call-" + string(rune('a'+index)), Role: "assistant", Category: "history", Tokens: 10, ToolCalls: []events.ToolCall{call}},
			events.Message{ID: "res-" + string(rune('a'+index)), Role: "tool", Category: "files", Name: "read_file", ToolCallID: call.ID, Content: "body", Tokens: 500, OK: &ok},
		)
	}
	s := &session.Session{ID: "main", Messages: messages}
	s.SetRunPin("call-e")
	compactor := New(events.NewBus())
	changed, _ := compactor.ElideOld(s, "r1", "test", 4000, 100, 200, func(text string) (int, bool) { return len(text), true })
	if !changed {
		t.Fatal("expected elision before the pin")
	}
	after := s.MessagesCopy()
	if len(after) != len(messages) {
		t.Fatalf("elide removed messages: %d -> %d", len(messages), len(after))
	}
	byID := map[string]events.Message{}
	for _, message := range after {
		byID[message.ID] = message
	}
	for index := 0; index < 6; index++ {
		suffix := string(rune('a' + index))
		call, hasCall := byID["call-"+suffix]
		result, hasResult := byID["res-"+suffix]
		if !hasCall || !hasResult {
			t.Fatalf("pair %s lost a side", suffix)
		}
		if len(call.ToolCalls) != 1 || call.ToolCalls[0].ID != result.ToolCallID {
			t.Fatalf("pair %s no longer matches", suffix)
		}
	}
	// call-e is the pin; it and everything after it must be untouched.
	for _, id := range []string{"res-e", "call-f", "res-f"} {
		if byID[id].Elided {
			t.Fatalf("%s is at or after the pin and must not be elided", id)
		}
	}
	if !byID["res-a"].Elided {
		t.Fatal("history before the pin should still compact")
	}
}
