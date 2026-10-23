package contextmgr

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"harness/internal/events"
	"harness/internal/session"
)

func TestOneRuleCompactsEveryRunShape2up(t *testing.T) {
	for _, shape := range []string{"fresh", "after failed run", "Continue", "queued", "helper", "worker", "restored", "tool-only"} {
		t.Run(shape, func(t *testing.T) {
			s := oneRuleChat(shape)
			asked := []events.Message{}
			result := New(nil).Compact(context.Background(), s, "run", OneRuleInput{Used: 950, Window: 1000, Fixed: 100, SummaryPct: .90, Pin: "task", Summarize: func(messages []events.Message, maxTokens int) (string, error) {
				asked = append(asked, messages...)
				if maxTokens != 500 {
					t.Fatalf("summary max=%d", maxTokens)
				}
				return "DONE: old\nNEXT: task\nFILES CHANGED: none\nOPEN QUESTIONS: none", nil
			}})
			if !result.Triggered || !result.Changed || result.Stop != "" {
				t.Fatalf("result=%+v", result)
			}
			if len(asked) == 0 || asked[len(asked)-1].Content != HandOffInstruction {
				t.Fatalf("summary request=%+v", asked)
			}
			if result.AfterChangeable*2 >= result.BeforeChangeable {
				t.Fatalf("not under half: %+v", result)
			}
			if result.HandOffRequestTokens > result.CrossingTokens {
				t.Fatalf("hand-off request=%d crossing request=%d", result.HandOffRequestTokens, result.CrossingTokens)
			}
			t.Logf("request sizes: hand-off=%d crossing=%d", result.HandOffRequestTokens, result.CrossingTokens)
			messages := s.MessagesCopy()
			if !hasExact(messages, "task", "CURRENT TASK WORD FOR WORD") || messages[0].Role != "harness" {
				t.Fatalf("new context=%+v", messages)
			}
		})
	}
}

func TestOneRuleFallbackStillCompacts2up(t *testing.T) {
	for _, row := range []struct {
		name   string
		answer string
		err    error
	}{
		{"error", "", errors.New("model unavailable")}, {"refusal", "", errors.New("HTTP 400 refused")}, {"empty", "  ", nil},
	} {
		t.Run(row.name, func(t *testing.T) {
			s := oneRuleChat(row.name)
			result := New(nil).Compact(context.Background(), s, "run", OneRuleInput{Used: 950, Window: 1000, Fixed: 100, SummaryPct: .9, Pin: "task", Summarize: func([]events.Message, int) (string, error) { return row.answer, row.err }})
			if !result.Changed || result.Stop != "" || !strings.Contains(s.MessagesCopy()[0].Content, "record can be searched with recall.") || !strings.Contains(s.MessagesCopy()[0].Content, row.name) {
				t.Fatalf("result=%+v messages=%+v", result, s.MessagesCopy())
			}
		})
	}
}

func TestOneRuleNamesTheOneThingTooBig2up(t *testing.T) {
	for _, row := range []struct {
		name                string
		fixed, task, result int
		want                string
	}{
		{"fixed prompt", 1000, 1, 1, "fixed prompt"}, {"task message", 100, 1000, 1, "task message"}, {"newest result", 100, 1, 1000, "newest result"},
	} {
		t.Run(row.name, func(t *testing.T) {
			s := &session.Session{Messages: []events.Message{{ID: "old", Role: "assistant", Tokens: 100}, {ID: "task", Role: "user", Content: "CURRENT TASK WORD FOR WORD", Tokens: row.task}, {ID: "call", Role: "assistant", Tokens: 1}, {ID: "result", Role: "tool", Tokens: row.result}}}
			got := New(nil).Compact(context.Background(), s, "run", OneRuleInput{Used: 1200, Window: 1000, Fixed: row.fixed, SummaryPct: .9, Pin: "task", Summarize: func([]events.Message, int) (string, error) { return "summary", nil }})
			if !strings.Contains(got.Stop, row.want) || !strings.Contains(got.Stop, "tokens") {
				t.Fatalf("stop=%q", got.Stop)
			}
		})
	}
}

