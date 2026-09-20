package reflection

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Reflection's durable outputs (item 17-i, step 5). Both go through the paths
// that already exist and their existing rules: a memory note under 2eb's rule,
// and plan registration under 2dr's rule. Reflection never writes plan text.

// correctionMarkers are the shapes a summary uses when it states a correction,
// a preference, or a fact about the repository worth keeping. The pass applies
// 2eb's rule instead of the model deciding mid-task.
var correctionMarkers = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:the operator|the user|he|she|they) (?:corrected|clarified|said|prefers?|wants?|asked)\b`),
	regexp.MustCompile(`(?i)\bcorrection:\s*`),
	regexp.MustCompile(`(?i)\b(?:turns out|in fact|actually),?\s`),
	regexp.MustCompile(`(?i)\b(?:this repository|the repo(?:sitory)?|the project) (?:uses|needs|requires|is built with|has)\b`),
	regexp.MustCompile(`(?i)\bnot\b[^.]{0,40}\binstead\b`),
}

// NoteCandidates are the lines of a summary that state something durable. One
// line each, trimmed to the memory layer's limit, in the order they appear.
func NoteCandidates(summary Summary) []string {
	seen := map[string]bool{}
	notes := []string{}
	for _, raw := range scanLines(summary.Text + "\n" + summary.Changed + "\n" + summary.Open) {
		line := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(raw), "-*• "))
		if len(line) < 12 {
			continue
		}
		matched := false
		for _, marker := range correctionMarkers {
			if marker.MatchString(line) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if runes := []rune(line); len(runes) > 300 {
			line = string(runes[:297]) + "…"
		}
		if seen[strings.ToLower(line)] {
			continue
		}
		seen[strings.ToLower(line)] = true
		notes = append(notes, line)
	}
	return notes
}

// Noter is the memory layer reflection writes through: internal/memory's
// Manager satisfies it. The bool it returns is true when the note was already
// there, which is 2eb's no-duplicate rule and not reflection's own.
type Noter interface {
	Note(workspace, note string) (string, bool, error)
}

// NoteResult is one attempted note.
type NoteResult struct {
	Workspace string `json:"workspace"`
	Note      string `json:"note"`
	Written   bool   `json:"written"`
	Duplicate bool   `json:"duplicate"`
	Error     string `json:"error,omitempty"`
}

// WriteNotes writes one memory note per durable line a summary states. A line
// already in the layer is not written twice.
func WriteNotes(noter Noter, summary Summary, limit int) []NoteResult {
	results := []NoteResult{}
	if noter == nil || summary.Workspace == "" {
		return results
	}
	for index, note := range NoteCandidates(summary) {
		if limit > 0 && index >= limit {
			break
		}
		_, duplicate, err := noter.Note(summary.Workspace, note)
		result := NoteResult{Workspace: summary.Workspace, Note: note, Written: err == nil && !duplicate, Duplicate: duplicate}
		if err != nil {
			result.Error = err.Error()
		}
		results = append(results, result)
	}
	return results
}

// agentFiles mark a repository root as one an agent works in (2dr's rule).
var agentFiles = []string{"AGENTS.md", "CLAUDE.md", ".agent.md", "AGENT.md"}

// PlanCandidate is a repository recent activity touched that has agent files
// at its root and no plan registered.
type PlanCandidate struct {
	Root      string   `json:"root"`
	AgentFile string   `json:"agent_file"`
	Touched   int      `json:"touched"`
	Reasons   []string `json:"reasons,omitempty"`
}

// PlanCandidates is tape-bounded: it looks only at the roots the summaries
// name, never at the disk. A root that already has a plan is not a candidate.
func PlanCandidates(summaries []Summary, registered func(root string) bool) []PlanCandidate {
	touched := map[string]int{}
	for _, summary := range summaries {
		if summary.Workspace == "" || summary.PlanID != "" {
			continue
		}
		touched[filepath.Clean(summary.Workspace)]++
	}
	roots := make([]string, 0, len(touched))
	for root := range touched {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	candidates := []PlanCandidate{}
	for _, root := range roots {
		if registered != nil && registered(root) {
			continue
		}
		marker := ""
		for _, name := range agentFiles {
			if info, err := os.Stat(filepath.Join(root, name)); err == nil && info.Mode().IsRegular() {
				marker = name
				break
			}
		}
		if marker == "" {
			continue
		}
		candidates = append(candidates, PlanCandidate{Root: root, AgentFile: marker, Touched: touched[root]})
	}
	return candidates
}
