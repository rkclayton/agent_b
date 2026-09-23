package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
)

func TestFreshNinetyKProfileAnswersHelloLive(t *testing.T) {
	baseURL := os.Getenv("AGENTB_REAL_MODEL_URL")
	model := os.Getenv("AGENTB_REAL_MODEL_NAME")
	if baseURL == "" || model == "" {
		t.Skip("set AGENTB_REAL_MODEL_URL and AGENTB_REAL_MODEL_NAME for live acceptance")
	}
	cfg := config.Defaults(t.TempDir())
	cfg.Context.Accounting = "auto"
	profile := cfg.Servers[0]
	profile.ID, profile.Label, profile.BaseURL, profile.Model = "fresh-90k", "Fresh 90k", baseURL, model
	profile.Context.NCtx, profile.Context.ReserveOutput = 0, 10240
	profile.RequestTimeoutS = 120
	profile.Reasoning.Enabled = false
	profile.Capabilities.NCtx = 90000
	profile.Capabilities.Server = "llama.cpp"
	profile.Capabilities.Tokenize = true
	profile.Capabilities.ApplyTemplate = true
	profile.Capabilities.ApplyTemplateTools = true
	profile.Capabilities.Streaming = true
	cfg.Servers = []config.Profile{profile}
	lookup := func(id string) (*config.Profile, bool) { return &profile, id == profile.ID }
	bus := newCapturedBus()
	runner := NewRunner(bus.Bus, tools.New(), &PromptRenderer{text: "Answer briefly."}, lookup, func() config.Config { return cfg })
	item := &session.Session{ID: "fresh", ServerID: profile.ID, Workspace: t.TempDir(), Runnable: true, Run: session.RunState{Status: "running", MaxTurns: 4}, ToolsEnabled: map[string]bool{}, ToolCalls: map[string]int{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	item.Append(events.Message{ID: "hello", Role: "user", Content: "hello", Category: "history", Tokens: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	reason, detail, _ := runner.Run(ctx, item, "r1")
	if reason != "done" {
		t.Fatalf("run stopped: reason=%s detail=%q", reason, detail)
	}
	var budget events.Budget
	answer := ""
	for _, event := range bus.Recent(item.ID) {
		switch event.Type {
		case events.BudgetEvent:
			if value, ok := event.Data.(events.Budget); ok {
				if value.NCtx == 0 {
					t.Fatalf("zero context window was published: %+v", value)
				}
				budget = value
			}
		case events.ModelResponse:
			if data, ok := event.Data.(map[string]any); ok {
				answer, _ = data["content"].(string)
			}
		}
	}
	if budget.NCtx != 90000 || budget.Reserve != 10240 || budget.Ceiling != 79760 || budget.UsedEst <= 0 {
		t.Fatalf("budget = %+v", budget)
	}
	if strings.TrimSpace(answer) == "" {
		t.Fatal("hello produced no answer")
	}
	t.Logf("hello answer=%q budget n_ctx=%d prompt=%d reserve=%d ceiling=%d mode=%s", answer, budget.NCtx, budget.UsedEst, budget.Reserve, budget.Ceiling, budget.Mode)
}
