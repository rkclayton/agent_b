package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/config"
)

func TestLoadReturnsEverySavedNoteWithoutPromptBudgetTrimming2vi(t *testing.T) {
	root := t.TempDir()
	cfg := func() config.Config {
		return config.Config{Memory: config.Memory{Enabled: true, Dir: root, MaxTokens: 220}}
	}
	manager := New(root, cfg, func(_ context.Context, _, body string) (int, error) { return (len([]rune(body)) + 3) / 4, nil })

	path := filepath.Join(root, "layer.md")
	lines := []string{}
	for index := 0; index < 40; index++ {
		lines = append(lines, "2026-09-15 a durable fact worth keeping number "+strings.Repeat("x", 20))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, heading := range []string{"## Agent memory", "## Folder memory"} {
		block, _, err := manager.load(context.Background(), path, "p", heading)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(block, "durable fact worth keeping") != 40 || strings.Contains(block, "omitted") {
			t.Fatalf("load did not return all notes: %s", firstLines(block, 3))
		}
	}

	// The harness still never touches the file.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(strings.Join(lines, "\n"))+1 {
		t.Fatal("loading a layer changed the file on disk")
	}
}

func TestUnderBudgetLoadsTheSameList(t *testing.T) {
	root := t.TempDir()
	cfg := func() config.Config {
		return config.Config{Memory: config.Memory{Enabled: true, Dir: root, MaxTokens: 5000}}
	}
	manager := New(root, cfg, func(_ context.Context, _, body string) (int, error) { return (len([]rune(body)) + 3) / 4, nil })
	path := filepath.Join(root, "layer.md")
	if err := os.WriteFile(path, []byte("2026-09-15 one short fact\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	block, _, err := manager.load(context.Background(), path, "p", "## Agent memory")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(block, "over its budget") {
		t.Fatalf("a layer under budget carries a notice:\n%s", block)
	}
}

func TestRemoveAgentDeletesOnlyNamedEntry2kt(t *testing.T) {
	root := t.TempDir()
	manager := New(root, func() config.Config { return config.Config{Memory: config.Memory{Enabled: true, Dir: root}} }, func(context.Context, string, string) (int, error) { return 0, nil })
	if _, _, err := manager.NoteAgent("agent-b", "ping output"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.NoteAgent("agent-b", "operator prefers concise reports"); err != nil {
		t.Fatal(err)
	}
	removed, err := manager.RemoveAgent("agent-b", "ping output")
	if err != nil || !removed {
		t.Fatalf("removed=%t err=%v", removed, err)
	}
	content, err := manager.ReadAgent("agent-b")
	if err != nil || strings.Contains(content, "ping output") || !strings.Contains(content, "operator prefers concise reports") {
		t.Fatalf("content=%q err=%v", content, err)
	}
}

func firstLines(value string, count int) string {
	parts := strings.SplitN(value, "\n", count+1)
	if len(parts) > count {
		parts = parts[:count]
	}
	return strings.Join(parts, "\n")
}
