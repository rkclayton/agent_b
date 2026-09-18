package worker

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Diagnostic is one finding of the plan lint. An error refuses Go; a warning is
// shown on the panel and changes nothing.
type Diagnostic struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

var unresolvedEntry = regexp.MustCompile(`^- \[(discovery|blocker)\] \S`)

// Lint checks a plan folder with the rules the harness's own orders are gated by
// (item 2bq): an id names at most one line, and an item file's ## Unresolved is
// "(none)" or explicit [discovery]/[blocker] entries; breaking either refuses
// Go. An item with no item file or no verifier is a warning: 2t-ii's gate
// already marks it [!] and asks the planner for one, and the contract keeps
// that path.
func Lint(planDir string) []Diagnostic {
	diagnostics := []Diagnostic{}
	data, err := os.ReadFile(filepath.Join(planDir, "plan.md"))
	if err != nil {
		return append(diagnostics, Diagnostic{"error", fmt.Sprintf("plan.md cannot be read: %v", err)})
	}
	seen := map[string]bool{}
	for _, item := range Parse(string(data)) {
		if item.ID == "" {
			continue
		}
		if seen[item.ID] {
			diagnostics = append(diagnostics, Diagnostic{"error", fmt.Sprintf("item %s: more than one line carries this id", item.ID)})
			continue
		}
		seen[item.ID] = true
		body, err := os.ReadFile(filepath.Join(planDir, "plan", "items", item.ID+".md"))
		if os.IsNotExist(err) {
			diagnostics = append(diagnostics, Diagnostic{"warning", fmt.Sprintf("item %s: has no item file (plan/items/%s.md), so it names no verifier; Go will mark it [!] and ask for one", item.ID, item.ID)})
			continue
		}
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{"error", fmt.Sprintf("item %s: %v", item.ID, err)})
			continue
		}
		text := strings.ReplaceAll(string(body), "\r\n", "\n")
		if item.Marker == " " && field(text, "verify") == "" {
			diagnostics = append(diagnostics, Diagnostic{"warning", fmt.Sprintf("item %s: names no verifier; Go will mark it [!] and ask for one", item.ID)})
		}
		for _, line := range unresolvedLines(text) {
			if line != "(none)" && !unresolvedEntry.MatchString(line) {
				diagnostics = append(diagnostics, Diagnostic{"error", fmt.Sprintf("item %s: unresolved entry outside the vocabulary: %q (use (none), - [discovery] … or - [blocker] …)", item.ID, line)})
			}
		}
	}
	return diagnostics
}

// Refusal is the first error, the reason Go does not start, or "".
func Refusal(diagnostics []Diagnostic) string {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == "error" {
			return "plan lint: " + diagnostic.Message
		}
	}
	return ""
}

func unresolvedLines(text string) []string {
	_, rest, ok := strings.Cut(text, "\n## Unresolved\n")
	if !ok {
		return nil
	}
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	lines := []string{}
	for _, line := range strings.Split(rest, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
