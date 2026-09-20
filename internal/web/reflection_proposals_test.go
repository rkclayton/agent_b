package web

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"harness/internal/agent"
	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/projection"
	"harness/internal/reflection"
	"harness/internal/session"
	"harness/internal/tools"
)

// Item 17-i's registration decision, v1.1.1/W3. Reflection proposes a plan and
// registers nothing; the operator meets the ordinary Allow-this card, marked as
// proposed by reflection. Approving registers through the existing path;
// declining suppresses that repository until its agent files change.
func proposalFixture(t *testing.T) (*Server, *session.Session, *reflection.Store, string) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Defaults(root)
	cfg.Servers = []config.Profile{{ID: "fake", Label: "Fake", BaseURL: "http://127.0.0.1:9", Model: "test", RequestTimeoutS: 5}}
	cfg.Agents = []config.Agent{{Name: "Tester", B: "fake", Toolset: config.FullToolset()}}
	bus := events.NewBus()
	server := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, bus)
	writers, err := events.NewWriters(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writers.Close() })
	projector := projection.NewStore()
	bus.SetDurableSink(writers.WriteRecord, projector.Apply, projector.MarkStale)
	server.SetProjection(projector, writers)
	registry := session.NewRegistry(bus, writers, server.Profile, cfg.Run.MaxTurns, server.ConfigSnapshot)
	server.SetRegistry(registry)
	runner := newRunnerForProposals(t, server, bus, registry)
	server.SetRuntime(nil, runner, nil)
	if _, err := registry.Create("chat", config.AgentID("Tester"), cfg.Workspace); err != nil {
		t.Fatal(err)
	}
	item := registry.List()[0]
	server.StartReflection(time.Hour)
	t.Cleanup(server.StopReflection)

	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("guidance"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := server.reflection.store
	proposal := reflection.Proposal{Root: repo, AgentFile: "AGENTS.md", Activity: "2 runs touched this folder, and AGENTS.md sits at its root", Fingerprint: reflection.AgentFileFingerprint(repo)}
	if err := store.UpsertProposal(proposal); err != nil {
		t.Fatal(err)
	}
	return server, item, store, repo
}

// answerNextCard answers the approval the offer raises, once it appears.
func answerNextCard(t *testing.T, server *Server, bus *events.Bus, sessionID, decision string) chan string {
	t.Helper()
	answered := make(chan string, 1)
	channel, unsubscribe := bus.Subscribe()
	go func() {
		defer unsubscribe()
		deadline := time.After(20 * time.Second)
		for {
			select {
			case event := <-channel:
				if event.Type != events.ApprovalRequired {
					continue
				}
				data, _ := event.Data.(map[string]any)
				name, _ := data["name"].(string)
				callID, _ := data["call_id"].(string)
				args, _ := data["args"].(map[string]any)
				if name != proposalOfferName {
					continue
				}
				if proposer, _ := args["proposed_by"].(string); proposer != "reflection" {
					answered <- "the card is not marked as proposed by reflection"
					return
				}
				if activity, _ := args["activity"].(string); activity == "" {
					answered <- "the card carries no activity line"
					return
				}
				if err := server.runner.Gate().Decide(sessionID, callID, decision); err != nil {
					answered <- "decide: " + err.Error()
					return
				}
				answered <- ""
				return
			case <-deadline:
				answered <- "no card was raised"
				return
			}
		}
	}()
	return answered
}

func TestReflectionProposesAPlanAndApprovalRegistersItThroughTheExistingPath(t *testing.T) {
	server, item, store, repo := proposalFixture(t)
	answered := answerNextCard(t, server, server.bus, item.ID, "approve")
	server.OfferReflectionProposals()
	if problem := <-answered; problem != "" {
		t.Fatal(problem)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		proposal, found, err := store.ProposalFor(repo)
		if err != nil {
			t.Fatal(err)
		}
		if found && proposal.State == reflection.ProposalApproved {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the approved proposal was not recorded: %+v", proposal)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !planRegistered(item, repo) {
		t.Fatal("approval did not register the plan through the existing path")
	}
}

func TestADeclinedProposalIsNotOfferedAgainUntilTheAgentFilesChange(t *testing.T) {
	server, item, store, repo := proposalFixture(t)
	answered := answerNextCard(t, server, server.bus, item.ID, "deny")
	server.OfferReflectionProposals()
	if problem := <-answered; problem != "" {
		t.Fatal(problem)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		proposal, _, err := store.ProposalFor(repo)
		if err != nil {
			t.Fatal(err)
		}
		if proposal.State == reflection.ProposalDeclined {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the declined proposal was not recorded: %+v", proposal)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if planRegistered(item, repo) {
		t.Fatal("a declined proposal registered the plan")
	}

	// The same repository, proposed again by a later pass: still declined.
	unchanged := reflection.Proposal{Root: repo, AgentFile: "AGENTS.md", Activity: "3 runs touched this folder, and AGENTS.md sits at its root", Fingerprint: reflection.AgentFileFingerprint(repo)}
	if err := store.UpsertProposal(unchanged); err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingProposals(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("a declined proposal came back with no change: %+v", pending)
	}

	// Its agent files change: the question is worth asking again.
	time.Sleep(1100 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("guidance, rewritten"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := reflection.Proposal{Root: repo, AgentFile: "AGENTS.md", Activity: unchanged.Activity, Fingerprint: reflection.AgentFileFingerprint(repo)}
	if err := store.UpsertProposal(changed); err != nil {
		t.Fatal(err)
	}
	pending, err = store.PendingProposals(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Root != repo {
		t.Fatalf("the proposal did not return after its agent files changed: %+v", pending)
	}
}

// newRunnerForProposals builds the ordinary runner, so the card the offer
// raises is the one the product raises.
func newRunnerForProposals(t *testing.T, server *Server, bus *events.Bus, registry *session.Registry) *agent.Runner {
	t.Helper()
	cfg := server.ConfigSnapshot()
	promptPath := filepath.Join(t.TempDir(), "system.md")
	if err := os.WriteFile(promptPath, []byte("system {{tools}} {{memory}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	renderer, err := agent.LoadTemplate(promptPath)
	if err != nil {
		t.Fatal(err)
	}
	toolset := tools.New()
	toolset.Configure(cfg)
	runner := agent.NewRunner(bus, toolset, renderer, server.Profile, server.ConfigSnapshot)
	_ = registry
	return runner
}
