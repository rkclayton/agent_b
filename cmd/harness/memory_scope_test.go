package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/memory"
	"harness/internal/session"
	"harness/internal/tools"
)

// Item 2fh, the walk's step 6 without a model: what one scratch chat remembers
// "for later chats" a new chat carries, a project fact from a chat working in a
// plan's repository lands in that plan's layer, and a scratch folder never has
// a memory file of its own.
func TestMemoryCarriesAcrossScratchChatsAndProjectFactsGoToThePlan(t *testing.T) {
	root, repo := t.TempDir(), t.TempDir()
	data := filepath.Join(root, "data")
	cfg := config.Defaults(filepath.Join(data, "scratch"))
	profile := &cfg.Servers[0]
	profiles := func(id string) (*config.Profile, bool) { return profile, id == profile.ID }
	writers, err := events.NewWriters(filepath.Join(data, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	bus := events.NewBus()
	bus.SetDurableSink(writers.WriteRecord, nil, nil)
	registry := session.NewRegistry(bus, writers, profiles, 40, func() config.Config { return cfg })
	registry.SetPlansRoot(filepath.Join(data, "plans"))
	manager := memory.New(data, func() config.Config { return cfg }, func(_ context.Context, _, text string) (int, error) { return len(text) / 4, nil })
	registry.SetMemoryLoader(manager.Load)
	registry.SetAgentMemoryLoader(manager.LoadAgent)
	remember := tools.NewRemember(manager, bus)
	agentID := cfg.DefaultAgentID()

	chatA, err := registry.Create("a", agentID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !chatA.Scratch {
		t.Fatalf("a new chat must be a scratch chat: %s", chatA.Workspace)
	}
	reply, err := remember.Call(context.Background(), chatA, map[string]any{"note": "the walk project's mascot is a heron named Quill", "target": "folder"})
	if err != nil || !strings.Contains(reply, "agent layer") {
		t.Fatalf("a fact from a chat with no project in scope goes to the agent layer, and says so: %q %v", reply, err)
	}
	if _, err := os.Stat(manager.Path(chatA.Workspace)); !os.IsNotExist(err) {
		t.Fatalf("the scratch folder got a memory file of its own: %v", err)
	}
	chatB, err := registry.Create("b", agentID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(chatB.AgentMemoryBlock, "heron named Quill") {
		t.Fatalf("chat B does not carry chat A's note: %q", chatB.AgentMemoryBlock)
	}

	if _, _, err := registry.EnsurePlan(repo); err != nil {
		t.Fatal(err)
	}
	chatC, err := registry.Create("c", agentID, "")
	if err != nil {
		t.Fatal(err)
	}
	if written, err := chatC.WriteRoot(filepath.Join(repo, "notes.txt")); err != nil || !strings.EqualFold(written, filepath.Clean(repo)) {
		t.Fatalf("a write into the plan's repository: %q %v", written, err)
	}
	reply, err = remember.Call(context.Background(), chatC, map[string]any{"note": "the build uses make", "target": "folder"})
	if err != nil || strings.Contains(reply, "agent layer") {
		t.Fatalf("a project fact from a chat working in a plan's repo: %q %v", reply, err)
	}
	if notes, _ := manager.Read(repo); !strings.Contains(notes, "the build uses make") {
		t.Fatalf("the fact did not land in the plan's layer: %q", notes)
	}
	if agentNotes, _ := manager.ReadAgent(agentID); strings.Contains(agentNotes, "the build uses make") {
		t.Fatal("the project fact leaked into the agent layer")
	}

	// A later chat on the same plan loads that layer at its next run boundary.
	chatE, err := registry.Create("e", agentID, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(chatE.MemoryBlock, "the build uses make") {
		t.Fatal("a chat that has not worked in the plan must not carry its layer")
	}
	if _, err := chatE.WriteRoot(filepath.Join(repo, "more.txt")); err != nil {
		t.Fatal(err)
	}
	if !chatE.RefreshScratchMemory() || !strings.Contains(chatE.MemoryBlock, "the build uses make") {
		t.Fatalf("a chat working in the plan must carry its layer: %q", chatE.MemoryBlock)
	}
}
