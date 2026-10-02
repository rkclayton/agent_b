package agent

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/events"
	"harness/internal/llm"
)

// s52Shape is s52's run as it happened on HomePC, content-free: each step is the
// tokens of the model's own text and the tokens of each tool result it asked for.
var s52Shape = [][]int{{0, 3993}, {0, 4869, 3366}, {58, 146, 111}, {46, 145}, {0, 37}, {50, 90}, {51, 19}, {47, 150}, {39, 187, 753, 635}, {35, 7}, {43, 1596, 2889, 76}, {57, 2191}, {59, 4447, 72, 71, 340}, {0, 212, 88, 237, 2277}, {0, 1871, 72}, {0, 73}, {0, 40}, {0, 2945}, {0, 2340}, {0, 14, 1244, 32, 32}, {0, 71, 400}}

// Item 2q5 CHECKS 1 and 3: s52's 38 tool calls replayed on a 24,576 ceiling mask at
// most once per eight calls, summarize never below 90%, and between compactions
// each request keeps at least 80% of the one before as its prefix.
func TestS52ReplayMasksRarelyAndKeepsTheCacheWarm2q5(t *testing.T) {
	step := 0
	server := newTemplateServer(t, func(body map[string]any, messages []map[string]any) map[string]any {
		if isSummaryRequest(messages) {
			return map[string]any{"content": "INTENT: inspect the installed copy\nNEXT STEP: continue"}
		}
		if step == len(s52Shape) {
			return map[string]any{"content": "done"}
		}
		calls := []any{}
		for index := range s52Shape[step][1:] {
			calls = append(calls, map[string]any{"index": index, "id": fmt.Sprintf("c%d-%d", step, index), "type": "function", "function": map[string]any{"name": "read_file", "arguments": fmt.Sprintf(`{"path":"r%d-%d.txt"}`, step, index)}})
		}
		reply := map[string]any{"tool_calls": calls, "content": strings.Repeat("w", s52Shape[step][0]*4)}
		step++
		return reply
	})
	// CHECK 4: estimated accounting against a server that reports what it counted.
	server.countPrompt, templateAccounting = true, "estimated"
	defer func() { templateAccounting = "exact" }()
	runner, item, bus := templateRunnerReserve(t, server, 8192)
	calls := 0
	for step, sizes := range s52Shape {
		for index, tokens := range sizes[1:] {
			calls++
			if err := os.WriteFile(filepath.Join(item.Workspace, fmt.Sprintf("r%d-%d.txt", step, index)), []byte(strings.Repeat("abcd", tokens)), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := runner.AddUser(context.Background(), item, "Inspect the installed copy and report."); err != nil {
		t.Fatal(err)
	}
	reason, detail, _ := runner.Run(context.Background(), item, "r52")
	masks, summaries, peak, sent, drift := 0, 0, 0, 0, 0.0
	for _, event := range bus.Recent(item.ID) {
		if data, ok := event.Data.(map[string]any); ok && event.Type == events.ModelRequest {
			sent = asInt(data["est_prompt_tokens"])
		}
		if data, ok := event.Data.(map[string]any); ok && event.Type == events.ModelResponse && sent > 0 {
			if reported := asInt(data["usage"].(map[string]any)["prompt_tokens"]); reported > 0 && peak > 0 {
				drift = max(drift, math.Abs(float64(sent-reported))/float64(reported))
			}
		}
		if event.Type == events.Compaction {
			if event.Data.(map[string]any)["kind"] == "summarize" {
				summaries++
			} else {
				masks++
			}
		}
		if budget, ok := event.Data.(events.Budget); ok && event.Type == events.BudgetEvent {
			peak = max(peak, budget.UsedEst)
		}
	}
	server.mu.Lock()
	requests := append([]string(nil), server.requests...)
	server.mu.Unlock()
	worst := 1.0
	for index := 1; index < len(requests); index++ {
		previous, next := requests[index-1], requests[index]
		shared := 0
		for shared < len(previous) && shared < len(next) && previous[shared] == next[shared] {
			shared++
		}
		if ratio := float64(shared) / float64(len(previous)); ratio > .5 {
			worst = min(worst, ratio)
		}
	}
	t.Logf("2q5 s52 replay: %d calls, %d masking passes, %d summaries, peak %d of 24576, worst kept prefix between compactions %.1f%%, worst count gap %.1f%%, reason=%s %q", calls, masks, summaries, peak, 100*worst, 100*drift, reason, detail)
	if reason != "done" || calls != 38 || masks*8 > calls || (summaries > 0 && peak < 24576*90/100) || worst < .80 {
		t.Fatalf("2q5 CHECKS 1 and 3 failed")
	}
}

// 2q1 CHECK 3, under 2q5 (a): the summarizer is the chat's own connection; one that
// refuses is tried once in a run and not again.
func TestARefusingSummarizerIsAskedOncePerRun2q1(t *testing.T) {
	dead := newSummaryServer(t, "unused")
	dead.server.Close()
	runner, item, bus, _ := compactionRunner(t, dead, nil, 32768)
	started := time.Now()
	for round := 0; round < 3; round++ {
		runner.summarize(context.Background(), item, "run", connectionForRunner(runner, "main"))
		for index := 0; index < 8; index++ {
			item.Append(events.Message{ID: fmt.Sprintf("n%d-%d", round, index), Role: "user", Content: strings.Repeat("more ", 20), Category: "history", Tokens: 100})
		}
	}
	roles := map[string]int{}
	for _, attempt := range summaryAttempts(bus, item.ID) {
		roles[attempt.Role]++
	}
	if roles["c"] != 0 || roles["b"] != 1 || time.Since(started) > 5*time.Second {
		t.Fatalf("the refusing summarizer must be asked once: attempts=%v in %s", roles, time.Since(started))
	}
}

// CHECK 5: s51's pattern. A turn-0 note, then a running turn whose four steps
// fill the window with the model's own words: each summary re-summarized the note
// and freed a little, and the run looped overflow, summarize, overflow. Now it
// ends, with the reason.
func TestAWeakCompactionEndsTheRunInsteadOfLooping2q1(t *testing.T) {
	summaries, steps := 0, 0
	server := newTemplateServer(t, func(body map[string]any, messages []map[string]any) map[string]any {
		if isSummaryRequest(messages) {
			summaries++
			return map[string]any{"content": strings.Repeat("progress ", max(10, 300-5*summaries))}
		}
		if steps < 4 {
			steps++
			reply := toolCall(fmt.Sprintf("step-%d", steps), "read_file", `{"path":"a.txt"}`)
			reply["content"] = strings.Repeat("thinking aloud ", 2200)
			return reply
		}
		return map[string]any{"content": "answer"}
	})
	runner, item, bus := templateRunnerReserve(t, server, 8192)
	item.ReplaceMessages([]events.Message{
		{ID: "m-1", Role: "user", Category: "history", Content: "start", Tokens: 2},
		{ID: "m-2", Role: llm.RoleHarness, Category: "summary", Content: strings.Repeat("note ", 2000), Tokens: 2500},
	})
	runner.ReserveIDs(3)
	if _, err := runner.AddUser(context.Background(), item, "go"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(item.Workspace, "a.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	reason, detail, _ := runner.Run(ctx, item, "r-s51")
	t.Logf("s51 pattern: reason=%s detail=%q summaries=%d attempts=%d", reason, detail, summaries, len(summaryAttempts(bus, item.ID)))
	if summaries > 2 || reason != "context_exhausted" || !strings.Contains(detail, "context cannot be reduced further") {
		t.Fatalf("at most two attempts, then the stated reason: reason=%s %q summaries=%d", reason, detail, summaries)
	}
}

func asInt(value any) int {
	switch number := value.(type) {
	case int:
		return number
	case float64:
		return int(number)
	}
	return 0
}

// Item 2q5 CHECK 5: after a summary the note carries his last message, the order id
// and the files changed, and no verbatim tool output.
func TestASummaryRestatesTheTaskWithoutToolOutput2q5(t *testing.T) {
	server := newSummaryServer(t, "INTENT: carry on")
	runner, item, _, _ := compactionRunner(t, server, nil, 32768)
	ok := true
	item.Append(events.Message{ID: "w1", Role: "assistant", ToolCalls: []events.ToolCall{{ID: "write", Name: "write_file", Arguments: `{"path":"notes/a.txt","content":"x"}`}}})
	item.Append(events.Message{ID: "w2", Role: "tool", Category: "results", Name: "write_file", ToolCallID: "write", OK: &ok, Content: "PLANTED-TOOL-OUTPUT wrote the file", Tokens: 10})
	item.Append(events.Message{ID: "u1", Role: "user", Category: "history", Content: "go: Order ID: `rel-9.9.9`, finish it", Tokens: 10})
	if !runner.summarize(context.Background(), item, "run", connectionForRunner(runner, "main")) {
		t.Fatal("the summary was not accepted")
	}
	for _, message := range item.MessagesCopy() {
		if message.Category != "summary" {
			continue
		}
		for _, want := range []string{"LAST USER MESSAGE: go: Order ID: `rel-9.9.9`, finish it", "ORDER: rel-9.9.9", "FILES CHANGED: notes/a.txt"} {
			if !strings.Contains(message.Content, want) {
				t.Errorf("the note lacks %q", want)
			}
		}
		if strings.Contains(message.Content, "PLANTED-TOOL-OUTPUT") {
			t.Error("the note carries verbatim tool output")
		}
		return
	}
	t.Fatal("no note")
}
