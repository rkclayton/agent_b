package agent

import (
	"context"
	"fmt"
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

func TestFitWindowResultRequestsSmallerSameOffsetInsteadOfOverflowingContext(t *testing.T) {
	cfg := config.Defaults(t.TempDir())
	runner := &Runner{cfg: func() config.Config { return cfg }}
	profile := &cfg.Servers[0]
	metadata := map[string]any{"window_offset": 65537, "next_offset": 131073, "more": true}

	content, ok, gotMetadata, tokens := runner.fitWindowResult(
		context.Background(), nil, profile, "fetch_url",
		map[string]any{"offset": float64(65537), "limit": float64(65536)},
		strings.Repeat("x", 65536), true, metadata, 34903, 19996, false,
	)

	if ok || tokens < 1 {
		t.Fatalf("ok=%t tokens=%d content=%q", ok, tokens, content)
	}
	for _, want := range []string{"too large for the current model context", "same offset=65537", "limit no greater than 16384", "Do not advance"} {
		if !strings.Contains(content, want) {
			t.Fatalf("content %q does not contain %q", content, want)
		}
	}
	if gotMetadata["result_too_large"] != true || gotMetadata["retry_offset"] != 65537 || gotMetadata["retry_limit"] != 16384 {
		t.Fatalf("metadata = %#v", gotMetadata)
	}
	if metadata["result_too_large"] != nil {
		t.Fatalf("source metadata was mutated: %#v", metadata)
	}
}

func TestFitReadFileClampsToLargestSuccessfulWindow(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "large.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("abcdefghij", 4000)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults(root)
	registry := tools.New(tools.NewReadFile(cfg.Tools.ReadFile))
	item := &session.Session{Workspace: root, ToolsEnabled: map[string]bool{"read_file": true}, LastSeen: map[string]time.Time{}}
	runner := &Runner{cfg: func() config.Config { return cfg }, tools: registry}
	profile := &cfg.Servers[0]
	args := map[string]any{"path": "large.txt", "offset": 1, "limit": 40000}
	original, originalOK := registry.Call(context.Background(), item, "read_file", args)
	if !originalOK {
		t.Fatal(original)
	}
	originalTokens := runner.textTokens(context.Background(), profile, original)
	available := 1000
	content, ok, metadata, tokens := runner.fitWindowResult(context.Background(), item, profile, "read_file", args, original, true, nil, originalTokens, available, false)
	if !ok || tokens > available || metadata["result_clamped"] != true {
		t.Fatalf("ok=%t tokens=%d metadata=%#v content=%q", ok, tokens, metadata, content)
	}
	for _, want := range []string{"requested_bytes=40000", "remaining_bytes=", "next_offset=", "[byte window:"} {
		if !strings.Contains(content, want) {
			t.Fatalf("clamped content missing %q: %s", want, content)
		}
	}
	limit := metadata["returned_limit"].(int)
	largerArgs := cloneMetadata(args)
	largerArgs["limit"] = limit + 1
	larger, largerOK := registry.Call(context.Background(), item, "read_file", largerArgs)
	if !largerOK {
		t.Fatal(larger)
	}
	returned := headerInteger(larger, "bytes")
	remaining := max(0, headerInteger(larger, "total")-returned)
	next := headerInteger(larger, "next_offset")
	note := fmt.Sprintf("[context clamp: requested_bytes=40000 returned_bytes=%d remaining_bytes=%d next_offset=%d]\n", returned, remaining, next)
	if runner.textTokens(context.Background(), profile, note+larger) <= available {
		t.Fatalf("returned limit %d was not maximal", limit)
	}
}

func TestFitWindowResultLeavesFittingAndNonWindowResultsUnchanged(t *testing.T) {
	cfg := config.Defaults(t.TempDir())
	runner := &Runner{cfg: func() config.Config { return cfg }}
	profile := &cfg.Servers[0]

	for _, test := range []struct {
		name      string
		ok        bool
		available int
		tokens    int
	}{
		{name: "read_file", ok: true, available: 100, tokens: 100},
		{name: "shell", ok: true, available: 10, tokens: 100},
		{name: "fetch_url", ok: false, available: 10, tokens: 100},
	} {
		content, ok, _, tokens := runner.fitWindowResult(context.Background(), nil, profile, test.name, nil, "original", test.ok, nil, test.tokens, test.available, false)
		if content != "original" || ok != test.ok || tokens != test.tokens {
			t.Fatalf("%s changed: content=%q ok=%t tokens=%d", test.name, content, ok, tokens)
		}
	}
}

func TestFitWindowResultRefusesOversizedReadBatchWithBatchGuidance(t *testing.T) {
	cfg := config.Defaults(t.TempDir())
	runner := &Runner{cfg: func() config.Config { return cfg }}
	profile := &cfg.Servers[0]
	content, ok, metadata, tokens := runner.fitWindowResult(context.Background(), &session.Session{}, profile, "read_file", map[string]any{
		"path": "large.txt",
		"windows": []any{map[string]any{"offset": float64(1), "limit": float64(65536)}},
	}, strings.Repeat("x", 65536), true, nil, 32000, 1000, false)
	if ok || tokens < 1 || !strings.Contains(content, "fewer or smaller windows") || strings.Contains(content, "same offset=") {
		t.Fatalf("content=%q ok=%t tokens=%d", content, ok, tokens)
	}
	if metadata["result_too_large"] != true || metadata["retry_windows"] != "fewer_or_smaller" {
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
	profile := &cfg.Servers[0]
	args := map[string]any{"path": "index.html", "offset": 9293, "limit": 30000}
	original, ok := registry.Call(context.Background(), item, "read_file", args)
	if !ok {
		t.Fatal(original)
	}
	originalTokens := runner.textTokens(context.Background(), profile, original)
	content, _, metadata, tokens := runner.fitWindowResult(context.Background(), item, profile, "read_file", args, original, true, nil, originalTokens, 0, false)
	if content == original || tokens >= originalTokens || metadata["result_too_large"] != true {
		t.Fatalf("a read with no room left came back whole: tokens=%d of %d metadata=%#v", tokens, originalTokens, metadata)
	}
	if unknown, _, _, _ := runner.fitWindowResult(context.Background(), item, profile, "read_file", args, original, true, nil, originalTokens, -1, false); unknown != original {
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
