package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/session"
)

// The two sentences 2eb adds are guidance the model needs every turn, so they
// have to be in the shipped prompt and they have to be byte-stable across
// requests within a session.
func TestShippedPromptCarriesBothMemorySentencesAndIsByteStable(t *testing.T) {
	shipped, err := os.ReadFile(filepath.Join("..", "..", "prompts", "system.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(shipped)

	for _, want := range []string{
		"Remember, with remember, only these: a correction the operator gave you, a preference the operator stated, or a repository fact you had to discover; recall first and never write a note that restates one you already have.",
		"When the operator names a project that is not in the plan list, ask for its path and offer to register it as a plan through the usual approval: the operator sends exactly `Add <absolute-path> as a plan`, or you may reply with exactly that line and nothing else to raise the same card; never search the operator's connection, home directory or drives to find it.",
	} {
		if strings.Count(text, want) != 1 {
			t.Errorf("shipped prompt does not carry this sentence exactly once:\n%s", want)
		}
	}

	// Both sentences sit immediately before the memory block, so the model reads
	// the rule and the notes together.
	rememberAt := strings.Index(text, "Remember, with remember, only these:")
	projectAt := strings.Index(text, "When the operator names a project that is not in the plan list")
	memoryAt := strings.Index(text, "{{memory}}")
	if !(rememberAt >= 0 && rememberAt < projectAt && projectAt < memoryAt) {
		t.Fatalf("sentence order is wrong: remember=%d project=%d memory=%d", rememberAt, projectAt, memoryAt)
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
	for _, want := range []string{"Remember, with remember, only these:", "never search the operator's connection, home directory or drives"} {
		if !strings.Contains(first, want) {
			t.Errorf("rendered prompt lost %q", want)
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
