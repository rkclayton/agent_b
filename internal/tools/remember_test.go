package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/memory"
	"harness/internal/session"
)

func memoryTools(t *testing.T) (*Remember, *Recall, *session.Session, string) {
	t.Helper()
	baseDir := t.TempDir()
	workspace := filepath.Join(baseDir, "workspace")
	cfg := config.Defaults(workspace)
	cfg.Memory.Dir = filepath.Join(baseDir, "memory")
	manager := memory.New(baseDir, func() config.Config { return cfg }, nil)
	return NewRemember(manager, events.NewBus()), NewRecall(manager), &session.Session{ID: "memory-test", AgentID: "coder", Workspace: workspace}, baseDir
}

func TestRememberTargetSeparatesWorkspaceAndAgentMemory(t *testing.T) {
	remember, recall, item, _ := memoryTools(t)
	if _, err := remember.Call(context.Background(), item, map[string]any{"note": "project fact", "scope": "repository"}); err != nil {
		t.Fatal(err)
	}
	if _, err := remember.Call(context.Background(), item, map[string]any{"note": "operator preference", "scope": "user"}); err != nil {
		t.Fatal(err)
	}
	value, err := recall.Call(context.Background(), item, nil)
	if err != nil || !strings.Contains(value, "Folder memory:\n") || !strings.Contains(value, "project fact") || !strings.Contains(value, "Agent memory:\n") || !strings.Contains(value, "operator preference") {
		t.Fatalf("recall=%q err=%v", value, err)
	}
	// Item 2jf (b): scope has NO DEFAULT. A default is how the folder layer
	// filled with facts about the operator -- the model has to say what kind of
	// fact this is, and an unscoped note is refused rather than guessed at.
	properties := remember.Schema()["properties"].(map[string]any)
	scope := properties["scope"].(map[string]any)
	if _, defaulted := scope["default"]; defaulted {
		t.Fatalf("scope has a default: %#v", scope)
	}
	if required, _ := remember.Schema()["required"].([]string); len(required) != 2 {
		t.Fatalf("required=%v, want note and scope", required)
	}
}

func TestRecallReadsRememberEntryAndRememberStillDetectsDuplicate(t *testing.T) {
	remember, recall, item, _ := memoryTools(t)
	ctx := context.Background()
	result, err := remember.Call(ctx, item, map[string]any{"note": "prefer focused tests first", "scope": "user"})
	if err != nil || result != "ok: noted as user; active next session." {
		t.Fatalf("remember.Call() = %q, %v", result, err)
	}
	result, err = recall.Call(ctx, item, nil)
	if err != nil || !strings.Contains(result, " prefer focused tests first") {
		t.Fatalf("recall.Call() = %q, %v", result, err)
	}
	result, err = remember.Call(ctx, item, map[string]any{"note": "prefer focused tests first", "scope": "user"})
	if err != nil || result != "ok: already noted" {
		t.Fatalf("duplicate remember.Call() = %q, %v", result, err)
	}
}

func TestRememberRefusesRulesAboutItsOwnTools2p3(t *testing.T) {
	remember, recall, item, _ := memoryTools(t)
	result, err := remember.Call(context.Background(), item, map[string]any{"note": "do not search outside the workspace, the approval wedges", "scope": "user"})
	if err != nil || result != "error: remember refused: notes about the agent's own tools are not kept; the user decides what the agent may do" {
		t.Fatalf("refusal=%q err=%v", result, err)
	}
	if recalled, _ := recall.Call(context.Background(), item, nil); recalled != "No saved notes for this folder." {
		t.Fatalf("refused note was saved: %q", recalled)
	}
	if result, err = remember.Call(context.Background(), item, map[string]any{"note": "the user prefers tabs", "scope": "user"}); err != nil || !strings.HasPrefix(result, "ok: noted") {
		t.Fatalf("ordinary preference=%q err=%v", result, err)
	}
}

