package contextmgr

import (
	"context"
	"errors"
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
