package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/llm"
	"harness/internal/session"
	"harness/internal/tools"
)

// Item 2go (v1.2.5): the name comes from the operator's first message, once.
// These are the cases the rule is written in terms of: the first clause or the
// first six words, whichever is shorter, trimmed of punctuation.
func TestFirstMessageNameTakesTheShorterOfClauseAndSixWords(t *testing.T) {
	for _, item := range []struct{ message, want string }{
		{"fix the login bug in vesper", "fix the login bug in vesper"},
		{"fix the login bug in vesper and then deploy it", "fix the login bug in vesper"},
		{"Fix the login bug. Then deploy.", "Fix the login bug"},
		{"read AGENTS.md, then tell me what it says", "read AGENTS.md"},
		{"why is the gate red?", "why is the gate red"},
		{"deploy", "deploy"},
		{"  padded   spacing   here  ", "padded spacing here"},
		{"first line\nsecond line", "first line"},
		{"a, b", "a"},
		{"!!!", ""},
		{"", ""},
		// A dotted version or a file name is not the end of a thought.
		{"bump to v1.2.5 everywhere", "bump to v1.2.5 everywhere"},
		{"edit user.go so it compiles", "edit user.go so it compiles"},
	} {
		if got := firstMessageName(item.message); got != item.want {
			t.Fatalf("firstMessageName(%q) = %q, want %q", item.message, got, item.want)
		}
	}
}

func TestFirstMessageNameIsBounded(t *testing.T) {
	long := ""
	for i := 0; i < 40; i++ {
		long += "abcdefghij "
	}
	name := firstMessageName(long)
	if len([]rune(name)) > 80 {
		t.Fatalf("name is %d runes: %q", len([]rune(name)), name)
	}
	if words := len([]rune(name)); words == 0 {
		t.Fatal("a long message still has a name")
	}
}

func TestModelNamesFirstChatOnceAndLeavesFailureMechanical(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		content, want string
	}{
		{name: "swap", status: http.StatusOK, content: "Fix local model setup", want: "Fix local model setup"},
		{name: "failure", status: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tc.status != http.StatusOK {
					http.Error(w, "no", tc.status)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": tc.content}, "finish_reason": "stop"}}})
			}))
			defer server.Close()
			connection := config.Connection{ID: "p", BaseURL: server.URL, Model: "fake", RequestTimeoutS: 2}
			bus := events.NewBus()
			var journal []events.Event
			bus.SetSink(func(event events.Event) error { journal = append(journal, event); return nil })
			runner := NewRunner(bus, tools.New(), &PromptRenderer{}, func(string) (*config.Connection, bool) { return &connection, true }, func() config.Config { return config.Config{} })
			var renamed string
			runner.SetSessionRenamer(func(_ string, label string, _ string) error { renamed = label; return nil })
			item := &session.Session{ID: "s", Label: "mechanical name", ConnectionID: "p", Role: "b", Runnable: true, ToolsEnabled: map[string]bool{}, ToolCalls: map[string]int{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
			item.Append(events.Message{Role: "user", Content: "please fix setup"})
			item.Append(events.Message{Role: "assistant", Content: "I fixed setup."})
			runner.nameAfterFirstRun(item, "r1")
			runner.nameAfterFirstRun(item, "r2")
			if renamed != tc.want {
				t.Fatalf("renamed=%q want=%q", renamed, tc.want)
			}
			if calls.Load() != 1 {
				t.Fatalf("calls=%d want 1", calls.Load())
			}
			seen := 0
			for _, event := range journal {
				if event.Type == events.ChatNamed {
					seen++
				}
			}
			if seen != 1 {
				t.Fatalf("chat.named events=%d", seen)
			}
		})
	}
}

func TestModelNamingDoesNotTouchWorkers(t *testing.T) {
	connection := config.Connection{ID: "p", BaseURL: "http://127.0.0.1:1", RequestTimeoutS: 1}
	runner := NewRunner(events.NewBus(), tools.New(), &PromptRenderer{}, func(string) (*config.Connection, bool) { return &connection, true }, func() config.Config { return config.Config{} })
	called := false
	runner.SetSessionRenamer(func(string, string, string) error { called = true; return nil })
	item := &session.Session{ID: "c", Label: "work", ConnectionID: "p", Role: "c"}
	item.Append(events.Message{Role: "user", Content: "work"})
	item.Append(events.Message{Role: "assistant", Content: "done"})
	runner.nameAfterFirstRun(item, "r1")
	if called {
		t.Fatal("worker was renamed")
	}
}

func TestModelNamingStillUsesFirstPairWhenAnotherMessageWasQueued(t *testing.T) {
	var request llm.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&request)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "First pair name"}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	connection := config.Connection{ID: "p", BaseURL: server.URL, Model: "fake", RequestTimeoutS: 2}
	runner := NewRunner(events.NewBus(), tools.New(), &PromptRenderer{}, func(string) (*config.Connection, bool) { return &connection, true }, func() config.Config { return config.Config{} })
	var renamed string
	runner.SetSessionRenamer(func(_ string, label string, _ string) error { renamed = label; return nil })
	item := &session.Session{ID: "s", Label: "mechanical", ConnectionID: "p", Role: "b"}
	item.Append(events.Message{Role: "user", Content: "first question"})
	item.Append(events.Message{Role: "assistant", Content: "first answer"})
	item.Append(events.Message{Role: "user", Content: "already queued second question"})
	runner.nameAfterFirstRun(item, "r1")
	if renamed != "First pair name" {
		t.Fatalf("renamed=%q", renamed)
	}
	content, _ := request.Messages[1].Content.(string)
	if len(request.Messages) != 2 || !strings.Contains(content, "first question") || strings.Contains(content, "second question") {
		t.Fatalf("naming request did not isolate the first pair: %+v", request.Messages)
	}
}

func TestModelChatNameRejectsLiteralNull(t *testing.T) {
	if got := modelChatName("null"); got != "" {
		t.Fatalf("modelChatName(null)=%q", got)
	}
}