func TestRecallEmptyStoreIgnoresModelSuppliedPath(t *testing.T) {
	_, recall, item, baseDir := memoryTools(t)
	outside := filepath.Join(baseDir, "outside.md")
	if err := os.WriteFile(outside, []byte("outside secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := recall.Call(context.Background(), item, map[string]any{"path": outside})
	if err != nil || result != "No saved notes for this folder." {
		t.Fatalf("recall.Call() = %q, %v", result, err)
	}
	if strings.Contains(result, "outside secret") {
		t.Fatal("recall read model-supplied path")
	}
}

func TestRecallSchemaExposesNoPath(t *testing.T) {
	_, recall, _, _ := memoryTools(t)
	properties, ok := recall.Schema()["properties"].(map[string]any)
	if !ok || len(properties) != 0 {
		t.Fatalf("recall properties = %#v, want none", recall.Schema()["properties"])
	}
}

func TestRememberToolsBlockByteDelta(t *testing.T) {
	legacy := map[string]any{"type": "function", "function": map[string]any{
		"name": "remember", "description": "Save note as durable workspace memory for future sessions. Call recall first to avoid duplicates; unlike recall, remember writes.",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{"note": map[string]any{"type": "string"}}, "required": []string{"note"}},
	}}
	current := map[string]any{"type": "function", "function": map[string]any{
		"name": "remember", "description": (&Remember{}).Description(), "parameters": (&Remember{}).Schema(),
	}}
	before, err := json.Marshal([]any{legacy})
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal([]any{current})
	if err != nil {
		t.Fatal(err)
	}
	// Item 2kt: the description states the durable/transient boundary before a
	// call and names the cost of falling through to the agent layer. Item 2jf
	// rewrote it to carry scope, replaces and the per-run cap, which is why the
	// number moved -- and the pin is why the move had to be deliberate. 2pz: 431 to 427.
	const wantDelta = 427
	if delta := len(after) - len(before); delta != wantDelta {
		t.Fatalf("remember tools-block byte delta=%d, want %d", delta, wantDelta)
	}
	t.Logf("remember tools-block byte delta: +%d (%d to %d)", wantDelta, len(before), len(after))
}

// Item 2jf (c): TWO WRITES PER RUN, enforced by the tool rather than asked for
// in the prompt. A rule the prompt asks for is a rule the model may follow; a
// rule the tool enforces is a rule.
func TestTwoNotesPerRunAndTheThirdIsRefused2jf(t *testing.T) {
	remember, _, item, _ := memoryTools(t)
	item.Run.LastRunID = "r1"
	ctx := context.Background()
	for index, note := range []string{"the user prefers focused tests", "the user reads reports in full"} {
		result, err := remember.Call(ctx, item, map[string]any{"note": note, "scope": "user"})
		if err != nil || !strings.HasPrefix(result, "ok: noted as user") {
			t.Fatalf("note %d: %q %v", index+1, result, err)
		}
	}
	third, err := remember.Call(ctx, item, map[string]any{"note": "a third fact", "scope": "user"})
	if err != nil {
		t.Fatalf("the third note errored rather than refusing: %v", err)
	}
	// The refusal says what to do instead, because a bare refusal teaches the
	// model nothing.
	for _, want := range []string{"2 notes this run", "replace one or skip"} {
		if !strings.Contains(third, want) {
			t.Errorf("the refusal does not say %q: %q", want, third)
		}
	}
	// A NEW RUN gets its own two. The cap is per run, not per session — a long
	// chat is not punished for a previous run's writes.
	item.Run.LastRunID = "r2"
	fourth, err := remember.Call(ctx, item, map[string]any{"note": "a fact in the next run", "scope": "user"})
	if err != nil || !strings.HasPrefix(fourth, "ok: noted as user") {
		t.Fatalf("a new run did not get its own allowance: %q %v", fourth, err)
	}
}

// (b): the scope decides the layer, and an unscoped note is refused rather than
// routed by a default. A default is how the folder layer filled up with facts
// about the operator.
func TestScopeDecidesTheLayerAndIsRequired2jf(t *testing.T) {
	remember, _, item, _ := memoryTools(t)
	ctx := context.Background()
	if result, _ := remember.Call(ctx, item, map[string]any{"note": "unscoped"}); !strings.Contains(result, "scope is required") {
		t.Errorf("an unscoped note was accepted: %q", result)
	}
	if result, _ := remember.Call(ctx, item, map[string]any{"note": "bad scope", "scope": "folder"}); !strings.Contains(result, "not one of user, repository or environment") {
		t.Errorf("the old vocabulary was accepted: %q", result)
	}
	// user and environment follow the agent; repository follows the folder.
	for scope, layer := range map[string]string{"user": "agent", "environment": "agent", "repository": "folder"} {
		if got := layerFor(scope); got != layer {
			t.Errorf("scope %q routes to %q, want %q", scope, got, layer)
		}
	}
}
