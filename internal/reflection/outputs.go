package reflection

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
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

// NoteProvenance marks every note reflection writes. A note is read back into
// later prompts for that folder, so a reader — operator or model — is told
// where it came from and that nobody confirmed it (v1.1.0/W6 cold review).
const NoteProvenance = "[reflection, unconfirmed]"

// NoteResult is one attempted note.
type NoteResult struct {
	Workspace string `json:"workspace"`
	Note      string `json:"note"`
	Written   bool   `json:"written"`
	Duplicate bool   `json:"duplicate"`
	Error     string `json:"error,omitempty"`
}

// WriteNotes writes one memory note per durable line a summary states. A line
// already in the layer is not written twice, and a run that read content the
// operator did not write produces no note at all: its summary is derived from
// text an attacker may have chosen, and a note is durable prompt material.
func WriteNotes(noter Noter, summary Summary, limit int) []NoteResult {
	results := []NoteResult{}
	if noter == nil || summary.Workspace == "" || summary.Untrusted {
		return results
	}
	for index, note := range NoteCandidates(summary) {
		if limit > 0 && index >= limit {
			break
		}
		note = NoteProvenance + " " + note
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
// name, never at the disk. A root that already has a plan is not a candidate,
// and neither is one whose agent file the agent itself wrote during the window
// it is being judged on (v1.1.0/W6 cold review).
func PlanCandidates(summaries []Summary, registered func(root string) bool) []PlanCandidate {
	touched := map[string]int{}
	earliest := map[string]time.Time{}
	for _, summary := range summaries {
		if summary.Workspace == "" || summary.PlanID != "" {
			continue
		}
		root := filepath.Clean(summary.Workspace)
		touched[root]++
		if at, seen := earliest[root]; !seen || summary.At.Before(at) {
			earliest[root] = summary.At
		}
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
		marker, reasons := "", []string{}
		for _, name := range agentFiles {
			info, err := os.Stat(filepath.Join(root, name))
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			// A second of grace: a summary's time is stored in milliseconds,
			// and a file written moments before the run is not "during" it.
			if info.ModTime().After(earliest[root].Add(time.Second)) {
				reasons = append(reasons, name+" was written during this window, not before it")
				continue
			}
			marker = name
			break
		}
		if marker == "" {
			continue
		}
		candidates = append(candidates, PlanCandidate{Root: root, AgentFile: marker, Touched: touched[root], Reasons: reasons})
	}
	return candidates
}

// AgentFileFingerprint identifies the state of a repository's agent files, so
// a declined proposal can come back when they change and not before.
func AgentFileFingerprint(root string) string {
	parts := []string{}
	for _, name := range agentFiles {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s:%d:%d", name, info.Size(), info.ModTime().UTC().UnixMilli()))
	}
	return strings.Join(parts, "|")
}

// ActivityLine is what the card says led to the proposal.
func ActivityLine(candidate PlanCandidate) string {
	runs := "1 run"
	if candidate.Touched != 1 {
		runs = fmt.Sprintf("%d runs", candidate.Touched)
	}
	return fmt.Sprintf("%s touched this folder, and %s sits at its root", runs, candidate.AgentFile)
}
