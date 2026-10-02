package agent

import (
	"context"
	"strings"
	"testing"

	"harness/internal/config"
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

	messages := runner.summaryMessages(connectionForRunner(runner, "main"), item)
	instruction, _ := messages[len(messages)-1].Content.(string)

	for _, heading := range []string{"INTENT:", "FILES:", "ERRORS AND FIXES:", "PENDING:", "NEXT STEP:"} {
		if !strings.Contains(instruction, heading) {
			t.Errorf("structured prompt missing heading %q", heading)
		}
	}
	if !strings.Contains(instruction, "consolidate it into these headings rather than writing a second note") {
		t.Error("prompt does not ask for consolidation")
	}
	// (b): the prompt asks for a summary and for nothing it cannot deliver.
	if strings.Contains(instruction, "USER MESSAGES:") || strings.Contains(instruction, "to copy into the note") {
		t.Fatalf("the prompt still asks the model to reproduce the user messages:\n%s", instruction)
	}
	if !strings.Contains(instruction, "carried into the note verbatim by Agent_b itself") {
		t.Fatalf("the prompt does not say the messages are already carried:\n%s", instruction)
	}

	// (a): and the NOTE the harness writes holds them, whatever the model returned.
	if !runner.summarize(context.Background(), item, "run", connectionForRunner(runner, "main")) {
		t.Fatal("summary was not accepted")
	}
	note := ""
	for _, message := range item.Snapshot().Messages {
		if message.Category == "summary" {
			note = message.Content
		}
	}
	if !strings.Contains(note, "USER MESSAGES in the span, carried forward verbatim by Agent_b:") {
		t.Fatalf("the note carries no user messages:\n%s", note)
	}
	for turn := 1; turn <= 4; turn++ {
		if !strings.Contains(note, userTextFor(turn)) {
			t.Errorf("span user message for turn %d is not in the note", turn)
		}
	}
	// Never the pinned task itself: that is not span content, it is the thing being
	// answered, and it is still in the conversation. Item 2q5 (c) re-states it after
	// the summary, which is not the span list.
	span := note[strings.Index(note, "USER MESSAGES in the span"):strings.Index(note, "LAST USER MESSAGE:")]
	if strings.Contains(span, "the real task") {
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

// Item 2mm (c): WHEN THE CARRIED MESSAGES THEMSELVES DO NOT FIT, THAT IS ITS OWN
// CONDITION, WITH THE NUMBERS — not a summary that quietly violates its cap.
func TestCarriedUserMessagesStateTheirOwnLimit2mm(t *testing.T) {
	records := []events.Message{{ID: "m-0", Role: "user", Category: "history", Turn: 0, Content: "opening"}}
	for turn := 1; turn <= 6; turn++ {
		records = append(records,
			events.Message{ID: idFor("user", turn), Role: "user", Category: "history", Turn: turn, Content: strings.Repeat("word ", 200)},
			events.Message{ID: idFor("res", turn), Role: "tool", Category: "files", Turn: turn, Content: "body"})
	}
	records = append(records, events.Message{ID: "task", Role: "user", Category: "history", Turn: 7, Content: "the real task"})

	// A limit that fits two of the six.
	block, used, dropped, droppedBytes := carriedUserMessages(records, "task", 2200)
	if dropped == 0 || droppedBytes == 0 {
		t.Fatalf("nothing was reported as dropped: used=%d dropped=%d", used, dropped)
	}
	if !strings.Contains(block, "did not fit the 2200-byte carry limit") || !strings.Contains(block, "the chat's journal holds them in full") {
		t.Fatalf("the condition is not stated with its numbers:\n%s", block)
	}
	if used > 2200+len(strings.Repeat("word ", 200)) {
		t.Fatalf("the block overran its limit: %d bytes", used)
	}
	// And with room, nothing is dropped and nothing is said about a limit.
	whole, _, noneDropped, _ := carriedUserMessages(records, "task", 1<<20)
	if noneDropped != 0 || strings.Contains(whole, "carry limit") {
		t.Fatalf("a block that fits reported a limit: dropped=%d", noneDropped)
	}
	// What survives a bound is the MOST RECENT of the span, because that is the
	// instruction a run is still closest to; the older ones are the ones a reader
	// can go and find in the journal, which the line above says.
	turns := []string{}
	for _, part := range strings.Split(whole, "(turn ")[1:] {
		turns = append(turns, strings.SplitN(part, ")", 2)[0])
	}
	if len(turns) < 2 {
		t.Fatalf("the span carried %d message(s); this case needs several", len(turns))
	}
	if !strings.Contains(block, "(turn "+turns[len(turns)-1]+")") {
		t.Fatalf("the newest carried message was dropped instead of an older one:\n%s", block[:200])
	}
	if strings.Contains(block, "(turn "+turns[0]+")") {
		t.Fatalf("the oldest carried message survived a bound the newest did not:\n%s", block[:200])
	}
	// The carry limit follows the connection's own window.
	if small, large := carryLimitBytes(&config.Connection{}), carryLimitBytes(&config.Connection{Context: config.Context{NCtx: 131072}}); large <= small {
		t.Fatalf("a bigger window did not carry more: %d vs %d", large, small)
	}
}
