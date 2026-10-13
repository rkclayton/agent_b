package agent

import (
	"context"
	"fmt"
	"harness/internal/events"
	"harness/internal/session"
	"strings"
	"testing"
)

func TestEveryRunStartPinsItsUserMessage2uo(t *testing.T) {
	starts := []string{"message", "after failure", "Continue", "Revise plan", "queued", "phone", "worker", "helper", "scheduled"}
	for _, start := range starts {
		messages := []events.Message{{ID: "old", Role: "assistant"}, {ID: "task", Role: "user"}}
		if start == "queued" {
			messages = append(messages, events.Message{ID: "queued", Role: "user"})
		}
		if start == "after failure" || start == "queued" {
			messages = append(messages, events.Message{ID: "status", Role: "harness"})
		}
		if pin := session.RunPinFromTail(messages); pin != "task" {
			t.Errorf("%s pin=%q", start, pin)
		}
	}
	if pin := session.RunPinFromTail([]events.Message{{ID: "brief", Role: "harness"}}); pin != "brief" {
		t.Errorf("no-user pin=%q", pin)
	}
}

func TestFailedRunShapeCompactsBeforeItsFirstRequest2uo(t *testing.T) {
	summaries := 0
	server := newTemplateServer(t, func(body map[string]any, _ []map[string]any) map[string]any {
		if stream, _ := body["stream"].(bool); !stream {
			summaries++
			return map[string]any{"content": "DONE: earlier work\nNEXT: answer\nFILES CHANGED: none\nOPEN QUESTIONS: none"}
		}
		return map[string]any{"content": "carried on"}
	})
	runner, item, bus := templateRunnerReserve(t, server, 8192)
	for turn := 1; turn <= 10; turn++ {
		item.Append(events.Message{ID: fmt.Sprintf("u-%d", turn), Role: "user", Category: "history", Turn: turn, Content: "earlier request"})
		item.Append(events.Message{ID: fmt.Sprintf("a-%d", turn), Role: "assistant", Category: "history", Turn: turn, Content: strings.Repeat("earlier work ", 900)})
	}
	item.Append(events.Message{ID: "task", Role: "user", Category: "history", Turn: 11, Content: "carry on"})
	item.Append(events.Message{ID: "status", Role: "harness", Category: "history", Turn: 11, Content: "The prior run ended with model error: refused."})
	if reason, detail, _ := runner.Run(context.Background(), item, "run"); reason != "done" {
		t.Fatalf("run=%s %q", reason, detail)
	}
	attempts, budgets := summaryAttempts(bus, item.ID), []events.Budget{}
	for _, event := range bus.Recent(item.ID) {
		if event.Type == events.BudgetEvent {
			budgets = append(budgets, event.Data.(events.Budget))
		}
	}
	if summaries != 1 || len(attempts) != 1 || attempts[0].Outcome != "accepted" {
		t.Fatalf("summaries=%d attempts=%+v", summaries, attempts)
	}
	if len(budgets) < 2 || budgets[len(budgets)-1].UsedEst > budgets[len(budgets)-1].Ceiling/2 {
		t.Fatalf("post-compaction budget=%+v", budgets)
	}
}

func TestGuardAndCompactionNotice2uo(t *testing.T) {
	if got := guardedPromptTokens(events.Budget{UsedEst: 9000, UsedMeasured: 8800, Mode: "estimated"}); got != 9000 {
		t.Fatalf("anchored=%d", got)
	}
	if got := guardedPromptTokens(events.Budget{UsedEst: 9000, Mode: "estimated"}); got != 9900 {
		t.Fatalf("unanchored=%d", got)
	}
	bus, item := newCapturedBus(), &session.Session{ID: "chat", Messages: []events.Message{{ID: "task", Role: "user"}}}
	item.SetRunPin("task")
	x := &runState{r: &Runner{bus: bus.Bus}, s: item, runID: "run"}
	x.reportCompactionUnavailable()
	x.reportCompactionUnavailable()
	if messages := item.MessagesCopy(); len(messages) != 2 || !strings.Contains(messages[1].Content, "no earlier context") {
		t.Fatalf("messages=%+v", messages)
	}
	if x.afterCompaction() != "" {
		t.Fatal("an uncompacted stop said after compaction")
	}
	x.compacted = true
	if x.afterCompaction() != " after compaction" {
		t.Fatal("a compacted stop omitted its state")
	}
}
