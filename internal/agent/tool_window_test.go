package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
)

// 2q9 CHECK 1: s53's one-turn shape reaches another model answer instead of
// ending at context_exhausted. The fixed prompt includes 7,000 user tokens.
func TestSeveralLargeResultsShareTheRoomAndTheRunContinues2q9(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeStreamChunk(t, w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "continued"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 23000, "completion_tokens": 2}})
	}))
	defer model.Close()
	root := t.TempDir()
	cfg := config.Defaults(root)
	cfg.Context.Accounting = "estimated"
	connection := cfg.Connections[0]
	connection.ID, connection.BaseURL, connection.Model = "main", model.URL, "fake"
	connection.Context.NCtx, connection.Context.ReserveOutput = 32768, 8192
	connection.Capabilities.Streaming, connection.Capabilities.ToolCalls = true, true
	runner := NewRunner(events.NewBus(), tools.New(), &PromptRenderer{text: strings.Repeat("s", 18000)}, func(string) (*config.Connection, bool) { return &connection, true }, func() config.Config { return cfg })
	item := &session.Session{ID: "s53", ConnectionID: "main", Workspace: root, Scratch: true, Runnable: true, Run: session.RunState{Status: "running", MaxTurns: 3}, ToolsEnabled: map[string]bool{}, ToolCalls: map[string]int{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	item.Append(events.Message{ID: "u", Role: "user", Category: "history", Content: strings.Repeat("p", 25200), Tokens: 7000})
	item.Append(events.Message{ID: "a", Role: "assistant", Category: "history", ToolCalls: []events.ToolCall{{ID: "one", Name: "run_script"}, {ID: "two", Name: "run_script"}, {ID: "web", Name: "fetch_url"}}})
	remaining := 32768 - 8192 - 12000 - toolResultContextMargin
	for index, result := range []struct{ name, body string }{{"run_script", strings.Repeat("a", 32648)}, {"run_script", strings.Repeat("b", 38034)}, {"fetch_url", strings.Repeat("c", 40000)}} {
		allowance := remaining / (3 - index)
		content, ok, metadata, tokens := runner.fitWindowResult(context.Background(), item, &connection, result.name, nil, result.body, true, nil, runner.textTokens(context.Background(), &connection, result.body), allowance, false)
		if !ok || metadata["saved_path"] == nil {
			t.Fatalf("%s was not cut and saved: ok=%t metadata=%v", result.name, ok, metadata)
		}
		remaining -= tokens
		item.Append(events.Message{ID: fmt.Sprintf("t%d", index), Role: "tool", Category: "results", ToolCallID: []string{"one", "two", "web"}[index], Name: result.name, OK: &ok, Content: content, Tokens: tokens})
	}
	reason, detail, _ := runner.Run(context.Background(), item, "r1319")
	if reason != "done" || !strings.Contains(item.MessagesCopy()[len(item.MessagesCopy())-1].Content, "continued") {
		t.Fatalf("run stopped: %s %s", reason, detail)
	}
}

// 2q9 CHECK 2: a cut result keeps its full bytes in the chat and names them.
func TestHugeScriptResultIsCutSavedAndReadable2q9(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	runner := &Runner{cfg: func() config.Config { return cfg }}
	item := &session.Session{ID: "chat", Workspace: root, Scratch: true, ToolsEnabled: map[string]bool{"read_file": true}, LastSeen: map[string]time.Time{}}
	body := strings.Repeat("0123456789", 20000)
	content, ok, metadata, tokens := runner.fitWindowResult(context.Background(), item, &cfg.Connections[0], "run_script", nil, body, true, nil, runner.textTokens(context.Background(), &cfg.Connections[0], body), 1000, false)
	path, _ := metadata["saved_path"].(string)
	if !ok || tokens > 1000 || path == "" || !strings.Contains(content, "200000 bytes") || !strings.Contains(content, path) {
		t.Fatalf("content_head=%q ok=%t tokens=%d metadata=%v", content[:min(200, len(content))], ok, tokens, metadata)
	}
	window, err := tools.NewReadFile(cfg.Tools.ReadFile).Call(context.Background(), item, map[string]any{"path": path, "offset": 100001, "limit": 20})
	if err != nil || !strings.Contains(window, "01234567890123456789") {
		t.Fatalf("middle window: %v %q", err, window)
	}
	if files := workspaceFileSnapshot(root); len(files) != 0 {
		t.Fatalf("saved result entered delivery set: %v", files)
	}
}

// 2q9 CHECK 3 and CHECK 4: fetch uses the same cut, while fitting bytes do not change.
func TestFetchCutsInsteadOfRefusingAndFittingResultIsIdentical2q9(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	runner := &Runner{cfg: func() config.Config { return cfg }}
	item := &session.Session{ID: "chat", Workspace: root, Scratch: true}
	large := strings.Repeat("f", 40000)
	cut, ok, metadata, tokens := runner.fitWindowResult(context.Background(), item, &cfg.Connections[0], "fetch_url", nil, large, true, nil, runner.textTokens(context.Background(), &cfg.Connections[0], large), 1000, false)
	if !ok || tokens > 1000 || metadata["saved_path"] == nil || strings.HasPrefix(cut, "error:") {
		t.Fatalf("cut=%q ok=%t tokens=%d metadata=%v", cut, ok, tokens, metadata)
	}
	const exact = "byte-identical result"
	got, gotOK, gotMetadata, gotTokens := runner.fitWindowResult(context.Background(), item, &cfg.Connections[0], "shell", nil, exact, true, map[string]any{"same": true}, 7, 7, false)
	if got != exact || !gotOK || gotTokens != 7 || gotMetadata["same"] != true {
		t.Fatalf("fit changed: %q %t %d %v", got, gotOK, gotTokens, gotMetadata)
	}
	if !savedToolResultRead(map[string]any{"path": metadata["saved_path"]}) {
		t.Fatal("a saved result re-read was not classified as untrusted")
	}
}

func TestLastResortCutsTheNewestCurrentResultToItsSavedLine2q9(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	runner := &Runner{cfg: func() config.Config { return cfg }}
	item := &session.Session{Workspace: root}
	item.ReplaceMessages([]events.Message{{ID: "user", Role: "user", Content: "task"}, {ID: "old", Role: "tool", Content: strings.Repeat("a", 20000)}, {ID: "new", Role: "tool", Content: strings.Repeat("b", 20000)}})
	item.SetRunPin("user")
	if !runner.cutNewestRunningResult(context.Background(), item, &cfg.Connections[0]) {
		t.Fatal("newest result was not cut")
	}
	messages := item.MessagesCopy()
	if len(messages[1].Content) != 20000 || !strings.Contains(messages[2].Content, "full output saved") || len(messages[2].Content) > 300 {
		t.Fatalf("old=%d newest=%q", len(messages[1].Content), messages[2].Content)
	}
}

func TestSavedToolResultsHoldTwoFullSegments2q9(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, savedToolResultDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(dir, "results-1.txt")
	if err := os.WriteFile(first, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(first, savedToolResultSegmentBytes); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{}
	if _, _, err := runner.saveToolResult(&session.Session{Workspace: root}, strings.Repeat("z", savedToolResultSegmentBytes)); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	var bytes int64
	for _, entry := range entries {
		info, _ := entry.Info()
		bytes += info.Size()
	}
	if len(entries) != 2 || bytes != 2*savedToolResultSegmentBytes {
		t.Fatalf("segments=%d bytes=%d", len(entries), bytes)
	}
	t.Logf("full bound: %d segments, %d bytes", len(entries), bytes)
}

func TestFitWindowResultSavesFetchInsteadOfRequestingARetry(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	runner := &Runner{cfg: func() config.Config { return cfg }}
	connection := &cfg.Connections[0]
	metadata := map[string]any{"window_offset": 65537, "next_offset": 131073, "more": true}
	content, ok, gotMetadata, tokens := runner.fitWindowResult(
		context.Background(), &session.Session{Workspace: root}, connection, "fetch_url",
		map[string]any{"offset": float64(65537), "limit": float64(65536)},
		strings.Repeat("x", 65536), true, metadata, 34903, 19996, false,
	)
	if !ok || tokens > 19996 || gotMetadata["saved_path"] == nil || !strings.Contains(content, "full output saved") {
		t.Fatalf("ok=%t tokens=%d metadata=%v", ok, tokens, gotMetadata)
	}
	if metadata["result_too_large"] != nil {
		t.Fatalf("source metadata was mutated: %#v", metadata)
	}
}

func TestFitReadFileUsesTheSharedSavedResultRule(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "large.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("abcdefghij", 4000)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults(root)
	registry := tools.New(tools.NewReadFile(cfg.Tools.ReadFile))
	item := &session.Session{Workspace: root, ToolsEnabled: map[string]bool{"read_file": true}, LastSeen: map[string]time.Time{}}
	runner := &Runner{cfg: func() config.Config { return cfg }, tools: registry}
	connection := &cfg.Connections[0]
	args := map[string]any{"path": "large.txt", "offset": 1, "limit": 40000}
	original, originalOK := registry.Call(context.Background(), item, "read_file", args)
	if !originalOK {
		t.Fatal(original)
	}
	originalTokens := runner.textTokens(context.Background(), connection, original)
	available := 1000
	content, ok, metadata, tokens := runner.fitWindowResult(context.Background(), item, connection, "read_file", args, original, true, nil, originalTokens, available, false)
	if !ok || tokens > available || metadata["saved_path"] == nil || !strings.Contains(content, "full output saved") {
		t.Fatalf("ok=%t tokens=%d metadata=%#v", ok, tokens, metadata)
	}
}

func TestFitWindowResultLeavesFittingAndNonWindowResultsUnchanged(t *testing.T) {
	cfg := config.Defaults(t.TempDir())
	runner := &Runner{cfg: func() config.Config { return cfg }}
	connection := &cfg.Connections[0]

	for _, test := range []struct {
		name      string
		ok        bool
		available int
		tokens    int
	}{
		{name: "read_file", ok: true, available: 100, tokens: 100},
		{name: "shell", ok: true, available: 100, tokens: 10},
		{name: "fetch_url", ok: false, available: 100, tokens: 10},
	} {
		content, ok, _, tokens := runner.fitWindowResult(context.Background(), nil, connection, test.name, nil, "original", test.ok, nil, test.tokens, test.available, false)
		if content != "original" || ok != test.ok || tokens != test.tokens {
			t.Fatalf("%s changed: content=%q ok=%t tokens=%d", test.name, content, ok, tokens)
		}
	}
}

func TestFitWindowResultSavesAnOversizedReadBatch(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	runner := &Runner{cfg: func() config.Config { return cfg }}
	connection := &cfg.Connections[0]
	content, ok, metadata, tokens := runner.fitWindowResult(context.Background(), &session.Session{Workspace: root}, connection, "read_file", map[string]any{
		"path":    "large.txt",
		"windows": []any{map[string]any{"offset": float64(1), "limit": float64(65536)}},
	}, strings.Repeat("x", 65536), true, nil, 32000, 1000, false)
	if !ok || tokens > 1000 || !strings.Contains(content, "full output saved") {
		t.Fatalf("ok=%t tokens=%d metadata=%v", ok, tokens, metadata)
	}
	if metadata["result_too_large"] != true || metadata["saved_path"] == nil {
		t.Fatalf("metadata=%#v", metadata)
	}
}

// Item 2et: on the s7 tape a 30,000-byte read came back whole because the
// turn's remaining room had reached 0 and zero was treated as "no limit".
// Zero room is zero room; only an unknown window (-1) passes a result through.
func TestFitReadFileWithNoRoomLeftIsNotReturnedWhole(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(strings.Repeat("<div>x</div>", 5000)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults(root)
	registry := tools.New(tools.NewReadFile(cfg.Tools.ReadFile))
	item := &session.Session{Workspace: root, ToolsEnabled: map[string]bool{"read_file": true}, LastSeen: map[string]time.Time{}}
	runner := &Runner{cfg: func() config.Config { return cfg }, tools: registry}
	connection := &cfg.Connections[0]
	args := map[string]any{"path": "index.html", "offset": 9293, "limit": 30000}
	original, ok := registry.Call(context.Background(), item, "read_file", args)
	if !ok {
		t.Fatal(original)
	}
	originalTokens := runner.textTokens(context.Background(), connection, original)
	content, _, metadata, tokens := runner.fitWindowResult(context.Background(), item, connection, "read_file", args, original, true, nil, originalTokens, 0, false)
	if content == original || tokens >= originalTokens || metadata["result_too_large"] != true {
		t.Fatalf("a read with no room left came back whole: tokens=%d of %d metadata=%#v", tokens, originalTokens, metadata)
	}
	if unknown, _, _, _ := runner.fitWindowResult(context.Background(), item, connection, "read_file", args, original, true, nil, originalTokens, -1, false); unknown != original {
		t.Fatal("an unknown window must pass the result through")
	}
}

func TestContextExhaustedNamesTheReadsItKept(t *testing.T) {
	item := &session.Session{ID: "s7"}
	item.ReplaceMessages([]events.Message{
		{ID: "m-1", Role: "assistant", ToolCalls: []events.ToolCall{{ID: "a", Name: "read_file", Arguments: `{"path":"C:/work/rpg/game.js"}`}, {ID: "b", Name: "read_file", Arguments: `{"path":"map.js"}`}}},
		{ID: "m-2", Role: "tool", Name: "read_file", ToolCallID: "a", Content: "bytes"},
		{ID: "m-3", Role: "tool", Name: "read_file", ToolCallID: "b", Content: "[elided: read_file map.js, 900 tokens]", Elided: true},
	})
	if got := keptReadsSentence(item); got != "; reads kept verbatim: game.js" {
		t.Fatalf("sentence=%q", got)
	}
}
