package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	workspaceinfo "harness/internal/workspace"
)

// The run loop re-binds a session to its agent on every turn. It must re-bind
// to the connection that role runs on: a worker created on the c connection was
// switched to b on its first turn, so the real Go ran on the wrong model.
func TestApplyAgentConfigKeepsEachRoleOnItsOwnConnection(t *testing.T) {
	agent := config.Agent{Name: "Worker", B: "fast", C: "strong", D: "planner", Toolset: []string{"read_file"}}
	for _, row := range []struct{ role, want string }{{"b", "fast"}, {"c", "strong"}, {"d", "planner"}} {
		item := &Session{ID: "s1", Role: row.role, ToolsEnabled: map[string]bool{}, LastSeen: map[string]time.Time{}}
		item.ApplyAgentConfig("worker", agent, config.Connection{ID: row.want, Label: row.want})
		if got := item.Snapshot().ConnectionID; got != row.want {
			t.Errorf("role %q bound to %q, want %q", row.role, got, row.want)
		}
	}
	// Without a c connection the worker falls back to b, so Go works on a
	// single-connection install.
	single := config.Agent{Name: "Solo", B: "fast", Toolset: []string{"read_file"}}
	item := &Session{ID: "s2", Role: "c", ToolsEnabled: map[string]bool{}, LastSeen: map[string]time.Time{}}
	item.ApplyAgentConfig("solo", single, config.Connection{ID: "fast", Label: "fast"})
	if got := item.Snapshot().ConnectionID; got != "fast" {
		t.Errorf("a worker with no c connection bound to %q, want the b connection", got)
	}
}

func TestNetworkBoundaryFollowsTheToolIdentity(t *testing.T) {
	cfg := config.Defaults(t.TempDir())
	cfg.Shell.ServiceAccount.Enabled = false
	if got := NetworkBoundary(cfg); got != "" {
		t.Fatalf("split-off boundary=%q", got)
	}
	cfg.Shell.ServiceAccount.Enabled = true
	got := NetworkBoundary(cfg)
	if !strings.HasPrefix(got, "Network boundary: service-context shell may reach loopback and the configured model server") {
		t.Fatalf("split-on boundary=%q", got)
	}
}

func TestNetworkBoundaryStaysStableUntilReopen(t *testing.T) {
	item := &Session{NetworkBoundary: "Network boundary: captured", NetworkBoundarySet: true}
	if !item.MarkNetworkBoundaryStale("") {
		t.Fatal("split change did not add a degraded note")
	}
	got := item.Snapshot()
	if got.NetworkBoundary != "Network boundary: captured" || len(got.DegradedNotes) != 1 || got.DegradedNotes[0] != staleNetworkBoundaryNote {
		t.Fatalf("stale session=%+v", got)
	}
	item.ReopenNetworkBoundary("")
	got = item.Snapshot()
	if got.NetworkBoundary != "" || !got.NetworkBoundarySet || len(got.DegradedNotes) != 0 {
		t.Fatalf("reopened session=%+v", got)
	}
}

func TestSnapshotCarriesToolCallCounts(t *testing.T) {
	s := &Session{
		ToolsEnabled:   map[string]bool{"read_file": true},
		ToolCalls:      map[string]int{},
		SchemaTokens:   map[string]int{"read_file": 42},
		MarginalTokens: map[string]int{"read_file": 17},
	}
	s.IncrementToolCall("read_file")
	s.IncrementToolCall("read_file")
	s.IncrementToolCall("unknown")
	snapshot := s.Snapshot()
	if len(snapshot.Tools) != 1 || snapshot.Tools[0].Calls != 2 || snapshot.Tools[0].SchemaTokens != 42 || snapshot.Tools[0].MarginalTokens != 17 {
		t.Fatalf("snapshot tools=%+v", snapshot.Tools)
	}
}

