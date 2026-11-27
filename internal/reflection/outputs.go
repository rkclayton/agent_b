package reflection

import (
	"regexp"
	"strings"

	"harness/internal/memory"
)

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

type SessionNoter interface {
	NoteSummary(Summary, string) (string, bool, error)
}

// NoteProvenance marks every note reflection writes. A note is read back into
// later prompts for that folder, so a reader — operator or model — is told
// where it came from and that nobody confirmed it (cold review).
//
// this moved from a PREFIX to the trailing provenance form the memory
// loader already parses, and it now names the run. As a prefix it was invisible to
// the parser — whose provenance pattern is anchored to the end of the line — so the
// bracket was part of the note's own words: it travelled into every prompt as text
// and it defeated the duplicate check. NoteProvenance is kept as the marker a reader
// looks for; noteLine is what gets written.
const NoteProvenance = "reflection"

// noteLine is one note's text with its provenance: unconfirmed until the operator
// says otherwise, and carrying the run that produced it so (b)'s provenance survives
// without a second store.
func noteLine(note, runID string) string {
	return note + " " + memory.ReflectionSuffix(memory.ReflectionUnconfirmed, runID)
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
		note = noteLine(note, summary.RunID)
		var duplicate bool
		var err error
		if scoped, ok := noter.(SessionNoter); ok {
			_, duplicate, err = scoped.NoteSummary(summary, note)
		} else {
			_, duplicate, err = noter.Note(summary.Workspace, note)
		}
		result := NoteResult{Workspace: summary.Workspace, Note: note, Written: err == nil && !duplicate, Duplicate: duplicate}
		if err != nil {
			result.Error = err.Error()
		}
		results = append(results, result)
	}
	return results
}
