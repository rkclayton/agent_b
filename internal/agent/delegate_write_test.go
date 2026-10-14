package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
)

func TestWritingHelpersApplyWholeOrKeepCollision2rt(t *testing.T) {
	real, scratch := t.TempDir(), t.TempDir()
	for name, value := range map[string]string{"a.txt": "a0", "b.txt": "b0", "same.txt": "s0"} {
		if err := os.WriteFile(filepath.Join(real, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Defaults(scratch)
	cfg.Workspace, cfg.Approval.Mode = scratch, config.ApprovalModeOff
	runner := NewRunner(events.NewBus(), tools.New(), &PromptRenderer{}, func(string) (*config.Connection, bool) { return &cfg.Connections[0], true }, func() config.Config { return cfg })
	parent := &session.Session{ID: "chat", Workspace: real, Run: session.RunState{RunID: "run"}}

	first, _ := newHelperCopy(real, delegateCopyFiles, delegateCopyBytes)
	second, _ := newHelperCopy(real, delegateCopyFiles, delegateCopyBytes)
	defer first.Close()
	defer second.Close()
	_ = os.WriteFile(filepath.Join(first.Root, "a.txt"), []byte("a1"), 0o600)
	_ = os.WriteFile(filepath.Join(second.Root, "b.txt"), []byte("b1"), 0o600)
	if applied, collisions, _, err := runner.finishHelperCopy(context.Background(), parent, "one", first); err != nil || len(applied) != 1 || len(collisions) != 0 {
		t.Fatalf("first applied=%v collisions=%v err=%v", applied, collisions, err)
	}
	if applied, collisions, _, err := runner.finishHelperCopy(context.Background(), parent, "two", second); err != nil || len(applied) != 1 || len(collisions) != 0 {
		t.Fatalf("second applied=%v collisions=%v err=%v", applied, collisions, err)
	}

	winner, _ := newHelperCopy(real, delegateCopyFiles, delegateCopyBytes)
	loser, _ := newHelperCopy(real, delegateCopyFiles, delegateCopyBytes)
	defer winner.Close()
	defer loser.Close()
	_ = os.WriteFile(filepath.Join(winner.Root, "same.txt"), []byte("winner"), 0o600)
	_ = os.WriteFile(filepath.Join(loser.Root, "same.txt"), []byte("loser"), 0o600)
	if _, _, _, err := runner.finishHelperCopy(context.Background(), parent, "winner", winner); err != nil {
		t.Fatal(err)
	}
	applied, collisions, kept, err := runner.finishHelperCopy(context.Background(), parent, "loser", loser)
	if err != nil || len(applied) != 0 || strings.Join(collisions, ",") != "same.txt" {
		t.Fatalf("loser applied=%v collisions=%v kept=%q err=%v", applied, collisions, kept, err)
	}
	if data, err := os.ReadFile(filepath.Join(kept, "same.txt")); err != nil || string(data) != "loser" {
		t.Fatalf("kept collision=%q err=%v", data, err)
	}

	parentRace, _ := newHelperCopy(real, delegateCopyFiles, delegateCopyBytes)
	defer parentRace.Close()
	_ = os.WriteFile(filepath.Join(parentRace.Root, "same.txt"), []byte("helper"), 0o600)
	_ = os.WriteFile(filepath.Join(real, "same.txt"), []byte("parent"), 0o600)
	if applied, collisions, _, err := runner.finishHelperCopy(context.Background(), parent, "parent-race", parentRace); err != nil || len(applied) != 0 || len(collisions) != 1 {
		t.Fatalf("parent race applied=%v collisions=%v err=%v", applied, collisions, err)
	}
}

func TestWritingHelperBoundsCleanupAndSlots2rt(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	_ = os.WriteFile(filepath.Join(source, "one"), []byte("1"), 0o600)
	_ = os.WriteFile(filepath.Join(source, "two"), []byte("2"), 0o600)
	if err := copyTree(source, target, 1, 100, map[string]helperFile{}); err == nil || !strings.Contains(err.Error(), "copy bound") {
		t.Fatalf("oversize err=%v", err)
	}
	copy, err := newHelperCopy(source, 10, 100)
	if err != nil {
		t.Fatal(err)
	}
	copyPath := copy.Root
	copy.Close()
	if _, err := os.Stat(copyPath); !os.IsNotExist(err) {
		t.Fatalf("helper copy remains: %v", err)
	}

	cfg := config.Defaults(t.TempDir())
	cfg.Connections[0].MaxConcurrent = 1
	runner := NewRunner(events.NewBus(), tools.New(), &PromptRenderer{}, func(string) (*config.Connection, bool) { return &cfg.Connections[0], true }, func() config.Config { return cfg })
	release, err := runner.acquireDelegate(context.Background(), "chat", "run", cfg.Connections[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	go func() {
		second, err := runner.acquireDelegate(context.Background(), "chat", "run", cfg.Connections[0].ID)
		if err == nil {
			close(entered)
			second()
		}
	}()
	select {
	case <-entered:
		t.Fatal("second helper entered a one-slot connection")
	case <-time.After(30 * time.Millisecond):
	}
	release()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("waiting helper did not enter")
	}
	for index := 2; index < 8; index++ {
		done, err := runner.acquireDelegate(context.Background(), "chat", "run", cfg.Connections[0].ID)
		if err != nil {
			t.Fatalf("helper %d: %v", index+1, err)
		}
		done()
	}
	if _, err := runner.acquireDelegate(context.Background(), "chat", "run", cfg.Connections[0].ID); err == nil {
		t.Fatal("ninth helper was not refused")
	}
	cfg.Connections[0].MaxConcurrent = 4
	four := NewRunner(events.NewBus(), tools.New(), &PromptRenderer{}, func(string) (*config.Connection, bool) { return &cfg.Connections[0], true }, func() config.Config { return cfg })
	releases := make([]func(), 4)
	for index := range releases {
		releases[index], err = four.acquireDelegate(context.Background(), "chat", "four", cfg.Connections[0].ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	fifth := make(chan struct{})
	go func() {
		release, _ := four.acquireDelegate(context.Background(), "chat", "four", cfg.Connections[0].ID)
		close(fifth)
		release()
	}()
	select {
	case <-fifth:
		t.Fatal("fifth helper entered before one of four finished")
	case <-time.After(30 * time.Millisecond):
	}
	releases[0]()
	select {
	case <-fifth:
	case <-time.After(time.Second):
		t.Fatal("fifth helper did not leave the four-helper wait")
	}
	for _, release := range releases[1:] {
		release()
	}
}

func TestDelegateConnectionApprovalOnceAndUnattendedRefusal2rt(t *testing.T) {
	bus := events.NewBus()
	stream, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	cfg := config.Defaults(t.TempDir())
	second := cfg.Connections[0]
	second.ID, second.Label = "second", "Second fixture"
	cfg.Connections = append(cfg.Connections, second)
	runner := NewRunner(bus, tools.New(), &PromptRenderer{}, func(id string) (*config.Connection, bool) { return cfg.Connection(id) }, func() config.Config { return cfg })
	parent := &session.Session{ID: "chat", ConnectionID: cfg.Connections[0].ID, Run: session.RunState{RunID: "run"}}
	type answer struct {
		id, label string
		err       error
	}
	done := make(chan answer, 1)
	go func() {
		id, label, err := runner.delegateConnection(context.Background(), parent, second.Label)
		done <- answer{id, label, err}
	}()
	event := <-stream
	if event.Type != events.ApprovalRequired {
		t.Fatalf("event=%s", event.Type)
	}
	data := event.Data.(map[string]any)
	args := data["args"].(map[string]any)
	if data["name"] != "delegate connection" || args["connection"] != second.Label {
		t.Fatalf("card=%#v", data)
	}
	if err := runner.Gate().Decide(parent.ID, data["call_id"].(string), "once"); err != nil {
		t.Fatal(err)
	}
	<-stream
	if got := <-done; got.err != nil || got.id != second.ID {
		t.Fatalf("selection=%+v", got)
	}
	if _, _, err := runner.delegateConnection(context.Background(), parent, second.Label); err != nil {
		t.Fatalf("approved connection did not hold for chat: %v", err)
	}

	cfg.Approval.Unattended = true
	unattended := NewRunner(events.NewBus(), tools.New(), &PromptRenderer{}, func(id string) (*config.Connection, bool) { return cfg.Connection(id) }, func() config.Config { return cfg })
	parent.Origin = "scheduled"
	if _, _, err := unattended.delegateConnection(context.Background(), parent, second.Label); err == nil {
		t.Fatal("unattended cross-connection helper was not refused")
	}
}

func TestWritingHelperReturnIsOneUndoGroup2rt(t *testing.T) {
	real, data := t.TempDir(), t.TempDir()
	_ = os.WriteFile(filepath.Join(real, "old.txt"), []byte("before"), 0o600)
	bus := events.NewBus()
	coordinator := tools.NewFileCoordinator(session.NewWorkspaceRegistry(), func(id string) string { return id }, bus)
	undo := tools.NewFileUndoStore(data)
	coordinator.SetUndoStore(undo)
	cfg := config.Defaults(data)
	cfg.Approval.Mode = config.ApprovalModeOff
	runner := NewRunner(bus, tools.New(), &PromptRenderer{}, func(string) (*config.Connection, bool) { return &cfg.Connections[0], true }, func() config.Config { return cfg })
	runner.SetDelegateFileCoordinator(coordinator)
	parent := &session.Session{ID: "chat", Workspace: real, Run: session.RunState{RunID: "helper-return"}, LastSeen: map[string]time.Time{}}
	copy, err := newHelperCopy(real, 10, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	_ = os.WriteFile(filepath.Join(copy.Root, "old.txt"), []byte("after"), 0o600)
	_ = os.WriteFile(filepath.Join(copy.Root, "new.txt"), []byte("new"), 0o600)
	if applied, _, _, err := runner.finishHelperCopy(context.Background(), parent, "helper", copy); err != nil || len(applied) != 2 {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	result, err := undo.Undo(parent.ID, parent.Run.RunID)
	if err != nil || len(result.Restored) != 2 {
		t.Fatalf("undo=%+v err=%v", result, err)
	}
	if value, _ := os.ReadFile(filepath.Join(real, "old.txt")); string(value) != "before" {
		t.Fatalf("old=%q", value)
	}
	if _, err := os.Stat(filepath.Join(real, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("new file remains: %v", err)
	}
}

func TestDelegateUsesNamedConnection2rt(t *testing.T) {
	var parentCalls, helperCalls atomic.Int32
	parentServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { parentCalls.Add(1) }))
	defer parentServer.Close()
	helperServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		helperCalls.Add(1)
		writeStreamChunk(t, w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "helper done"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 3}})
	}))
	defer helperServer.Close()
	cfg := config.Defaults(t.TempDir())
	cfg.Context.Accounting = "estimated"
	parentConnection := cfg.Connections[0]
	parentConnection.ID, parentConnection.Label, parentConnection.BaseURL, parentConnection.Model = "parent", "Parent fixture", parentServer.URL, "fixture"
	parentConnection.Capabilities.Streaming, parentConnection.Capabilities.ToolCalls = true, true
	parentConnection.Context.NCtx, parentConnection.Context.ReserveOutput, parentConnection.Capabilities.NCtx = 32768, 4096, 32768
	helperConnection := parentConnection
	helperConnection.ID, helperConnection.Label, helperConnection.BaseURL = "helper", "Helper fixture", helperServer.URL
	cfg.Connections = []config.Connection{parentConnection, helperConnection}
	runner := NewRunner(events.NewBus(), tools.New(), &PromptRenderer{text: "parent", delegate: "helper {{tools}}"}, func(id string) (*config.Connection, bool) { return cfg.Connection(id) }, func() config.Config { return cfg })
	parent := &session.Session{ID: "chat", ConnectionID: parentConnection.ID, Workspace: t.TempDir(), Runnable: true, Run: session.RunState{RunID: "run", MaxTurns: 4}, ToolsEnabled: map[string]bool{}, ToolCalls: map[string]int{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	runner.delegateApproved[parent.ID+"\x00"+helperConnection.ID] = true
	result, err := runner.runDelegate(context.Background(), parent, tools.DelegateRequest{Task: "answer", Connection: helperConnection.Label})
	if err != nil || result.Summary != "helper done" || result.Connection != helperConnection.Label || helperCalls.Load() != 1 || parentCalls.Load() != 0 || parent.ConnectionID != parentConnection.ID {
		t.Fatalf("result=%+v err=%v calls=%d/%d parent=%s", result, err, parentCalls.Load(), helperCalls.Load(), parent.ConnectionID)
	}
}