func TestCreateLikePreservesToolsetButCreatesFreshScratch(t *testing.T) {
	logs, data, source := t.TempDir(), t.TempDir(), t.TempDir()
	writers, err := events.NewWriters(logs)
	if err != nil {
		t.Fatal(err)
	}
	defer writers.Close()
	connection := &config.Connection{ID: "main", Label: "Coder", Context: config.Context{NCtx: 32768, ReserveOutput: 8192}, Capabilities: config.Capabilities{Streaming: true, ToolCalls: true, OverflowBehavior: "error"}}
	cfg := config.Defaults(source)
	registry := NewRegistry(events.NewBus(), writers, func(id string) (*config.Connection, bool) { return connection, id == connection.ID }, 40, func() config.Config { return cfg })
	registry.SetPlansRoot(filepath.Join(data, "plans"))
	manager := workspaceinfo.New(data, func(dir string) string { return filepath.Join(data, filepath.Base(dir)+".md") })
	registry.SetWorkspaceManager(manager)
	first, err := registry.Create("", connection.ID, source)
	if err != nil {
		t.Fatal(err)
	}
	first.ToggleTool("shell", true)
	second, err := registry.CreateLike(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := second.Snapshot()
	if !snapshot.Scratch || snapshot.WorkspaceDir == first.Snapshot().WorkspaceDir || !strings.HasPrefix(snapshot.WorkspaceDir, filepath.Join(data, "scratch")) {
		t.Fatalf("scratch snapshot=%+v", snapshot)
	}
	if !second.EnabledTools()["shell"] {
		t.Fatal("source toolset was not preserved")
	}
}

func TestSnapshotCarriesRunAggregates(t *testing.T) {
	s := &Session{}
	s.RecordModelTurn()
	s.RecordModelTurn()
	s.RecordCompaction(-123)
	s.RecordCompactionModel(400, 50)
	snapshot := s.Snapshot()
	if snapshot.ModelTurns != 2 || snapshot.CompactionCount != 1 || snapshot.CompactionTokenDelta != -123 || snapshot.CompactionModelCalls != 1 || snapshot.CompactionPrompt != 400 || snapshot.CompactionCompletion != 50 {
		t.Fatalf("snapshot aggregates=%+v", snapshot)
	}
}

func TestToolsetFreezesAfterFirstModelTurn(t *testing.T) {
	s := &Session{ToolsEnabled: map[string]bool{"shell": false}}
	s.RecordModelTurn()
	if s.ToggleTool("shell", true) || s.ToolEnabled("shell") {
		t.Fatal("an established chat changed its tool schema")
	}
}

func TestCloseIsDurableMetadataAndNeverStopsARun(t *testing.T) {
	running := &Session{Run: RunState{Status: "running"}}
	if err := running.Close(); err == nil || !strings.Contains(err.Error(), "stop the run before closing") || running.IsClosed() {
		t.Fatalf("running close err=%v closed=%t", err, running.IsClosed())
	}
	held := &Session{Run: RunState{Status: "held"}}
	if err := held.Close(); err == nil || held.IsClosed() {
		t.Fatalf("held close err=%v closed=%t", err, held.IsClosed())
	}
	idle := &Session{Run: RunState{Status: "idle"}}
	if err := idle.Close(); err != nil || !idle.Snapshot().Closed {
		t.Fatalf("idle close err=%v snapshot=%+v", err, idle.Snapshot())
	}
}

func TestRenameAuthorsPinUserAndLeaveAuxUnpinned(t *testing.T) {
	logs := t.TempDir()
	writers, err := events.NewWriters(logs)
	if err != nil {
		t.Fatal(err)
	}
	defer writers.Close()
	bus := events.NewBus()
	connection := &config.Connection{ID: "main", Label: "Coder", Context: config.Context{NCtx: 32768, ReserveOutput: 8192}, Capabilities: config.Capabilities{Streaming: true, ToolCalls: true, OverflowBehavior: "error"}}
	cfg := config.Defaults(t.TempDir())
	registry := NewRegistry(bus, writers, func(id string) (*config.Connection, bool) { return connection, id == connection.ID }, 40, func() config.Config { return cfg })
	registry.SetPlansRoot(filepath.Join(t.TempDir(), "plans"))
	item, err := registry.Create("main", connection.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	eventsOut, cancel := bus.Subscribe()
	defer cancel()
	if err := registry.RenameBy(item.ID, "C suggestion", "c"); err != nil {
		t.Fatal(err)
	}
	aux := <-eventsOut
	if aux.Type != events.SessionRenamed || aux.Data.(map[string]any)["by"] != "c" || item.Snapshot().NamePinned {
		t.Fatalf("aux event=%+v snapshot=%+v", aux, item.Snapshot())
	}
	if err := registry.Rename(item.ID, "User title"); err != nil {
		t.Fatal(err)
	}
	user := <-eventsOut
	if user.Data.(map[string]any)["by"] != "user" || !item.Snapshot().NamePinned {
		t.Fatalf("user event=%+v", user)
	}
	if err := registry.RenameBy(item.ID, "Ignored", "c"); err != nil {
		t.Fatal(err)
	}
	if item.Snapshot().Label != "User title" {
		t.Fatalf("pin lost: %+v", item.Snapshot())
	}
	if err := registry.Rename(item.ID, "null"); err == nil || item.Snapshot().Label != "User title" {
		t.Fatalf("literal null rename err=%v label=%q", err, item.Snapshot().Label)
	}
	if err := registry.Close(item.ID); err != nil {
		t.Fatal(err)
	}
	if err := registry.Rename(item.ID, "Closed title"); err != nil {
		t.Fatal(err)
	}
	if item.Snapshot().Label != "Closed title" {
		t.Fatalf("closed rename=%+v", item.Snapshot())
	}
}

func TestCreateLikeKeepsConnectionWorkspaceAndExactToolsetAfterClose(t *testing.T) {
	logs := t.TempDir()
	writers, err := events.NewWriters(logs)
	if err != nil {
		t.Fatal(err)
	}
	defer writers.Close()
	bus := events.NewBus()
	connection := &config.Connection{ID: "main", Label: "Coder", Context: config.Context{NCtx: 32768, ReserveOutput: 8192}, Capabilities: config.Capabilities{Streaming: true, ToolCalls: true, OverflowBehavior: "error"}}
	cfg := config.Config{Context: config.GlobalContext{Accounting: "estimated"}}
	registry := NewRegistry(bus, writers, func(id string) (*config.Connection, bool) { return connection, id == connection.ID }, 40, func() config.Config { return cfg })
	registry.SetPlansRoot(filepath.Join(t.TempDir(), "plans"))
	first, err := registry.Create("", connection.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first.ToggleTool("shell", false)
	if err := registry.Close(first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := registry.CreateLike(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	want, got := first.Snapshot(), second.Snapshot()
	// By name, not by index: item 13 (v1.2.5) merged two tools into one, and a
	// position is not what this is about.
	shellEnabled := false
	for _, tool := range got.Tools {
		if tool.Name == "shell" {
			shellEnabled = tool.Enabled
		}
	}
	if got.AgentID != "coder" || got.ConnectionID != want.ConnectionID || !got.Scratch || got.Workspace == want.Workspace || got.AgentName != "Coder" || got.BConnection != "Coder" || shellEnabled {
		t.Fatalf("cloned session=%+v", got)
	}
	if len(registry.List()) != 2 || !registry.List()[0].Snapshot().Closed {
		t.Fatalf("closed session was discarded: %+v", registry.List())
	}
}

func TestRoleAndPlanSnapshotRestoreWithoutLegacyMigration(t *testing.T) {
	logs := t.TempDir()
	writers, err := events.NewWriters(logs)
	if err != nil {
		t.Fatal(err)
	}
	defer writers.Close()
	connection := &config.Connection{ID: "planner", Label: "Planner", Context: config.Context{NCtx: 32768, ReserveOutput: 8192}, Capabilities: config.Capabilities{Streaming: true, ToolCalls: true, OverflowBehavior: "error"}}
	cfg := config.Config{Context: config.GlobalContext{Accounting: "estimated"}, Agents: []config.Agent{{Name: "Agent", B: "planner", D: "planner", Toolset: config.FullToolset()}}}
	registry := NewRegistry(events.NewBus(), writers, func(id string) (*config.Connection, bool) { return connection, id == connection.ID }, 40, func() config.Config { return cfg })
	plans := t.TempDir()
	registry.SetPlansRoot(plans)
	legacy, err := registry.Create("legacy", "agent", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	legacySaved := legacy.Snapshot()
	legacySaved.Role = ""
	legacySaved.ID = "legacy-restored"
	if restored, err := registry.Restore(legacySaved); err != nil || restored.Snapshot().Role != "b" || restored.Snapshot().PlanID != "" {
		t.Fatalf("legacy restore=%+v err=%v", restored, err)
	}
	planID := "stable-plan"
	planDir := filepath.Join(plans, planID)
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "plan.md"), []byte("# Stable name\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.CreateRole("invalid", "agent", legacy.Workspace, "d", "."); err == nil || !strings.Contains(err.Error(), "invalid plan id") {
		t.Fatalf("invalid plan id error=%v", err)
	}
	d, err := registry.CreateRole("design", "agent", legacy.Workspace, "d", planID)
	if err != nil {
		t.Fatal(err)
	}
	saved := d.Snapshot()
	saved.ID = "design-restored"
	restored, err := registry.Restore(saved)
	if err != nil {
		t.Fatal(err)
	}
	got := restored.Snapshot()
	if got.Role != "d" || got.PlanID != planID || got.PlanName != "Stable name" || got.PlanDir != planDir {
		t.Fatalf("d restore=%+v", got)
	}
	if restored.ToggleTool("shell", true) || restored.ToolEnabled("shell") {
		t.Fatal("d session enabled shell outside its file jail")
	}
	d.ToolsEnabled["shell"] = true
	d.ApplyAgentConfig("agent", cfg.Agents[0], *connection)
	if d.ConnectionID != cfg.Agents[0].D || d.ToolEnabled("shell") || d.ToolEnabled("run_script") {
		t.Fatalf("agent rebind widened d session: %+v", d.Snapshot())
	}
}

func TestRestoreKeepsThreeChatBindingsAndFallsBackOnlyWhenEmpty(t *testing.T) {
	logs := t.TempDir()
	writers, err := events.NewWriters(logs)
	if err != nil {
		t.Fatal(err)
	}
	defer writers.Close()
	connections := map[string]*config.Connection{
		"server": {ID: "server", Label: "Slumberland", Model: "main", Context: config.Context{NCtx: 32768}},
		"local":  {ID: "local", Label: "Local", Model: "local", Context: config.Context{NCtx: 32768}},
	}
	cfg := config.Config{Context: config.GlobalContext{Accounting: "estimated"}, Agents: []config.Agent{{Name: "Agent", B: "server", Toolset: config.FullToolset()}}}
	registry := NewRegistry(events.NewBus(), writers, func(id string) (*config.Connection, bool) { value, ok := connections[id]; return value, ok }, 40, func() config.Config { return cfg })
	created := time.Now().UTC().Format(time.RFC3339Nano)
	for _, row := range []struct{ id, saved, want string }{{"s13", "server", "server"}, {"s15", "local", "local"}, {"s16", "", "server"}} {
		saved := Snapshot{ID: row.id, AgentID: "agent", ConnectionID: row.saved, Role: "b", CreatedAt: created, Workspace: t.TempDir(), Runnable: true, Run: RunState{Status: "idle"}}
		restored, restoreErr := registry.Restore(saved)
		if restoreErr != nil {
			t.Fatalf("restore %s: %v", row.id, restoreErr)
		}
		got := restored.Snapshot()
		if got.ConnectionID != row.want || !got.Runnable || got.NotRunnableReason != "" {
			t.Fatalf("restore %s = connection %q runnable=%v reason=%q", row.id, got.ConnectionID, got.Runnable, got.NotRunnableReason)
		}
	}
}

func TestRestoreUnknownConnectionIsNeverRunnable(t *testing.T) {
	writers, err := events.NewWriters(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer writers.Close()
	cfg := config.Config{Context: config.GlobalContext{Accounting: "estimated"}, Agents: []config.Agent{{Name: "Agent", B: "server", Toolset: config.FullToolset()}}}
	registry := NewRegistry(events.NewBus(), writers, func(string) (*config.Connection, bool) { return nil, false }, 40, func() config.Config { return cfg })
	saved := Snapshot{ID: "missing", AgentID: "agent", ConnectionID: "retired", Role: "b", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workspace: t.TempDir(), Runnable: true, Run: RunState{Status: "idle"}}
	restored, err := registry.Restore(saved)
	if err != nil {
		t.Fatal(err)
	}
	got := restored.Snapshot()
	if got.Runnable || got.NotRunnableReason != "connection retired no longer exists" {
		t.Fatalf("unknown connection restore = runnable=%v reason=%q", got.Runnable, got.NotRunnableReason)
	}
}
