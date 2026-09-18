package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func lintFixture(t *testing.T, planMD string, items map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "plan", "items"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte(planMD), 0o600); err != nil {
		t.Fatal(err)
	}
	for id, body := range items {
		if err := os.WriteFile(filepath.Join(dir, "plan", "items", id+".md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Item 2bq: a malformed plan refuses Go with the reason; a missing verifier
// or item file is a warning, because 2t-ii's [!] and ask path keeps it.
func TestPlanLintRefusesMalformedPlansAndWarnsOnMissingVerifiers(t *testing.T) {
	clean := lintFixture(t, "# P\n\n- [ ] [[1]] one\n- [ ] [[2]] two\n", map[string]string{
		"1": "verify: echo ok\n\n# 1\n\n## Unresolved\n\n(none)\n",
		"2": "verify: echo ok\n\n# 2\n\n## Unresolved\n\n- [discovery] which file\n",
	})
	if diagnostics := Lint(clean); len(diagnostics) != 0 || Refusal(diagnostics) != "" {
		t.Fatalf("a clean plan has findings: %+v", diagnostics)
	}
	duplicate := lintFixture(t, "# P\n\n- [ ] [[1]] one\n- [ ] [[1]] again\n", map[string]string{"1": "verify: echo ok\n\n# 1\n"})
	if refusal := Refusal(Lint(duplicate)); !strings.Contains(refusal, "more than one line carries this id") {
		t.Fatalf("duplicate ids: %q", refusal)
	}
	vocabulary := lintFixture(t, "# P\n\n- [ ] [[1]] one\n", map[string]string{"1": "verify: echo ok\n\n# 1\n\n## Unresolved\n\nsomething vague\n"})
	if refusal := Refusal(Lint(vocabulary)); !strings.Contains(refusal, "outside the vocabulary") {
		t.Fatalf("vocabulary: %q", refusal)
	}
	noVerifier := lintFixture(t, "# P\n\n- [ ] [[1]] no file\n- [ ] [[2]] no verifier\n", map[string]string{"2": "# 2\n"})
	diagnostics := Lint(noVerifier)
	if Refusal(diagnostics) != "" || len(diagnostics) != 2 {
		t.Fatalf("missing verifiers must warn, not refuse: %+v", diagnostics)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity != "warning" {
			t.Fatalf("expected warnings: %+v", diagnostics)
		}
	}
}

// v0.68.0/W16: the Unresolved vocabulary binds only items Go would work, and
// the section is read line by line, so neither placement nor fences hide an
// entry or invent one.
func TestPlanLintReadsEveryUnresolvedSectionAndSparesFinishedItems(t *testing.T) {
	cases := map[string]struct {
		marker, body string
		refuses      bool
	}{
		"finished item keeps old free text": {"x", "verify: echo ok\n\n## Unresolved\n\nask the operator\n", false},
		"section on the first line":         {" ", "## Unresolved\nfree text\n", true},
		"heading with trailing spaces":      {" ", "verify: echo ok\n\n## Unresolved   \nfree text\n", true},
		"second section":                    {" ", "verify: echo ok\n\n## Unresolved\n\n(none)\n\n## Notes\n\n## Unresolved\n\nfree text\n", true},
		"fenced example is not an entry":    {" ", "verify: echo ok\n\n## Unresolved\n\n```\n## Unresolved\nfree\n```\n* [blocker] a real one\n(None)\n", false},
	}
	for name, value := range cases {
		dir := lintFixture(t, "# P\n\n- ["+value.marker+"] [[1]] one\n", map[string]string{"1": value.body})
		if refuses := Refusal(Lint(dir)) != ""; refuses != value.refuses {
			t.Errorf("%s: refuses=%v, want %v (%+v)", name, refuses, value.refuses, Lint(dir))
		}
	}
}
