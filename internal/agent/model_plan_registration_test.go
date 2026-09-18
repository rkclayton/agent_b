package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
)

// Item 2fa: a model turn that is exactly "Add <path> as a plan" raises the same
// Allow-this card as the operator's sentence, named as the model's proposal;
// approval registers, a decline leaves nothing.
func TestModelProposedPlanRegistrationRaisesTheOperatorsCard(t *testing.T) {
	for _, tc := range []struct {
		name, decision string
		wantRegistered bool
	}{{"approved", "once", true}, {"declined", "deny", false}} {
		t.Run(tc.name, func(t *testing.T) {
			root, repo := t.TempDir(), filepath.Join(t.TempDir(), "repo")
			if err := os.MkdirAll(repo, 0o700); err != nil {
				t.Fatal(err)
			}
			line := "Add " + repo + " as a plan"
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": line}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5}})
				fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			}))
			defer model.Close()
			cfg := config.Defaults(root)
			cfg.Context.Accounting = "estimated"
			profile := cfg.Servers[0]
			profile.ID, profile.BaseURL = "main", model.URL
			profile.Context.NCtx, profile.Context.ReserveOutput = 32768, 8192
			profile.Capabilities.Streaming, profile.Capabilities.ToolCalls = true, true
			profile.Capabilities.OverflowBehavior = "error"
			cfg.Servers = []config.Profile{profile}
			approvals := make(chan events.Event, 8)
			bus := events.NewBus()
			bus.SetSink(func(event events.Event) error {
				if event.Type == events.ApprovalRequired {
					approvals <- event
				}
				return nil
			})
			registrations := 0
			item := &session.Session{
				ID: "b-chat", ServerID: profile.ID, Role: "b", Workspace: root, Runnable: true,
				Run: session.RunState{Status: "running", MaxTurns: 4}, ToolsEnabled: map[string]bool{},
				ToolCalls: map[string]int{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{},
				RegisterPlan: func(path string) (session.Plan, bool, error) {
					registrations++
					return session.Plan{ID: "p", Name: "repo", Repo: path}, true, nil
				},
			}
			runner := NewRunner(bus, tools.New(), &PromptRenderer{text: "system"}, func(id string) (*config.Profile, bool) { return &profile, id == profile.ID }, func() config.Config { return cfg })
			if _, err := runner.AddUser(context.Background(), item, "I found the game repo; should it be a plan?"); err != nil {
				t.Fatal(err)
			}
			done := make(chan string, 1)
			go func() {
				reason, detail, _ := runner.Run(context.Background(), item, "run-1")
				done <- reason + " / " + detail
			}()
			var required events.Event
			select {
			case required = <-approvals:
			case <-time.After(5 * time.Second):
				t.Fatal("the model's line raised no card")
			}
			data := required.Data.(map[string]any)
			if data["name"] != "plan registration proposed by agent_b" || data["args"].(map[string]any)["path"] != filepath.Clean(repo) {
				t.Fatalf("approval=%#v", data)
			}
			if err := runner.Gate().Decide(item.ID, data["call_id"].(string), tc.decision); err != nil {
				t.Fatal(err)
			}
			result := <-done
			if (registrations == 1) != tc.wantRegistered {
				t.Fatalf("run=%s registrations=%d", result, registrations)
			}
			if messages := item.MessagesCopy(); messages[len(messages)-1].Content != line {
				t.Fatalf("the line must stay in the transcript above the card: %+v", messages[len(messages)-1])
			}
		})
	}
}

func TestAModelLineInsideProseRaisesNothing(t *testing.T) {
	if _, ok := requestedPlanPath("Sure — you could say: Add C:\repo as a plan"); ok {
		t.Fatal("a sentence that merely contains the line must not register")
	}
}
