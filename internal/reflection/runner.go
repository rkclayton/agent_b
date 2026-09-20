package reflection

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The pass itself (item 17-i). One summary call when a run closes, and a
// fuller pass on a tick: the overview per plan, the tool-candidate report, and
// the two durable outputs. Nothing here runs inside a run: the run is over
// before any of it starts, and a failure of any part leaves the run's outcome
// exactly as it was.

// ModelCall is the one model call reflection makes. Everything else in this
// package is deterministic.
type ModelCall func(ctx context.Context, profileID, system, user string) (string, error)

// Registrar registers a plan for a repository (2dr's path). Reflection never
// writes plan text; it only asks for the repository to be registered.
type Registrar interface {
	PlanRegistered(root string) bool
	RegisterPlan(root string) error
}

// Runner carries what a pass needs. Any of the optional parts may be nil: a
// pass does what it can and says what it skipped.
type Runner struct {
	Store     *Store
	Call      ModelCall
	Noter     Noter
	Registrar Registrar
	// SessionLogs returns the JSONL paths a session's events are in.
	SessionLogs func(sessionID string) []string
	// AllLogs returns every chat log, for the report.
	AllLogs func() []string
	Now     func() time.Time
	// NoteLimit bounds how many memory notes one summary may produce, and
	// PassNoteLimit how many one pass may write at all: the per-summary bound
	// alone does not bound a caller with many runs (v1.1.0/W6 cold review).
	NoteLimit     int
	PassNoteLimit int
	// NoteWritten is called for each note actually written, so the harness can
	// publish it as a memory write and the operator's existing "drop this
	// chat's memory" path can revoke it.
	NoteWritten func(summary Summary, note string)
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

const summarySystemPrompt = `You summarise one finished run of a coding agent, for the operator's own record.
Answer with JSON only, no prose around it, in this shape:
{"files_read":["..."],"files_written":["..."],"changed":"one or two sentences on what changed","open":"what was left unresolved, or an empty string","text":"three sentences at most, plain and concrete"}
Name only files the transcript shows. If the run changed nothing, say so. Never invent a file, a test result, or an outcome.`

// RunDigest is what the summary call is given: the run's own record, read from
// the JSONL, which is the authority.
type RunDigest struct {
	RunID     string
	SessionID string
	Read      []string
	Written   []string
	Tools     int
	Failures  int
	Messages  []string
	Stopped   string
	// Untrusted is true when the run read content the operator did not write:
	// a fetched page, or any result the tool layer marked untrusted.
	Untrusted bool
}

// Digest reads one run's events out of the session's logs.
func Digest(paths []string, sessionID, runID string) (RunDigest, error) {
	digest := RunDigest{RunID: runID, SessionID: sessionID}
	read, written := map[string]bool{}, map[string]bool{}
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return digest, err
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 1<<20), 64<<20)
		for scanner.Scan() {
			var row struct {
				SessionID string `json:"session_id"`
				RunID     string `json:"run_id"`
				Type      string `json:"type"`
				Data      struct {
					Name      string         `json:"name"`
					Args      map[string]any `json:"args"`
					OK        *bool          `json:"ok"`
					Untrusted bool           `json:"untrusted"`
					Reason    string         `json:"reason"`
					Message   struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"message"`
				} `json:"data"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
				continue
			}
			if row.RunID != runID || (sessionID != "" && row.SessionID != sessionID) {
				continue
			}
			switch row.Type {
			case "tool.call":
				digest.Tools++
				if row.Data.Name == "fetch_url" || row.Data.Name == "call_service" {
					digest.Untrusted = true
				}
				path, _ := row.Data.Args["path"].(string)
				switch row.Data.Name {
				case "read_file", "search_text", "find_files", "list_dir":
					if path != "" {
						read[path] = true
					}
				case "write_file", "edit_file":
					if path != "" {
						written[path] = true
					}
				}
			case "tool.result":
				if row.Data.OK != nil && !*row.Data.OK {
					digest.Failures++
				}
				if row.Data.Untrusted {
					digest.Untrusted = true
				}
			case "message.appended":
				content := strings.TrimSpace(row.Data.Message.Content)
				if content == "" {
					continue
				}
				if len(content) > 1200 {
					content = content[:1200] + "…"
				}
				digest.Messages = append(digest.Messages, row.Data.Message.Role+": "+content)
			case "run.stopped":
				digest.Stopped = row.Data.Reason
			}
		}
		file.Close()
		if err := scanner.Err(); err != nil {
			return digest, err
		}
	}
	digest.Read, digest.Written = sortedKeys(read), sortedKeys(written)
	return digest, nil
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Prompt is what the model is asked to summarise.
func (d RunDigest) Prompt() string {
	lines := []string{
		fmt.Sprintf("Run %s of chat %s stopped: %s.", d.RunID, d.SessionID, orUnknown(d.Stopped)),
		fmt.Sprintf("%d tool calls, %d of them failed.", d.Tools, d.Failures),
	}
	if len(d.Read) > 0 {
		lines = append(lines, "Files read: "+strings.Join(d.Read, ", "))
	}
	if len(d.Written) > 0 {
		lines = append(lines, "Files written: "+strings.Join(d.Written, ", "))
	}
	lines = append(lines, "", "Transcript:")
	messages := d.Messages
	if len(messages) > 24 {
		messages = append(append([]string{}, messages[:12]...), messages[len(messages)-12:]...)
	}
	lines = append(lines, messages...)
	return strings.Join(lines, "\n")
}

func orUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

// parseSummary reads the model's JSON, tolerating a fenced block or prose
// around it. A summary that cannot be read is not an error for the run: the
// raw text is kept as the summary's text.
func parseSummary(answer string) (files, written []string, changed, open, text string) {
	trimmed := strings.TrimSpace(answer)
	if start := strings.Index(trimmed, "{"); start >= 0 {
		if end := strings.LastIndex(trimmed, "}"); end > start {
			var parsed struct {
				FilesRead    []string `json:"files_read"`
				FilesWritten []string `json:"files_written"`
				Changed      string   `json:"changed"`
				Open         string   `json:"open"`
				Text         string   `json:"text"`
			}
			if err := json.Unmarshal([]byte(trimmed[start:end+1]), &parsed); err == nil {
				return parsed.FilesRead, parsed.FilesWritten, parsed.Changed, parsed.Open, strings.TrimSpace(parsed.Text)
			}
		}
	}
	return nil, nil, "", "", trimmed
}

// SummariseRun is the call at a run's close. It never returns an error to its
// caller's run: a failure is recorded on the summary row and nothing else.
func (r *Runner) SummariseRun(ctx context.Context, sessionID, runID, workspace, planID, profileID string, aux bool) Summary {
	started := r.now()
	summary := Summary{At: started, SessionID: sessionID, RunID: runID, Workspace: workspace, PlanID: planID, Profile: profileID, Aux: aux}
	paths := []string{}
	if r.SessionLogs != nil {
		paths = r.SessionLogs(sessionID)
	}
	digest, err := Digest(paths, sessionID, runID)
	if err != nil {
		summary.Failed = "read the run's record: " + err.Error()
	}
	summary.Read, summary.Written, summary.Untrusted = digest.Read, digest.Written, digest.Untrusted
	switch {
	case r.Call == nil:
		summary.Failed = "no model profile for the summary"
	case summary.Failed == "":
		answer, callErr := r.Call(ctx, profileID, summarySystemPrompt, digest.Prompt())
		if callErr != nil {
			summary.Failed = "summary call: " + callErr.Error()
		} else {
			read, written, changed, open, text := parseSummary(answer)
			if len(read) > 0 {
				summary.Read = mergeStrings(summary.Read, read)
			}
			if len(written) > 0 {
				summary.Written = mergeStrings(summary.Written, written)
			}
			summary.Changed, summary.Open, summary.Text = changed, open, text
		}
	}
	if !aux {
		// The item asks that a summary from the run's own profile say so.
		summary.Text = strings.TrimSpace(summary.Text)
		marker := "[summarised by the run's own profile; no aux profile is configured]"
		if summary.Text == "" {
			summary.Text = marker
		} else {
			summary.Text += "\n" + marker
		}
	}
	summary.Duration = r.now().Sub(started).Milliseconds()
	if r.Store != nil {
		if id, err := r.Store.PutSummary(summary); err == nil {
			summary.ID = id
		} else if summary.Failed == "" {
			summary.Failed = "record: " + err.Error()
		}
	}
	return summary
}

