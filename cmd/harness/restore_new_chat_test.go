package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"harness/internal/agent"
	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/projection"
	"harness/internal/session"
	"harness/internal/tools"
)

// Item 2es: after a restart with a retained chat present, the chat `+` creates
// is a new id with its own scratch folder, and its first request carries the
// system prompt and the one user message: no retained history, no `files`.
// Ids minted after the restart do not repeat the retained chat's ids.
func TestNewChatAfterRestartCarriesNothingFromARetainedChat(t *testing.T) {
	root := t.TempDir()
	logs := filepath.Join(root, "logs")
	cfg := config.Defaults(filepath.Join(root, "scratch"))
	cfg.Context.Accounting = "estimated"
	profile := &cfg.Servers[0]
	profile.BaseURL = "http://127.0.0.1:1"
	profile.RequestTimeoutS = 1
	profile.Context.NCtx = 32768
	profile.Context.ReserveOutput = 8192
	profile.Capabilities.Tokenize = false
	profile.Capabilities.Streaming = true
	profile.Capabilities.ToolCalls = true
	profile.Capabilities.OverflowBehavior = "error"
	profiles := func(id string) (*config.Profile, bool) { return profile, id == profile.ID }

	firstWriters, err := events.NewWriters(logs)
	if err != nil {
		t.Fatal(err)
	}
	firstBus := events.NewBus()
	firstBus.SetDurableSink(firstWriters.WriteRecord, nil, nil)
	firstRegistry := session.NewRegistry(firstBus, firstWriters, profiles, 40, func() config.Config { return cfg })
	firstRegistry.SetPlansRoot(filepath.Join(root, "plans"))
	retained, err := firstRegistry.Create("", cfg.DefaultAgentID(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []events.Message{
		{ID: "m-7", Role: "user", Category: "history", Content: "work on the runbook"},
		{ID: "m-8", Role: "tool", Category: "files", Content: "[byte window: offset=1 bytes=12 total=12 more=false]\n1: runbook text", Tokens: 900},
		{ID: "m-9", Role: "assistant", Category: "history", Content: "the runbook is saved"},
	} {
		retained.Append(message)
		firstBus.Publish(events.New(events.MessageAppended, retained.ID, "r3", map[string]any{"message": message}))
	}
	if err := firstWriters.Close(); err != nil {
		t.Fatal(err)
	}

	secondWriters, err := events.NewWriters(logs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondWriters.Close() })
	secondBus := events.NewBus()
	var requests []events.Event
	var budgets []events.Event
	secondBus.SetDurableSink(func(record events.Event) (events.LogCursor, error) {
		switch record.Type {
		case events.ModelRequest:
			requests = append(requests, record)
		case events.BudgetEvent:
			budgets = append(budgets, record)
		}
		return secondWriters.WriteRecord(record)
	}, nil, nil)
	secondRegistry := session.NewRegistry(secondBus, secondWriters, profiles, 40, func() config.Config { return cfg })
	secondRegistry.SetPlansRoot(filepath.Join(root, "plans"))
	restored, err := restoreRetainedChats(secondWriters, secondRegistry)
	if err != nil || len(restored) != 1 || len(restored[0].Snapshot().Messages) != 3 {
		t.Fatalf("restore: %d chats, err %v", len(restored), err)
	}
	// The restored chat's new log generation projects its retained transcript,
	// not an empty chat (the gate caught the first attempt at this failing).
	projected, _, err := projection.ProjectFile(restored[0].LogPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	kept := false
	for _, entry := range projected.Chat {
		if entry.Type == "user" && entry.Text == "work on the runbook" {
			kept = true
		}
	}
	if !kept {
		t.Fatalf("restored chat projects without its transcript: %+v", projected.Chat)
	}
	if floor := retainedIDFloor(secondWriters); floor < 9 {
		t.Fatalf("id floor %d does not cover m-9", floor)
	}

	fresh, err := secondRegistry.CreateLike(restored[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ID == restored[0].ID || len(fresh.Snapshot().Messages) != 0 || fresh.Workspace == restored[0].Workspace {
		t.Fatalf("new chat id=%s messages=%d workspace=%s (retained %s at %s)", fresh.ID, len(fresh.Snapshot().Messages), fresh.Workspace, restored[0].ID, restored[0].Workspace)
	}

	template := filepath.Join(root, "system.md")
	if err := os.WriteFile(template, []byte("system\nmemory={{memory}}\ntools={{tools}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	renderer, err := agent.LoadTemplate(template)
	if err != nil {
		t.Fatal(err)
	}
	runner := agent.NewRunner(secondBus, tools.New(), renderer, profiles, func() config.Config { return cfg })
	runner.ReserveIDs(retainedIDFloor(secondWriters))
	fresh.Runnable = true
	fresh.Run = session.RunState{Status: "running", MaxTurns: 40}
	message, err := runner.AddUser(context.Background(), fresh, "hello")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"m-7", "m-8", "m-9"} {
		if message.ID == id {
			t.Fatalf("the new chat's first message reused retained id %s", id)
		}
	}
	_, _, _ = runner.Run(context.Background(), fresh, "r1")

	var request map[string]any
	for _, event := range requests {
		if event.SessionID == fresh.ID {
			request, _ = event.Data.(map[string]any)
			break
		}
	}
	if request == nil {
		t.Fatal("the new chat made no model request")
	}
	if count, _ := request["message_count"].(int); count != 2 {
		t.Fatalf("message_count=%v, want 2 (system prompt + hello)", request["message_count"])
	}
	for _, event := range budgets {
		if event.SessionID != fresh.ID {
			continue
		}
		budget, _ := event.Data.(events.Budget)
		if budget.Categories["files"] != 0 || budget.Categories["results"] != 0 || budget.Categories["summary"] != 0 {
			t.Fatalf("new chat budget carries retained context: %+v", budget.Categories)
		}
	}
}
