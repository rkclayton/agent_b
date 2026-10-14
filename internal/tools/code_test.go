package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
)

func codeFixture(t *testing.T) (*Code, string) {
	t.Helper()
	root := t.TempDir()
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	lines[9] = "func Target() {}"
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	manager := NewLanguageDiagnostics(NewShell(config.Defaults(root).Shell), map[string][]string{".go": {exe, "-test.run=TestLanguageServerFixtureProcess", "--", "code"}})
	manager.timeout = time.Second
	t.Cleanup(manager.Close)
	return NewCode(manager), root
}
func TestCodeFixtureFormatsFourQuestionsAndBoundsReferences(t *testing.T) {
	tool, root := codeFixture(t)
	s := testSession(root, "code", "code")
	checks := []struct {
		args  map[string]any
		wants []string
	}{
		{map[string]any{"action": "definition", "name": "Target"}, []string{"main.go:10", "func Target() {}"}},
		{map[string]any{"action": "references", "name": "Target"}, []string{"main.go:1 line 1", "30 more"}},
		{map[string]any{"action": "outline", "path": "main.go"}, []string{"function Target main.go:10"}},
		{map[string]any{"action": "symbols", "name": "Tar"}, []string{"function Target main.go:10"}},
	}
	for _, check := range checks {
		got, err := tool.Call(context.Background(), s, check.args)
		if err != nil {
			t.Fatalf("%v: %v", check.args, err)
		}
		for _, want := range check.wants {
			if !strings.Contains(got, want) {
				t.Fatalf("%v result %q lacks %q", check.args, got, want)
			}
		}
	}
}
func TestCodeResolvesByNameAndReturnsAmbiguity(t *testing.T) {
	tool, root := codeFixture(t)
	s := testSession(root, "code", "code")
	for _, args := range []map[string]any{
		{"action": "definition", "name": "Target"},
		{"action": "definition", "name": "Target", "path": "main.go"},
		{"action": "definition", "name": "Target", "path": "main.go", "line": 10},
	} {
		got, err := tool.Call(context.Background(), s, args)
		if err != nil || !strings.Contains(got, "main.go:10") {
			t.Fatalf("%v: %q %v", args, got, err)
		}
	}
	got, err := tool.Call(context.Background(), s, map[string]any{"action": "definition", "name": "Duplicate"})
	if err != nil || !strings.Contains(got, "candidates:") || !strings.Contains(got, "main.go:10") || !strings.Contains(got, "main.go:20") || strings.Contains(got, "func Target()") {
		t.Fatalf("ambiguity=%q err=%v", got, err)
	}
}
func TestCodeMissingTimeoutBoundaryAndEntryBound(t *testing.T) {
	root := t.TempDir()
	s := testSession(root, "code", "code")
	missing := NewLanguageDiagnostics(NewShell(config.Defaults(root).Shell), map[string][]string{".go": {"not-installed-code-server"}})
	t.Cleanup(missing.Close)
	got, err := NewCode(missing).Call(context.Background(), s, map[string]any{"action": "symbols", "name": "x", "path": "x.go"})
	if err != nil || got != "code: no language server installed for .go; use search_text" {
		t.Fatalf("missing=%q err=%v", got, err)
	}
	exe, _ := os.Executable()
	silent := NewLanguageDiagnostics(NewShell(config.Defaults(root).Shell), map[string][]string{".go": {exe, "-test.run=TestLanguageServerFixtureProcess", "--", "silent"}})
	silent.timeout = 40 * time.Millisecond
	t.Cleanup(silent.Close)
	got, err = NewCode(silent).Call(context.Background(), s, map[string]any{"action": "symbols", "name": "x", "path": "x.go"})
	if err != nil || got != "code: language server did not answer in 3 s" {
		t.Fatalf("timeout=%q err=%v", got, err)
	}
	outside := filepath.Join(filepath.Dir(root), "outside.go")
	_, err = NewCode(missing).Call(context.Background(), s, map[string]any{"action": "outline", "path": outside})
	if err == nil || err.Error() != "path is outside the folder" {
		t.Fatalf("outside err=%v", err)
	}
	tool, fixtureRoot := codeFixture(t)
	got, err = tool.Call(context.Background(), testSession(fixtureRoot, "bound", "bound"), map[string]any{"action": "references", "name": "Target"})
	if err != nil || strings.Count(got, "main.go:") != 50 || !strings.HasSuffix(got, "30 more") || estimateCodeTokens(got) > 2000 {
		t.Fatalf("bounded entries=%d tokens=%d tail=%q err=%v", strings.Count(got, "main.go:"), estimateCodeTokens(got), got[max(0, len(got)-20):], err)
	}
	got, err = tool.Call(context.Background(), testSession(fixtureRoot, "outside", "outside"), map[string]any{"action": "references", "name": "Outside"})
	if err != nil || strings.Contains(got, "outside.go") {
		t.Fatalf("outside reference leaked: %q err=%v", got, err)
	}
}
func TestCodeSchemaPromptCost(t *testing.T) {
	tool := NewCode(nil)
	data, err := json.Marshal(map[string]any{"type": "function", "function": map[string]any{"name": tool.Name(), "description": tool.Description(), "parameters": tool.Schema()}})
	if err != nil {
		t.Fatal(err)
	}
	tokens := int(math.Ceil(float64(len([]rune(string(data)))) * 1.1 / 3.6))
	t.Logf("code schema and prompt line: %d estimated tokens", tokens)
	if tokens > 250 {
		t.Fatalf("cost=%d tokens", tokens)
	}
}