func mergeStrings(current, extra []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range append(append([]string{}, current...), extra...) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// PassResult is what one reflection pass produced.
type PassResult struct {
	At            time.Time       `json:"at"`
	Manual        bool            `json:"manual"`
	Summaries     int             `json:"summaries"`
	Overviews     []Overview      `json:"overviews"`
	Diffs         []string        `json:"diffs"`
	Report        Report          `json:"report"`
	Notes         []NoteResult    `json:"notes"`
	PlansProposed []PlanCandidate `json:"plans_proposed"`
	Pruned        int64           `json:"pruned"`
	Skipped       []string        `json:"skipped,omitempty"`
}

// Pass is the fuller reflection: overviews, the report, the durable outputs,
// and the retention prune. Automatic passes read recent summaries only; a
// manual pass reads them all.
func (r *Runner) Pass(ctx context.Context, manual bool) (PassResult, error) {
	result := PassResult{At: r.now(), Manual: manual}
	if r.Store == nil {
		return result, fmt.Errorf("reflection has no store")
	}
	since := time.Time{}
	if !manual {
		since = result.At.Add(-Retention)
	}
	summaries, err := r.Store.Summaries(since, 0)
	if err != nil {
		return result, err
	}
	result.Summaries = len(summaries)

	// Plan proposals first: each overview names the ones in its own folder.
	registered := func(string) bool { return false }
	if r.Registrar != nil {
		registered = r.Registrar.PlanRegistered
	}
	result.PlansProposed = PlanCandidates(summaries, registered)
	if err := r.RecordProposals(result.PlansProposed); err != nil {
		result.Skipped = append(result.Skipped, "record proposals: "+err.Error())
	}

	// One overview per workspace the summaries name, so a plan's own text is
	// about that plan's work.
	byWorkspace := map[string][]Summary{}
	order := []string{}
	for _, summary := range summaries {
		key := summary.PlanID
		if key == "" {
			key = summary.Workspace
		}
		if key == "" {
			continue
		}
		if _, seen := byWorkspace[key]; !seen {
			order = append(order, key)
		}
		byWorkspace[key] = append(byWorkspace[key], summary)
	}
	sort.Strings(order)
	for _, key := range order {
		group := byWorkspace[key]
		proposals := []PlanCandidate{}
		for _, candidate := range result.PlansProposed {
			if candidate.Root == filepath.Clean(group[0].Workspace) {
				proposals = append(proposals, candidate)
			}
		}
		overview, difference, err := r.Store.Overview(ctx, OverviewInput{PlanID: key, Root: group[0].Workspace, Summaries: group, At: result.At, Proposals: proposals})
		if err != nil {
			result.Skipped = append(result.Skipped, "overview for "+key+": "+err.Error())
			continue
		}
		result.Overviews = append(result.Overviews, overview)
		result.Diffs = append(result.Diffs, difference)
	}

	// The tool-candidate report, over every recorded chat.
	if r.AllLogs != nil {
		calls, err := CallsFromJSONL(r.AllLogs())
		if err != nil {
			result.Skipped = append(result.Skipped, "tool-candidate report: "+err.Error())
		} else {
			clusters := Clusters(calls)
			report := Report{At: result.At, Text: ReportText(clusters, 10), Clusters: clusters}
			if id, err := r.Store.PutReport(report); err == nil {
				report.ID = id
				result.Report = report
			} else {
				result.Skipped = append(result.Skipped, "record report: "+err.Error())
			}
		}
	} else {
		result.Skipped = append(result.Skipped, "tool-candidate report: no chat logs were offered")
	}

	// The durable outputs.
	limit, passLimit := r.NoteLimit, r.PassNoteLimit
	if limit == 0 {
		limit = 2
	}
	if passLimit == 0 {
		passLimit = 6
	}
	written := 0
	for _, summary := range summaries {
		if written >= passLimit {
			result.Skipped = append(result.Skipped, fmt.Sprintf("memory notes: the pass stopped at %d", passLimit))
			break
		}
		for _, note := range WriteNotes(r.Noter, summary, limit) {
			result.Notes = append(result.Notes, note)
			if note.Written {
				written++
				if r.NoteWritten != nil {
					r.NoteWritten(summary, note.Note)
				}
			}
		}
	}
	// Plan registration itself is a durable change the operator approves
	// through the existing card; reflection runs unattended, so it proposes in
	// the overview and registers nothing (v1.1.0/W6 cold review; the second
	// walk's step 16 is what a card with nobody present costs).
	if pruned, err := r.Store.Prune(result.At); err == nil {
		result.Pruned = pruned
	}
	return result, nil
}

// RecordProposals stores this pass's plan proposals so the operator can be
// asked through the existing card. Reflection registers nothing itself.
func (r *Runner) RecordProposals(candidates []PlanCandidate) error {
	if r.Store == nil {
		return nil
	}
	for _, candidate := range candidates {
		proposal := Proposal{
			Root:        candidate.Root,
			AgentFile:   candidate.AgentFile,
			Activity:    ActivityLine(candidate),
			Fingerprint: AgentFileFingerprint(candidate.Root),
			At:          r.now(),
		}
		if err := r.Store.UpsertProposal(proposal); err != nil {
			return err
		}
	}
	return nil
}
