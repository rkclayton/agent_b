package reflection

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Reflection joins the two (item 17-i, step 3): the structure comes from the
// code, the narrative from the summaries. Which parts have seen churn, what
// changed, what is open. It is text, stored per pass so two passes can be
// diffed, and it is never written into plan.md.

// OverviewInput is one pass's material.
type OverviewInput struct {
	PlanID    string
	Root      string
	Graph     *Graph
	Summaries []Summary
	At        time.Time
}

// churnOf counts how often each file appears in the summaries, written first.
func churnOf(summaries []Summary) []struct {
	Path    string
	Reads   int
	Writes  int
	Touched int
} {
	type counts struct{ reads, writes int }
	byPath := map[string]*counts{}
	for _, summary := range summaries {
		for _, file := range summary.Read {
			entry := byPath[file]
			if entry == nil {
				entry = &counts{}
				byPath[file] = entry
			}
			entry.reads++
		}
		for _, file := range summary.Written {
			entry := byPath[file]
			if entry == nil {
				entry = &counts{}
				byPath[file] = entry
			}
			entry.writes++
		}
	}
	rows := make([]struct {
		Path    string
		Reads   int
		Writes  int
		Touched int
	}, 0, len(byPath))
	for path, entry := range byPath {
		rows = append(rows, struct {
			Path    string
			Reads   int
			Writes  int
			Touched int
		}{path, entry.reads, entry.writes, entry.reads + entry.writes})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Writes != rows[j].Writes {
			return rows[i].Writes > rows[j].Writes
		}
		if rows[i].Touched != rows[j].Touched {
			return rows[i].Touched > rows[j].Touched
		}
		return rows[i].Path < rows[j].Path
	})
	return rows
}

// OverviewText is the pass's text: the structure, the churn, what changed and
// what is open. Deterministic given its input, so two passes differ only where
// the work differed.
func OverviewText(input OverviewInput) string {
	at := input.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	lines := []string{
		fmt.Sprintf("Reflection on %s — %s UTC", nameOf(input.PlanID, input.Root), at.UTC().Format("2006-01-02 15:04")),
		"",
	}
	if input.Graph != nil {
		lines = append(lines, input.Graph.Text(12), "")
	} else {
		lines = append(lines, "Structure: not extracted for this pass.", "")
	}
	lines = append(lines, fmt.Sprintf("Runs summarised: %d", len(input.Summaries)))
	if len(input.Summaries) == 0 {
		lines = append(lines, "", "No run has been summarised yet.")
		return strings.Join(lines, "\n")
	}
	oldest, newest := input.Summaries[len(input.Summaries)-1].At, input.Summaries[0].At
	lines = append(lines, fmt.Sprintf("Window: %s to %s", oldest.UTC().Format("2006-01-02"), newest.UTC().Format("2006-01-02")))
	failed := 0
	for _, summary := range input.Summaries {
		if summary.Failed != "" {
			failed++
		}
	}
	if failed > 0 {
		lines = append(lines, fmt.Sprintf("Summaries that could not be written: %d (the runs themselves were unaffected)", failed))
	}
	lines = append(lines, "", "Churn — the files this work has been touching:")
	churn := churnOf(input.Summaries)
	if len(churn) == 0 {
		lines = append(lines, "  (no file was named in these summaries)")
	}
	for index, row := range churn {
		if index >= 10 {
			lines = append(lines, fmt.Sprintf("  … and %d more", len(churn)-10))
			break
		}
		lines = append(lines, fmt.Sprintf("  %s · %d written · %d read", row.Path, row.Writes, row.Reads))
	}
	lines = append(lines, "", "What changed:")
	changed := 0
	for _, summary := range input.Summaries {
		if strings.TrimSpace(summary.Changed) == "" {
			continue
		}
		changed++
		if changed > 8 {
			continue
		}
		lines = append(lines, fmt.Sprintf("  %s · %s", summary.At.UTC().Format("01-02 15:04"), oneLine(summary.Changed)))
	}
	if changed == 0 {
		lines = append(lines, "  (nothing recorded)")
	} else if changed > 8 {
		lines = append(lines, fmt.Sprintf("  … and %d more", changed-8))
	}
	lines = append(lines, "", "What is open:")
	open := 0
	for _, summary := range input.Summaries {
		if strings.TrimSpace(summary.Open) == "" {
			continue
		}
		open++
		if open > 8 {
			continue
		}
		lines = append(lines, fmt.Sprintf("  %s · %s", summary.At.UTC().Format("01-02 15:04"), oneLine(summary.Open)))
	}
	if open == 0 {
		lines = append(lines, "  (nothing recorded)")
	} else if open > 8 {
		lines = append(lines, fmt.Sprintf("  … and %d more", open-8))
	}
	return strings.Join(lines, "\n")
}

func nameOf(planID, root string) string {
	if planID != "" {
		return planID
	}
	if root != "" {
		return filepath.Base(root)
	}
	return "this install"
}

func oneLine(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
	value = whitespaceRun.ReplaceAllString(value, " ")
	if len(value) > 160 {
		value = value[:160] + "…"
	}
	return value
}

// Diff is the line difference between two passes, oldest first. It is how the
// operator sees what a pass added.
func Diff(previous, current string) string {
	before := map[string]bool{}
	for _, line := range scanLines(previous) {
		before[strings.TrimSpace(line)] = true
	}
	after := map[string]bool{}
	for _, line := range scanLines(current) {
		after[strings.TrimSpace(line)] = true
	}
	added, removed := []string{}, []string{}
	for _, line := range scanLines(current) {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !before[trimmed] {
			added = append(added, "+ "+trimmed)
		}
	}
	for _, line := range scanLines(previous) {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !after[trimmed] {
			removed = append(removed, "- "+trimmed)
		}
	}
	if len(added) == 0 && len(removed) == 0 {
		return "No change between these two passes."
	}
	return strings.Join(append(removed, added...), "\n")
}

// Overview runs one pass for one plan or folder: extract the structure, read
// the summaries, write the text, and return it with the previous pass's diff.
func (s *Store) Overview(ctx context.Context, input OverviewInput) (Overview, string, error) {
	if input.Graph == nil && input.Root != "" {
		if graph, err := Structure(ctx, input.Root); err == nil {
			input.Graph = graph
		}
	}
	text := OverviewText(input)
	tier := "not extracted"
	if input.Graph != nil {
		tier = input.Graph.Tier + " (" + input.Graph.TierNote + ")"
	}
	previous, err := s.Overviews(input.PlanID, 1)
	if err != nil {
		return Overview{}, "", err
	}
	overview := Overview{At: input.At, PlanID: input.PlanID, Tier: tier, Text: text}
	id, err := s.PutOverview(overview)
	if err != nil {
		return Overview{}, "", err
	}
	overview.ID = id
	difference := "This is the first pass."
	if len(previous) > 0 {
		difference = Diff(previous[0].Text, text)
	}
	return overview, difference, nil
}
