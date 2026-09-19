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

var unresolvedEntry = regexp.MustCompile(`^[-*] \[(discovery|blocker)\] \S`)

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
		if item.Marker == " " && VerifierContradicts(field(text, "verify")) {
			diagnostics = append(diagnostics, Diagnostic{"error", fmt.Sprintf("item %s: verifier contradicts itself: %s", item.ID, field(text, "verify"))})
		}
		// Only an item Go would work is held to the vocabulary: a finished item's
		// record, written before the lint existed, never refuses Go.
		if item.Marker == "x" {
			continue
		}
		for _, line := range unresolvedLines(text) {
			if !strings.EqualFold(line, "(none)") && !unresolvedEntry.MatchString(line) {
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

// unresolvedLines are the entries of every ## Unresolved section, read line by
// line: any heading ends a section, and fenced blocks are neither headings nor
// entries.
func unresolvedLines(text string) []string {
	lines := []string{}
	inside, fenced := false, false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		if strings.HasPrefix(line, "#") {
			inside = strings.HasPrefix(line, "## ") && strings.EqualFold(strings.TrimSpace(line[3:]), "Unresolved")
			continue
		}
		if inside && line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// VerifierContradicts reports a verifier that can never pass because one of
// its && parts is another part negated — `C && ! C`, the shape a real planner
// wrote in the second walk (item 2fx). It is the plainest contradiction only;
// no attempt is made at general satisfiability.
func VerifierContradicts(command string) bool {
	parts := map[string]bool{}
	for _, part := range strings.Split(command, "&&") {
		parts[strings.Join(strings.Fields(part), " ")] = true
	}
	for part := range parts {
		if negated, ok := strings.CutPrefix(part, "!"); ok && parts[strings.TrimSpace(negated)] {
			return true
		}
	}
	return false
}

// openItemVerifier is an open plan line's inline "; verify: …" clause.
var openItemVerifier = regexp.MustCompile(`^\s*[-*] \[ \] .*?;\s*verify:\s*(.+)$`)

// ContradictoryVerifiers are the self-contradicting verifier commands a plan
// edit would leave where Go reads them (v0.70.2 cold review: no quoted example
// in prose, no finished item): an item file's verify: header, or an open plan
// line's inline "; verify: …" clause.
func ContradictoryVerifiers(path, text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	found := []string{}
	if strings.HasPrefix(strings.ReplaceAll(path, "\\", "/"), "plan/items/") {
		if command := ItemVerifier(text); VerifierContradicts(command) {
			found = append(found, command)
		}
		return found
	}
	for _, line := range strings.Split(text, "\n") {
		if match := openItemVerifier.FindStringSubmatch(line); match != nil {
			if command := strings.TrimSpace(match[1]); VerifierContradicts(command) {
				found = append(found, command)
			}
		}
	}
	return found
}

// ItemVerifier is an item file's verifier command as the worker reads it: a
// verify: line in the header, before the first heading; "" when none.
func ItemVerifier(body string) string {
	return field(body, "verify")
}
