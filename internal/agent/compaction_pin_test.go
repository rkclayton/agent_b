package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
)

// Item 2u8: a turn boundary may take another user message into the run, but it
// never changes which message began the task the run is answering.
func TestRunPinStaysOnTheFirstUserMessageAcrossTenTurns2u8(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(fmt.Sprintf("queued_%t", queued), func(t *testing.T) {
			turn := 0
			pins, roles := []string{}, []string{}
			var itemPin func() string
			var itemMessages func() []events.Message
			server := newTemplateServer(t, func(body map[string]any, _ []map[string]any) map[string]any {
				if stream, _ := body["stream"].(bool); !stream {
					return map[string]any{"content": "summary"}
				}
				messages := itemMessages()
				pin := itemPin()
				pins = append(pins, pin)
				role := "missing"
				for _, message := range messages {
					if message.ID == pin {
						role = message.Role
					}
				}
				roles = append(roles, role)
				turn++
				if turn < 10 {
					return toolCall(fmt.Sprintf("read-%d", turn), "read_file", `{"path":"part.txt"}`)
				}
				return map[string]any{"content": "done"}
			})
			runner, item, _ := templateRunnerReserve(t, server, 1024)
			itemPin = item.RunPin
			itemMessages = item.MessagesCopy
			if err := os.WriteFile(filepath.Join(item.Workspace, "part.txt"), []byte("part"), 0o600); err != nil {
				t.Fatal(err)
			}
			message, err := runner.AddUser(context.Background(), item, "first task")
			if err != nil {
				t.Fatal(err)
			}
			boundary := 0
			runner.SetQueuedBoundary(func(s *session.Session, _ string) {
				boundary++
				if queued && boundary == 4 {
					s.Append(events.Message{ID: "queued", Role: "user", Category: "history", Content: "queued detail"})
				}
			})
			if reason, detail, _ := runner.Run(context.Background(), item, "run"); reason != "done" {
				t.Fatalf("run=%s %q", reason, detail)
			}
			if len(pins) != 10 {
				t.Fatalf("observed %d pins: %v", len(pins), pins)
			}
			for index := range pins {
				if pins[index] != message.ID || roles[index] != "user" {
					t.Fatalf("turn %d pin=%q role=%q, want %q user", index+1, pins[index], roles[index], message.ID)
				}
			}
		})
	}
}

func TestFreshStartKeepsAQueuedUserMessageWhole2u8(t *testing.T) {
	mainServer := newSummaryServer(t, "short summary")
	runner, item, _, _ := compactionRunner(t, mainServer, nil, 32768)
	item.Append(events.Message{ID: "task", Role: "user", Category: "history", Content: "first task"})
	item.Append(events.Message{ID: "work", Role: "assistant", Category: "history", Content: "working"})
	item.Append(events.Message{ID: "queued", Role: "user", Category: "history", Content: "queued detail"})
	item.SetRunPin("task")
	if !runner.summarize(context.Background(), item, "run", connectionForRunner(runner, "main")) {
		t.Fatal("summary was not accepted")
	}
	messages := item.MessagesCopy()
	if len(messages) != 3 || messages[1].ID != "task" || messages[1].Content != "first task" || messages[2].ID != "queued" || messages[2].Content != "queued detail" {
		t.Fatalf("fresh context=%+v", messages)
	}
}

func TestSummaryIsNotRequestedWhenFreshStartCannotLand2u8(t *testing.T) {
	mainServer := newSummaryServer(t, "unused")
	runner, item, _, _ := compactionRunner(t, mainServer, nil, 32768)
	item.Append(events.Message{ID: "tool-pin", Role: "tool", Category: "results", Content: "result"})
	item.SetRunPin("tool-pin")
	if runner.summarize(context.Background(), item, "run", connectionForRunner(runner, "main")) {
		t.Fatal("summary unexpectedly landed")
	}
	if calls := mainServer.chatCalls.Load(); calls != 0 {
		t.Fatalf("summary requests=%d, want 0", calls)
	}
}

func TestEightTurnRecordedReplayCompactsAndShrinksTheNextRequest2u8(t *testing.T) {
	turn, summaries := 0, 0
	requestBytes := []int{}
	server := newTemplateServer(t, func(body map[string]any, messages []map[string]any) map[string]any {
		if stream, _ := body["stream"].(bool); !stream {
			summaries++
			return map[string]any{"content": "DONE: read the parts\nNEXT: answer\nFILES CHANGED: none\nOPEN QUESTIONS: none"}
		}
		encoded, _ := json.Marshal(messages)
		requestBytes = append(requestBytes, len(encoded))
		turn++
		if turn <= 8 {
			reply := toolCall(fmt.Sprintf("read-%d", turn), "read_file", `{"path":"part.txt"}`)
			reply["content"] = strings.Repeat("work", 3000)
			return reply
		}
		return map[string]any{"content": "done"}
	})
	runner, item, bus := templateRunnerReserve(t, server, 8192)
	if err := os.WriteFile(filepath.Join(item.Workspace, "part.txt"), []byte("part"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.AddUser(context.Background(), item, "read all eight parts and answer"); err != nil {
		t.Fatal(err)
	}
	if reason, detail, _ := runner.Run(context.Background(), item, "run"); reason != "done" {
		t.Fatalf("run=%s %q", reason, detail)
	}
	attempts := summaryAttempts(bus, item.ID)
	if summaries != 1 || len(attempts) != 1 || attempts[0].Outcome != "accepted" || len(requestBytes) < 9 {
		t.Fatalf("summaries=%d attempts=%+v request_bytes=%v", summaries, attempts, requestBytes)
	}
	before, after := requestBytes[len(requestBytes)-2], requestBytes[len(requestBytes)-1]
	t.Logf("2u8 replay: summary_requests=%d outcome=%s request_bytes=%d->%d", summaries, attempts[0].Outcome, before, after)
	if after >= before {
		t.Fatalf("next request did not shrink: %d -> %d", before, after)
	}
}

// Item 2qh: the hand-off has four fixed fields while the task stays outside it.
func TestSummaryPromptBuildsFreshHandOffAndKeepsTaskVerbatim(t *testing.T) {
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

	for _, heading := range []string{"DONE:", "NEXT:", "FILES CHANGED:", "OPEN QUESTIONS:"} {
		if !strings.Contains(instruction, heading) {
			t.Errorf("structured prompt missing heading %q", heading)
		}
	}
	if !strings.Contains(instruction, "current task's user messages will be preserved verbatim outside this hand-off") || !strings.Contains(instruction, "Do not mention or summarize any prior hand-off") {
		t.Fatalf("fresh-context constraints missing:\n%s", instruction)
	}

	// The task itself is retained next to the one hand-off, not copied into it.
	if !runner.summarize(context.Background(), item, "run", connectionForRunner(runner, "main")) {
		t.Fatal("summary was not accepted")
	}
	after := item.Snapshot().Messages
	if len(after) != 2 || after[0].Category != "summary" || after[1].ID != "task" || after[1].Content != "the real task" {
		t.Fatalf("fresh context=%+v", after)
	}
	if strings.Contains(after[0].Content, "the real task") {
		t.Fatal("hand-off copied the pinned task")
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
