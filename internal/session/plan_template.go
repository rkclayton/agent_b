package session

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PlanChanged, when set, hears every new plan ("plan.created") and every
// rewrite of an existing plan.md ("plan.updated") with the plan's folder
// (item 2bq). The web server publishes these as events so every open page
// follows a plan the moment it changes, whichever route changed it.
var PlanChanged func(kind, planDir string)

func notifyPlan(kind, planDir string) {
	if PlanChanged != nil {
		PlanChanged(kind, planDir)
	}
}

// seededRules are the harness's own rules for any plan; nothing in them is
// taken from the plan's repository (item 2bq).
var seededRules = []string{
	"Discovery before repair: establish the cause with a measurement before changing anything.",
	"Name a hypothesis so it can be refuted, label it inferred, and say what would kill it.",
	"Separate measured from inferred in every claim.",
	"Prefer the cheapest action that fits.",
}

// PlanTemplate is a new plan's plan.md: the five sections of the harness's own
// PLAN.md (item 2bq). The first heading stays the plan's display name, and no
// line is a marker, so the worker finds no item until one is written.
func PlanTemplate(name, repo string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", name)
	b.WriteString("## Product and end goals\n\n(what this plan is for, and what done looks like)\n\n")
	b.WriteString("## Architecture\n\n")
	b.WriteString(RepoMap(repo))
	b.WriteString("\n### Invariants\n\n(what must stay true; kept by the operator or the planner)\n\n")
	b.WriteString("## Standing rules\n\n")
	for _, rule := range seededRules {
		fmt.Fprintf(&b, "- %s\n", rule)
	}
	b.WriteString("\n## Milestones and current order\n\n(items are written here as `- [ ] <text>` lines once accepted)\n\n")
	b.WriteString("## Index\n\n(generated from plan/items)\n")
	return b.String()
}

const repoMapLimit = 40

// RepoMap is a generated map of a repository's top level: each directory with
// one line (its README's first line, a Go package comment, or its file count),
// then the notable top-level files. It reads names and first lines only.
func RepoMap(repo string) string {
	if strings.TrimSpace(repo) == "" {
		return "Repository map: no repository is bound to this plan yet.\n"
	}
	entries, err := os.ReadDir(repo)
	if err != nil {
		return fmt.Sprintf("Repository map: %s could not be read (%v).\n", repo, err)
	}
	dirs, files := []string{}, []string{}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
			continue
		}
		if entry.IsDir() {
			dirs = append(dirs, name)
		} else {
			files = append(files, name)
		}
	}
	sort.Strings(dirs)
	sort.Strings(files)
	var b strings.Builder
	fmt.Fprintf(&b, "Repository map (generated from %s):\n\n", repo)
	shown := 0
	for _, dir := range dirs {
		if shown == repoMapLimit {
			fmt.Fprintf(&b, "- … %d more directories\n", len(dirs)-shown)
			break
		}
		fmt.Fprintf(&b, "- `%s/` — %s\n", dir, describeDir(filepath.Join(repo, dir)))
		shown++
	}
	if len(files) > 0 {
		if len(files) > 12 {
			files = append(files[:12], fmt.Sprintf("… %d more", len(files)-12))
		}
		fmt.Fprintf(&b, "- files: %s\n", strings.Join(files, ", "))
	}
	return b.String()
}

func describeDir(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "unreadable"
	}
	for _, name := range []string{"README.md", "README.txt", "README"} {
		if line := firstLine(filepath.Join(dir, name), func(text string) string { return strings.TrimSpace(strings.TrimLeft(text, "# ")) }); line != "" {
			return line
		}
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		if line := firstLine(filepath.Join(dir, entry.Name()), func(text string) string {
			if strings.HasPrefix(text, "// Package ") {
				return strings.TrimSpace(strings.TrimPrefix(text, "//"))
			}
			return ""
		}); line != "" {
			return line
		}
	}
	return fmt.Sprintf("%d entries", len(entries))
}

// firstLine returns the first line keep turns into a non-empty summary,
// reading at most the first 40 lines.
func firstLine(path string, keep func(string) string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for count := 0; count < 40 && scanner.Scan(); count++ {
		if value := keep(strings.TrimSpace(scanner.Text())); value != "" {
			if len(value) > 100 {
				value = value[:100] + "…"
			}
			return value
		}
	}
	return ""
}
