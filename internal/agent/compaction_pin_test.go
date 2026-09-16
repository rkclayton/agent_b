package agent

import (
	"strings"
	"testing"

	"harness/internal/events"
)

// The structured prompt is the second half of 2eg: the note has to carry the
// task forward, not just avoid eating it.
func TestSummaryPromptIsStructuredAndCarriesSpanUserMessagesVerbatim(t *testing.T) {
	mainServer := newSummaryServer(t, "short summary")
	runner, item, _, _ := compactionRunner(t, mainServer, nil, 32768)
	ok := true
	for turn := 1; turn <= 6; turn++ {
		item.Append(events.Message{ID: idFor("user", turn), Role: "user", Category: "history", Turn: turn, Content: userTextFor(turn)})
		item.Append(events.Message{ID: idFor("call", turn), Role: "assistant", Category: "history", Turn: turn, ToolCalls: []events.ToolCall{{ID: idFor("c", turn), Name: "read_file", Arguments: `{"path":"a.go"}`}}})
		item.Append(events.Message{ID: idFor("res", turn), Role: "tool", Category: "files", Turn: turn, ToolCallID: idFor("c", turn), Name: "read_file", OK: &ok, Content: "body"})
	}
	item.Append(events.Message{ID: "task", Role: "user", Category: "history", Turn: 7, Content: "the real task"})
	item.SetRunPin("task")

	messages := runner.summaryMessages(profileForRunner(runner, "main"), item)
	instruction, _ := messages[len(messages)-1].Content.(string)

	for _, heading := range []string{"INTENT:", "USER MESSAGES:", "FILES:", "ERRORS AND FIXES:", "PENDING:", "NEXT STEP:"} {
		if !strings.Contains(instruction, heading) {
			t.Errorf("structured prompt missing heading %q", heading)
		}
	}
	if !strings.Contains(instruction, "consolidate it into these headings rather than writing a second note") {
		t.Error("prompt does not ask for consolidation")
	}
	if !strings.Contains(instruction, "USER MESSAGES in the span, verbatim, to copy into the note:") {
		t.Fatalf("prompt carries no verbatim block:\n%s", instruction)
	}
	// Every user message inside the span, and never the pinned task itself.
	for turn := 1; turn <= 4; turn++ {
		if !strings.Contains(instruction, userTextFor(turn)) {
			t.Errorf("span user message for turn %d is not verbatim in the prompt", turn)
		}
	}
	if strings.Contains(instruction, "USER MESSAGES in the span, verbatim, to copy into the note:") &&
		strings.Contains(instruction[strings.Index(instruction, "USER MESSAGES in the span"):], "the real task") {
		t.Error("the pinned task must not be listed as span content")
	}
}

// The note says which turns it covers, so the model can see the task is not in it.
func TestCompactionNoteHeaderNamesTheTurnsItCovers(t *testing.T) {
	mainServer := newSummaryServer(t, "short summary")
	_, item, _, _ := compactionRunner(t, mainServer, nil, 32768)
	ok := true
	for turn := 3; turn <= 9; turn++ {
		item.Append(events.Message{ID: idFor("user", turn), Role: "user", Category: "history", Turn: turn, Content: userTextFor(turn)})
		item.Append(events.Message{ID: idFor("call", turn), Role: "assistant", Category: "history", Turn: turn, ToolCalls: []events.ToolCall{{ID: idFor("c", turn), Name: "read_file", Arguments: `{"path":"a.go"}`}}})
		item.Append(events.Message{ID: idFor("res", turn), Role: "tool", Category: "files", Turn: turn, ToolCallID: idFor("c", turn), Name: "read_file", OK: &ok, Content: "body"})
	}
	item.Append(events.Message{ID: "task", Role: "user", Category: "history", Turn: 10, Content: "the real task"})
	item.SetRunPin("task")

	header := compactionNoteHeader(item)
	if !strings.HasPrefix(header, compactionNoteHeaderPrefix) {
		t.Fatalf("header lost its stable prefix: %q", header)
	}
	if !strings.Contains(header, "turns ") || !strings.HasSuffix(header, "):\n") {
		t.Fatalf("header does not name a turn range: %q", header)
	}
	if strings.Contains(header, "10") {
		t.Errorf("header claims to cover the running turn: %q", header)
	}
}

func idFor(prefix string, turn int) string {
	return prefix + "-" + string(rune('0'+turn))
}

func userTextFor(turn int) string {
	return "user question number " + string(rune('0'+turn))
}
