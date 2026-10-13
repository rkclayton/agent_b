package agent

import (
	"context"
	"net"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
)

type profileSkillHost2u7 struct{ seen []string }

func (h *profileSkillHost2u7) Index(agentID string) string {
	h.seen = append(h.seen, agentID)
	if agentID == "review" {
		return "review-only | Review skill."
	}
	return ""
}

func (*profileSkillHost2u7) Read(string, string) {}
func (*profileSkillHost2u7) Proposal(context.Context, *session.Session, string) (string, error) {
	return "", nil
}

func TestBudgetPromptRequestsTheChatsOwnProfileSkillIndex2u7(t *testing.T) {
	cfg := config.Defaults(t.TempDir())
	cfg.Context.Accounting = "estimated"
	connection := cfg.Connections[0]
	connection.Model = "stand-in"
	connection.Context.NCtx = 32768
	connection.Context.ReserveOutput = 10240
	runner := NewRunner(events.NewBus(), tools.New(), &PromptRenderer{text: "system {{skills}}"}, func(string) (*config.Connection, bool) {
		return &connection, true
	}, func() config.Config { return cfg })
	host := &profileSkillHost2u7{}
	runner.SetSkillHost(host)
	for _, agentID := range []string{"agent_b", "review"} {
		item := &session.Session{ID: agentID, AgentID: agentID, ConnectionID: connection.ID, ToolsEnabled: map[string]bool{}, ToolCalls: map[string]int{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
		runner.PublishBudget(context.Background(), item)
	}
	if len(host.seen) != 2 || host.seen[0] != "agent_b" || host.seen[1] != "review" {
		t.Fatalf("skill indexes requested for %v", host.seen)
	}
}

func TestPublishBudgetDialFailurePublishesEstimatedBudget(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	connection := config.Connection{
		ID:              "offline",
		BaseURL:         "http://" + address,
		Model:           "offline",
		RequestTimeoutS: 1,
		Context:         config.Context{NCtx: 32768, ReserveOutput: 10240},
		Capabilities:    config.Capabilities{Tokenize: true, ApplyTemplate: true, ApplyTemplateTools: true},
	}
	cfg := config.Defaults(t.TempDir())
	cfg.Context.Accounting = "exact"
	cfg.Connections = []config.Connection{connection}
	bus := newCapturedBus()
	runner := NewRunner(bus.Bus, tools.New(), &PromptRenderer{text: "system"}, func(id string) (*config.Connection, bool) {
		return &connection, id == connection.ID
	}, func() config.Config { return cfg })
	var reportedSession, reportedConnection string
	runner.SetModelUnreachable(func(sessionID, connectionID string) {
		reportedSession, reportedConnection = sessionID, connectionID
	})
	item := &session.Session{
		ID:             "main",
		ConnectionID:   connection.ID,
		Workspace:      t.TempDir(),
		ToolsEnabled:   map[string]bool{},
		ToolCalls:      map[string]int{},
		SchemaTokens:   map[string]int{},
		MarginalTokens: map[string]int{},
	}

	runner.PublishBudget(context.Background(), item)

	var budgetError, unreachable, estimated bool
	for _, event := range bus.Recent(item.ID) {
		data, _ := event.Data.(map[string]any)
		if event.Type == events.Error && data["where"] == "budget" {
			budgetError = true
		}
		if event.Type == events.ModelUnreachable && data["host"] == address {
			unreachable = true
		}
		if budget, ok := event.Data.(events.Budget); event.Type == events.BudgetEvent && ok && budget.Estimated && len(budget.Findings) == 1 {
			estimated = true
		}
	}
	if budgetError || unreachable || !estimated {
		t.Fatalf("budget_error=%t model_unreachable=%t estimated=%t events=%#v", budgetError, unreachable, estimated, bus.Recent(item.ID))
	}
	if reportedSession != "" || reportedConnection != "" {
		t.Fatalf("reachability callback session=%q connection=%q", reportedSession, reportedConnection)
	}
}