func TestOneRuleHandOffIsSmallerThanCrossingRequest2up(t *testing.T) {
	s := oneRuleChat("handoff-size")
	got := New(nil).Compact(context.Background(), s, "run", OneRuleInput{Used: 950, Window: 1000, Fixed: 100, SummaryPct: .9, Pin: "task", Summarize: func([]events.Message, int) (string, error) {
		return "DONE: old\nNEXT: task\nFILES CHANGED: none\nOPEN QUESTIONS: none", nil
	}})
	if got.HandOffRequestTokens <= 0 || got.HandOffRequestTokens > got.CrossingTokens {
		t.Fatalf("handoff request=%d crossing=%d", got.HandOffRequestTokens, got.CrossingTokens)
	}
}

func TestOneRuleSizeRefusalGetsOneActionAndOneRetry2up(t *testing.T) {
	s := oneRuleChat("refusal")
	first := New(nil).Compact(context.Background(), s, "run", OneRuleInput{Used: 400, Window: 1000, Fixed: 100, SummaryPct: .9, Pin: "task", RefusalSentence: "request exceeds the server limit", RefusalAttempt: 1, Summarize: func([]events.Message, int) (string, error) { return "summary", nil }})
	if !first.Triggered || !first.Changed || !first.Retry || first.Stop != "" {
		t.Fatalf("first refusal=%+v", first)
	}
	second := New(nil).Compact(context.Background(), s, "run", OneRuleInput{Used: 400, Window: 1000, Fixed: 100, SummaryPct: .9, Pin: "task", RefusalSentence: "request exceeds the server limit", RefusalAttempt: 2, Summarize: func([]events.Message, int) (string, error) { return "summary", nil }})
	if second.Changed || second.Retry || second.Stop != "request exceeds the server limit" {
		t.Fatalf("second refusal=%+v", second)
	}
}

func TestOneRuleNeverSilentlyDropsMessages2up(t *testing.T) {
	for _, row := range []struct {
		name   string
		answer string
		err    error
	}{
		{"summary", "DONE: old\nNEXT: task\nFILES CHANGED: none\nOPEN QUESTIONS: none", nil},
		{"error", "", errors.New("unavailable")},
		{"refusal", "", errors.New("refused")},
		{"empty", "", nil},
	} {
		t.Run(row.name, func(t *testing.T) {
			s := oneRuleChat(row.name)
			before := len(s.MessagesCopy())
			got := New(nil).Compact(context.Background(), s, "run", OneRuleInput{Used: 950, Window: 1000, Fixed: 100, SummaryPct: .9, Pin: "task", Summarize: func([]events.Message, int) (string, error) { return row.answer, row.err }})
			after := s.MessagesCopy()
			if len(after) >= before || !got.Changed || after[0].Role != "harness" || strings.TrimSpace(after[0].Content) == "" {
				t.Fatalf("silent removal: result=%+v before=%d after=%+v", got, before, after)
			}
		})
	}
}

func oneRuleChat(shape string) *session.Session {
	messages := []events.Message{{ID: "old-user", Role: "user", Content: "old", Tokens: 200}, {ID: "old-answer", Role: "assistant", Content: strings.Repeat("work ", 80), Tokens: 400}, {ID: "task", Role: "user", Content: "CURRENT TASK WORD FOR WORD", Tokens: 20}, {ID: "call", Role: "assistant", Tokens: 10}, {ID: "result", Role: "tool", Content: "newest", Tokens: 30}}
	if shape == "after failed run" {
		messages = append(messages, events.Message{ID: "status", Role: "harness", Content: "failed", Tokens: 5})
	}
	return &session.Session{Messages: messages}
}

func hasExact(messages []events.Message, id, content string) bool {
	for _, message := range messages {
		if message.ID == id && message.Content == content {
			return true
		}
	}
	return false
}

