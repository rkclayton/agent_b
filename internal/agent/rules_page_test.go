package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"harness/internal/events"
)

func TestRulesPageMatchesRuntime2vg(t *testing.T) {
	root := filepath.Join("..", "..")
	sources := map[string]string{}
	for _, reason := range events.StopReasons {
		sources[reason] = "run end"
	}
	scheduler := ruleRead(t, filepath.Join(root, "internal", "agent", "scheduler.go"))
	function := regexp.MustCompile(`(?s)func canonicalTerminalReason\(.*?\n\}`).FindString(scheduler)
	for _, line := range strings.Split(function, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "case ") {
			for _, match := range regexp.MustCompile(`"([a-z][a-z0-9_]*)"`).FindAllStringSubmatch(line, -1) {
				sources[match[1]] = "run end"
			}
		}
	}
	paths := []string{filepath.Join(root, "internal", "agent", "tool_window.go")}
	entries, err := os.ReadDir(filepath.Join(root, "internal", "tools"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".go") && !strings.HasSuffix(entry.Name(), "_test.go") {
			paths = append(paths, filepath.Join(root, "internal", "tools", entry.Name()))
		}
	}
	literal := regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
	for _, path := range paths {
		for _, raw := range literal.FindAllString(ruleRead(t, path), -1) {
			value, unquoteErr := strconv.Unquote(raw)
			switch {
			case unquoteErr == nil && strings.Contains(filepath.ToSlash(path), "/tools/") && strings.HasPrefix(value, "error: "):
				sources[value] = "call refusal"
			case unquoteErr == nil && (strings.Contains(value, "[tool result cut") || strings.HasPrefix(value, "engines_benched:")):
				sources[strings.TrimSpace(value)] = "result change"
			}
		}
	}
	page := ruleRead(t, filepath.Join(root, "docs", "RULES.md"))
	row := regexp.MustCompile("(?m)^\\| [^|]+ \\| [^|]+ \\| `([^`]*)` \\|")
	documented := map[string]bool{}
	for _, match := range row.FindAllStringSubmatch(page, -1) {
		documented[match[1]] = true
	}
	missing, stray := []string{}, []string{}
	for value := range sources {
		if !documented[value] {
			missing = append(missing, value)
		}
	}
	for value := range documented {
		if _, ok := sources[value]; !ok {
			stray = append(stray, value)
		}
	}
	sort.Strings(missing)
	sort.Strings(stray)
	if len(missing) > 0 || len(stray) > 0 {
		t.Fatalf("rules page mismatch\nmissing: %q\nstray: %q", missing, stray)
	}
	counts := map[string]int{}
	for _, kind := range sources {
		counts[kind]++
	}
	t.Logf("rules page matches %d run ends, %d call refusals, %d result changes", counts["run end"], counts["call refusal"], counts["result change"])
}

func ruleRead(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
