package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/config"
)

// A layer over its budget must tell the model what to do about it, and which
// layer is full. The old notice said "prune it by hand", which the model cannot
// act on and the operator never sees.
func TestOverBudgetNoticeNamesTheLayerAndAsksForConsolidation(t *testing.T) {
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

	for _, testCase := range []struct{ heading, want string }{
		{"## Agent memory", "agent memory"},
		{"## Folder memory", "folder memory"},
	} {
		block, _, err := manager.load(context.Background(), path, "p", testCase.heading)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(block, "older "+testCase.want+" notes are omitted here because the layer is over its budget") {
			t.Fatalf("notice does not name the %s layer:\n%s", testCase.want, firstLines(block, 3))
		}
		if !strings.Contains(block, "Nothing has been deleted.") {
			t.Error("notice does not say the file is intact")
		}
		if !strings.Contains(block, "consolidate the oldest notes into fewer lines with remember") {
			t.Error("notice does not ask for consolidation through the existing tool")
		}
		if !strings.Contains(block, "keeping every fact that still holds") {
			t.Error("notice does not constrain the consolidation")
		}
		if strings.Contains(block, "prune it by hand") {
			t.Error("the old un-actionable wording survived")
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

// Under budget there is no notice at all.
func TestUnderBudgetHasNoNotice(t *testing.T) {
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

func firstLines(value string, count int) string {
	parts := strings.SplitN(value, "\n", count+1)
	if len(parts) > count {
		parts = parts[:count]
	}
	return strings.Join(parts, "\n")
}