// Item 2we CHECK 2: at the line, older results that are at least half of the
// changeable context are stubbed first; the summary runs only if that is not enough.
func TestOneRulePrunesOlderResultsBeforeSummarizing2we(t *testing.T) {
	for _, row := range []struct {
		name               string
		used, r1, r2       int
		stubbed, summarize bool
		kind               string
	}{
		{"results at 60%", 950, 300, 210, true, false, "prune"},
		{"results at 40%", 950, 200, 140, false, true, "fresh"},
		{"results at 60% still over the line", 2100, 700, 500, true, true, "prune+fresh"},
	} {
		t.Run(row.name, func(t *testing.T) {
			s := &session.Session{Messages: []events.Message{
				{ID: "old-user", Role: "user", Content: "old", Tokens: 50},
				{ID: "old-call", Role: "assistant", ToolCalls: []events.ToolCall{{ID: "c1"}, {ID: "c2"}}, Tokens: 10},
				{ID: "old-r1", Role: "tool", ToolCallID: "c1", Name: "read_file", Category: "files", Content: "old file", Tokens: row.r1},
				{ID: "old-r2", Role: "tool", ToolCallID: "c2", Name: "shell", Category: "results", Content: "old output", Tokens: row.r2},
				{ID: "old-answer", Role: "assistant", Content: "answer", Tokens: 50},
				{ID: "task", Role: "user", Content: "CURRENT TASK WORD FOR WORD", Tokens: 20},
				{ID: "call", Role: "assistant", ToolCalls: []events.ToolCall{{ID: "c3"}}, Tokens: 10},
				{ID: "result", Role: "tool", ToolCallID: "c3", Name: "read_file", Category: "files", Content: "newest", Tokens: 30},
			}}
			bus, published := events.NewBus(), []events.Event{}
			bus.SetObserver(func(event events.Event) { published = append(published, event) })
			asked := []events.Message{}
			got := New(bus).Compact(context.Background(), s, "run", OneRuleInput{Used: row.used, Window: 1000, Fixed: 100, SummaryPct: .9, Pin: "task", Summarize: func(messages []events.Message, _ int) (string, error) {
				asked = append(asked, messages...)
				return "DONE: old\nNEXT: task\nFILES CHANGED: none\nOPEN QUESTIONS: none", nil
			}})
			if !got.Changed || got.Stop != "" || (len(asked) > 0) != row.summarize {
				t.Fatalf("result=%+v summarized=%v", got, len(asked) > 0)
			}
			after := s.MessagesCopy()
			want := "[read_file result removed at compaction, " + itoa(row.r1) + " tokens; recall reads it]"
			if row.stubbed && !row.summarize && (!hasExact(after, "old-r1", want) || !hasExact(after, "result", "newest") || !hasExact(after, "task", "CURRENT TASK WORD FOR WORD") || !hasExact(after, "old-user", "old")) {
				t.Fatalf("prune context=%+v", after)
			}
			if row.stubbed && !row.summarize && (after[0].Role != "harness" || after[0].Content != "Compacted: 2 results, "+itoa(row.r1+row.r2)+" tokens removed; nothing summarized") {
				t.Fatalf("prune line=%+v", after[0])
			}
			if row.summarize && row.stubbed && !hasExact(asked, "old-r1", want) {
				t.Fatalf("summary request was not stubbed: %+v", asked)
			}
			if row.summarize && !row.stubbed && !hasExact(asked, "old-r1", "old file") {
				t.Fatalf("summary request changed: %+v", asked)
			}
			var compaction map[string]any
			for _, event := range published {
				if event.Type == events.Compaction {
					compaction, _ = event.Data.(map[string]any)
				}
			}
			if compaction == nil || compaction["kind"] != row.kind {
				t.Fatalf("compaction=%+v", compaction)
			}
			affected, _ := compaction["affected_ids"].([]string)
			if row.stubbed && (compaction["pruned_results"] != 2 || compaction["pruned_tokens"] != row.r1+row.r2 || !contains(affected, "old-r1") || !contains(affected, "old-r2") || contains(affected, "result") || contains(affected, "task")) {
				t.Fatalf("prune event=%+v", compaction)
			}
			if !row.stubbed && compaction["pruned_results"] != 0 {
				t.Fatalf("fresh event=%+v", compaction)
			}
		})
	}
}

func itoa(value int) string { return strconv.Itoa(value) }

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
