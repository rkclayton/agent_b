package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/agent"
	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/memory"
	"harness/internal/session"
	"harness/internal/tools"
)

func TestUserReplyPreferenceFollowsProfileSwitch(t *testing.T) {
	requests := make(chan string, 1)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		if !strings.Contains(string(body), `"role":"tool"`) {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"fixture-recall","type":"function","function":{"name":"recall","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n\n")
			return
		}
		select {
		case requests <- string(body):
		default:
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"One brief answer.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer model.Close()
	data := t.TempDir()
	cfg := config.Defaults(data)
	cfg.Context.Accounting = "estimated"
	cfg.Connections[0].BaseURL, cfg.Connections[0].Model = model.URL, "fixture"
	cfg.Connections[0].Context.NCtx = 32768
	cfg.Connections[0].Capabilities.Streaming, cfg.Connections[0].Capabilities.ToolCalls = true, true
	cfg.Agents = append(cfg.Agents, config.Agent{Name: "other", B: "local", Toolset: []string{"recall"}}, config.Agent{Name: "private", B: "local", Private: true})
	manager := memory.New(data, func() config.Config { return cfg }, nil)
	bus := events.NewBus()
	writers, err := events.NewWriters(filepath.Join(data, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	bus.SetDurableSink(writers.WriteRecord, nil, nil)
	connection := func(id string) (*config.Connection, bool) { return cfg.Connection(id) }
	settings := func() config.Config { return cfg }
	registry := session.NewRegistry(bus, writers, connection, 40, settings)
	registry.SetPlansRoot(filepath.Join(data, "plans"))
	registry.SetAgentMemoryLoader(manager.LoadAgent)
	remember := tools.NewRemember(manager, bus)
	for _, item := range []struct{ agent, text string }{{cfg.DefaultAgentID(), "Use one short paragraph"}, {"private", "Use purple headings"}} {
		chat, err := registry.Create("fixture", item.agent, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := remember.Call(context.Background(), chat, map[string]any{"scope": "user", "note": item.text}); err != nil {
			t.Fatal(err)
		}
	}
	manager.SetBaseDir(filepath.Join(data, "another-profile"))
	chat, err := registry.Create("next fixture", "other", "")
	if err != nil {
		t.Fatal(err)
	}
	if block := chat.AgentMemoryBlock; !strings.Contains(block, "Use one short paragraph") || strings.Contains(block, "Use purple headings") {
		t.Fatalf("new chat memory: %q", block)
	}
	templatePath := filepath.Join(data, "prompt.txt")
	if err := os.WriteFile(templatePath, []byte("Fixture instructions.\n{{memory}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	prompt, err := agent.LoadTemplate(templatePath)
	if err != nil {
		t.Fatal(err)
	}
	chat.Append(events.Message{ID: "fixture-user", Role: "user", Content: "Describe the fixture.", Category: "history"})
	runner := agent.NewRunner(bus, tools.New(tools.NewRecall(manager, nil)), prompt, connection, settings)
	if reason, detail, _ := runner.Run(context.Background(), chat, "fixture-run"); reason != "done" {
		t.Fatalf("run reason=%q detail=%q", reason, detail)
	}
	select {
	case request := <-requests:
		if !strings.Contains(request, "Use one short paragraph") || strings.Contains(request, "Use purple headings") {
			t.Fatalf("model request has the wrong memory: %s", request)
		}
	default:
		t.Fatal("the stand-in received no request")
	}
}

// Item 2fh, the walk's step 6 without a model: what one scratch chat remembers
// "for later chats" a new chat carries, a project fact from a chat working in a
// plan's repository lands in that plan's layer, and a scratch folder never has
// a memory file of its own.
func TestMemoryCarriesAcrossScratchChatsAndProjectFactsGoToThePlan(t *testing.T) {
	root, repo := t.TempDir(), t.TempDir()
	data := filepath.Join(root, "data")
	cfg := config.Defaults(filepath.Join(data, "scratch"))
	connection := &cfg.Connections[0]
	connections := func(id string) (*config.Connection, bool) { return connection, id == connection.ID }
	writers, err := events.NewWriters(filepath.Join(data, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	bus := events.NewBus()
	bus.SetDurableSink(writers.WriteRecord, nil, nil)
	registry := session.NewRegistry(bus, writers, connections, 40, func() config.Config { return cfg })
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
	reply, err := remember.Call(context.Background(), chatA, map[string]any{"note": "the walk project's mascot is a heron named Quill", "scope": "user"})
	if err != nil || !strings.Contains(reply, "noted as user") {
		t.Fatalf("a user fact from a scratch chat goes to the agent layer, and says so: %q %v", reply, err)
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
	projectNote := filepath.Join(repo, "notes.txt")
	if written, err := chatC.WriteRoot(projectNote); err != nil || !session.SamePath(written, repo) {
		t.Fatalf("a write into the plan's repository: %q %v", written, err)
	}
	if err := os.WriteFile(projectNote, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	chatC.Touch(projectNote)
	reply, err = remember.Call(context.Background(), chatC, map[string]any{"note": "the build uses make", "scope": "repository"})
	if err != nil || strings.Contains(reply, "agent layer") {
		t.Fatalf("a project fact from a chat working in a plan's repo: %q %v", reply, err)
	}
	if notes, _ := manager.Read(chatC.MemoryFolder()); !strings.Contains(notes, "the build uses make") {
		t.Fatalf("the fact did not land in the plan's layer: reply=%q folder=%q roots=%v notes=%q", reply, chatC.MemoryFolder(), chatC.Snapshot().ProjectRoots, notes)
	}
	if agentNotes, _ := manager.ReadAgent(agentID); strings.Contains(agentNotes, "the build uses make") {
		t.Fatal("the project fact leaked into the agent layer")
	}

	// An existing chat's prompt head stays fixed even after it enters that plan.
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
	if chatE.RefreshScratchMemory() || strings.Contains(chatE.MemoryBlock, "the build uses make") {
		t.Fatalf("an existing chat changed its memory block: %q", chatE.MemoryBlock)
	}
}
