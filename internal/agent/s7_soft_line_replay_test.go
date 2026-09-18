package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"harness/internal/config"
	contextmgr "harness/internal/context"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
)

// s7's 2026-09-18 20:01 segment, replayed on the fake server (items 2ey, 2ex).
// testdata/s7-2001-shape.json is the restored session's shape taken from the
// tape — ids, roles, categories, token weights, elision and call pairing — with
// every piece of the operator's text replaced by filler of the same weight.
type shapeMessage struct {
	ID         string `json:"id"`
	Role       string `json:"role"`
	Category   string `json:"category"`
	Tokens     int    `json:"tokens"`
	Elided     bool   `json:"elided"`
	Name       string `json:"name"`
	ToolCallID string `json:"tool_call_id"`
	Turn       int    `json:"turn"`
	OK         *bool  `json:"ok"`
	ToolCalls  []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"tool_calls"`
}

func loadS7Shape(t *testing.T) []events.Message {
	t.Helper()
	raw, err := os.ReadFile("testdata/s7-2001-shape.json")
	if err != nil {
		t.Fatal(err)
	}
	var shape []shapeMessage
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatal(err)
	}
	messages := make([]events.Message, 0, len(shape))
	for _, item := range shape {
		// The fake tokenizer counts four runes per token.
		content := strings.Repeat("w", max(1, item.Tokens)*4)
		if item.Elided {
			content = "[elided: " + item.Name + " " + item.ID + "]"
		}
		message := events.Message{ID: item.ID, Role: item.Role, Category: item.Category, Tokens: item.Tokens, Elided: item.Elided, Name: item.Name, ToolCallID: item.ToolCallID, Turn: item.Turn, OK: item.OK, Content: content}
		for _, call := range item.ToolCalls {
			message.ToolCalls = append(message.ToolCalls, events.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
		}
		messages = append(messages, message)
	}
	return messages
}

func TestS7SegmentCompactsAtTheSoftLineBeforeTheRequest(t *testing.T) {
	server := newSummaryServer(t, "INTENT: continue the rpg\nNEXT STEP: answer the operator")
	cfg := config.Defaults(t.TempDir())
	cfg.Context.Accounting = "auto"
	main := cfg.Servers[0]
	main.ID, main.Label, main.BaseURL, main.Model = "main", "main", server.server.URL, "main-model"
	main.Context.NCtx = 32768
	main.RequestTimeoutS = 5
	main.Capabilities.Tokenize = true
	main.Capabilities.ApplyTemplate = true
	cfg.Servers = []config.Profile{main}
	cfg.Agents = []config.Agent{{Name: "Coder", B: "main", Toolset: config.FullToolset()}}
	bus := newCapturedBus()
	runner := NewRunner(bus.Bus, tools.New(), &PromptRenderer{text: "system {{workspace}} {{memory}} {{tools}}"}, cfg.Profile, func() config.Config { return cfg })
	s := &session.Session{ID: "s7", AgentID: cfg.DefaultAgentID(), ServerID: "main", Workspace: t.TempDir(), Runnable: true, Run: session.RunState{Status: "running", MaxTurns: 10}, ToolsEnabled: map[string]bool{}, ToolCalls: map[string]int{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	// As restore does (cmd/harness), older results come back as stubs.
	s.ReplaceMessages(contextmgr.StubOlderResults(loadS7Shape(t), 16384))
	if _, err := runner.AddUser(context.Background(), s, "test"); err != nil {
		t.Fatal(err)
	}
	reason, detail, _ := runner.Run(context.Background(), s, "r-test")

	firstCompaction, firstRequest := -1, -1
	var trigger any
	for index, event := range bus.Recent(s.ID) {
		switch event.Type {
		case events.Compaction:
			data := event.Data.(map[string]any)
			t.Logf("compaction #%d kind=%v trigger=%v %v→%v affected=%v", index, data["kind"], data["trigger"], data["before"], data["after"], data["affected_ids"])
			if firstCompaction < 0 {
				firstCompaction = index
				trigger = event.Data.(map[string]any)["trigger"]
			}
		case events.ModelRequest:
			if firstRequest < 0 {
				data, _ := json.Marshal(event.Data)
				t.Logf("first request: %.300s", data)
			}
			if firstRequest < 0 {
				firstRequest = index
			}
		}
	}
	t.Logf("s7 20:01 replay: reason=%s detail=%q first compaction #%d trigger=%v, first request #%d", reason, detail, firstCompaction, trigger, firstRequest)
	if firstCompaction < 0 || trigger != "soft_pct" {
		t.Fatalf("first compaction #%d trigger %v, want soft_pct", firstCompaction, trigger)
	}
	if firstRequest >= 0 && firstCompaction > firstRequest {
		t.Fatalf("compaction #%d came after the first request #%d", firstCompaction, firstRequest)
	}
}
