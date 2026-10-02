package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/events"
	"harness/internal/llm"
)

// Item 2q1, CHECKS 1, 2, 4 and 6: one `go` run is one turn of forty reads on a
// 24,576-token ceiling. It compacts rarely, each time to 45% or under, keeps its
// task, its newest four results and one note naming the files, and the note
// carries no file body.
func TestALongSingleTurnRunCompactsRarelyAndDeep2q1(t *testing.T) {
	calls := 0
	server := newTemplateServer(t, func(body map[string]any, messages []map[string]any) map[string]any {
		if isSummaryRequest(messages) {
			return map[string]any{"content": "INTENT: read the forty files\nFILES: each file read so far\nNEXT STEP: read the next file"}
		}
		if calls < 40 {
			calls++
			// A model narrates as it goes; its own words are not elided, so
			// eliding alone cannot reach the target and the run's steps fold.
			reply := toolCall(fmt.Sprintf("call-%02d", calls), "read_file", fmt.Sprintf(`{"path":"f%02d.txt"}`, calls))
			reply["content"] = strings.Repeat(fmt.Sprintf("Reading f%02d.txt next. ", calls), 40)
			return reply
		}
		return map[string]any{"content": "all forty files read"}
	})
	runner, item, bus := templateRunnerReserve(t, server, 8192)
	for n := 1; n <= 40; n++ {
		body := fmt.Sprintf("PLANTED-BODY-%02d\n", n) + strings.Repeat(fmt.Sprintf("line of file %02d in plain words\n", n), 90)
		if err := os.WriteFile(filepath.Join(item.Workspace, fmt.Sprintf("f%02d.txt", n)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runner.AddUser(context.Background(), item, "Read f01.txt through f40.txt one by one."); err != nil {
		t.Fatal(err)
	}
	reason, detail, _ := runner.Run(context.Background(), item, "r1")
	ceiling, compactions, pending, worst := 32768-8192, 0, false, 0
	for _, event := range bus.Recent(item.ID) {
		switch event.Type {
		case events.Compaction:
			compactions, pending = compactions+1, true
		case events.BudgetEvent:
			if budget, ok := event.Data.(events.Budget); ok && pending {
				pending, worst = false, max(worst, budget.UsedEst)
			}
		}
	}
	t.Logf("2q1 forty reads: reason=%s compactions=%d worst after=%d of %d (%.0f%%)", reason, compactions, worst, ceiling, 100*float64(worst)/float64(ceiling))
	if reason != "done" || compactions == 0 || worst*100 > ceiling*45 {
		t.Fatalf("each compaction must end at 45%% or under: reason=%s %q compactions=%d worst=%d", reason, detail, compactions, worst)
	}
	notes := 0
	for _, message := range item.MessagesCopy() {
		if message.Category != "summary" {
			continue
		}
		notes++
		if strings.Contains(message.Content, "PLANTED-BODY") || !strings.Contains(message.Content, ".txt") {
			t.Fatalf("a note names files and carries no body:\n%s", message.Content)
		}
	}
	last := server.lastRequest()
	for _, want := range []string{"Read f01.txt through f40.txt", "PLANTED-BODY-37", "PLANTED-BODY-38", "PLANTED-BODY-39", "PLANTED-BODY-40", compactionNoteHeaderPrefix} {
		if !strings.Contains(last, want) {
			t.Errorf("the request after compaction lost %q", want)
		}
	}
	if notes != 1 {
		t.Fatalf("a run that compacts more than once keeps one note, has %d", notes)
	}
}

// CHECK 3: a summarizer that refuses its connection is tried once in a run, then
// skipped straight to the next.
func TestARefusingSummarizerIsAskedOncePerRun2q1(t *testing.T) {
	main := newSummaryServer(t, "short summary")
	dead := newSummaryServer(t, "unused")
	dead.server.Close()
	runner, item, bus, _ := compactionRunner(t, main, dead, 32768)
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
	if roles["c"] != 1 || roles["b"] != 3 || time.Since(started) > 5*time.Second {
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
