package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/session"
)

// Durable-memory and working-directory guidance have to be in the shipped
// prompt and byte-stable across requests within a session.
func TestShippedPromptCarriesMemoryAndResolutionGuidanceAndIsByteStable(t *testing.T) {
	shipped, err := os.ReadFile(filepath.Join("..", "..", "prompts", "system.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(shipped)

	for _, want := range []string{
		"Remember, with remember, only these: a correction the operator gave you, a preference the operator stated, or a repository fact you had to discover; recall first and never write a note that restates one you already have.",
		"Rules: relative paths start in this chat's scratch folder; resolve a named plan or repo from the operator's words and work in that repo; ask in chat when more than one plan could match; when nothing points to a repo, work in scratch.",
	} {
		if strings.Count(text, want) != 1 {
			t.Errorf("shipped prompt does not carry this sentence exactly once:\n%s", want)
		}
	}

	// The working-directory rule precedes durable-memory guidance and notes.
	resolutionAt := strings.Index(text, "Rules: relative paths start")
	rememberAt := strings.Index(text, "Remember, with remember, only these:")
	memoryAt := strings.Index(text, "{{memory}}")
	if !(resolutionAt >= 0 && resolutionAt < rememberAt && rememberAt < memoryAt) {
		t.Fatalf("sentence order is wrong: resolution=%d remember=%d memory=%d", resolutionAt, rememberAt, memoryAt)
	}

	root := t.TempDir()
	template := filepath.Join(root, "system.md")
	if err := os.WriteFile(template, shipped, 0o600); err != nil {
		t.Fatal(err)
	}
	renderer, err := LoadTemplate(template)
	if err != nil {
		t.Fatal(err)
	}
	item := &session.Session{Workspace: filepath.Join(root, "repo"), PlansRoot: filepath.Join(root, "plans")}
	connection := &config.Connection{}
	first := renderer.Render(connection, item, []string{"read_file", "remember", "recall"}, "")
	for attempt := 0; attempt < 5; attempt++ {
		if again := renderer.Render(connection, item, []string{"read_file", "remember", "recall"}, ""); again != first {
			t.Fatalf("prompt is not byte-stable across requests on attempt %d", attempt+1)
		}
	}
	for _, want := range []string{"Remember, with remember, only these:", "relative paths start in this chat's scratch folder"} {
		if !strings.Contains(first, want) {
			t.Errorf("rendered prompt lost %q", want)
		}
	}
	for _, removed := range []string{"repository allow-list", "File tools may use scratch and every listed plan repo", "offer to register it as a plan", "never search the operator's connection"} {
		if strings.Contains(first, removed) {
			t.Errorf("rendered prompt retained removed reach guidance %q", removed)
		}
	}
}

// The prompt must not contradict the tool descriptions it sits beside.
func TestMemorySentenceAgreesWithTheRememberAndRecallDescriptions(t *testing.T) {
	shipped, err := os.ReadFile(filepath.Join("..", "..", "prompts", "system.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(shipped)
	if !strings.Contains(text, "recall first") {
		t.Error("the sentence does not send the model to recall before writing, which is what the tool text already says")
	}
	if strings.Contains(text, "delete") || strings.Contains(text, "prune it by hand") {
		t.Error("the prompt implies the harness removes notes; it never does")
	}
}
