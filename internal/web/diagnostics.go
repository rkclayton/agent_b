package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"harness/internal/telemetry"
)

// Item 2mv: ONE DIAGNOSTICS EXPORT, REDACTED.
//
// The probes already exist as separate endpoints and the logs as separate files;
// nothing gathered them, so asking for the state of an install meant asking for a
// list of things to fetch by hand.
//
// (d) NOTHING NEW IS PROBED. Everything here comes from the snapshot the server
// already assembles and from log files already on disk. No detection is run, no
// endpoint is re-called, nothing elevates. A section that would need work beyond
// reading says so and names itself instead.
//
// (b) REDACTED BY TELEMETRY'S RULES, WHICH ARE THE FLOOR AND NOT A CEILING. The
// export is meant to be pasted to someone else, so it takes the same rules that
// govern what may leave this machine, plus two things telemetry does not need:
//
//   - a path INSIDE the data or application root is kept, rewritten as <data>\… or
//     <app>\…, because "which file" is the whole value of a log tail and telemetry
//     would have replaced all of it with <path>;
//   - the account name is replaced wherever it appears, including inside a kept
//     path, because that is the one identifier a kept path would still carry.
//
// Telemetry's 200-character cut is deliberately NOT applied to log lines: a cut
// stack trace is worth nothing. Every other string takes it.

// diagnosticsLogTail is the number of lines taken from each log, from (a).
const diagnosticsLogTail = 200

// diagnosticsReadBound is the cheap-read bound from (d). A file larger than this is
// named and skipped rather than read, so an export cannot become the slow thing.
const diagnosticsReadBound = 8 << 20

type diagnosticsSection struct {
	Name    string `json:"name"`
	Means   string `json:"means"`
	Skipped string `json:"skipped,omitempty"`
	Value   any    `json:"value,omitempty"`
}

// redactor carries the roots so a path can be classified as inside or outside.
type redactor struct {
	data string
	app  string
	user string
}

var sidPattern = regexp.MustCompile(`S-1-5-(?:\d+-)+\d+`)

func newRedactor(dataRoot, appRoot string) *redactor {
	user := strings.TrimSpace(os.Getenv("USERNAME"))
	if user == "" {
		user = strings.TrimSpace(os.Getenv("USER"))
	}
	return &redactor{data: strings.TrimSuffix(filepath.Clean(dataRoot), string(filepath.Separator)), app: strings.TrimSuffix(filepath.Clean(appRoot), string(filepath.Separator)), user: user}
}

// keepRoots rewrites the two roots to placeholders BEFORE telemetry sees the text,
// so what is left of them survives as <data>\… rather than becoming <path>.
func (r *redactor) keepRoots(value string) string {
	for _, root := range []struct {
		prefix string
		as     string
	}{{r.app, "<app>"}, {r.data, "<data>"}} {
		if root.prefix == "" {
			continue
		}
		for _, form := range []string{root.prefix, filepath.ToSlash(root.prefix)} {
			if form == "" {
				continue
			}
			value = replaceFold(value, form, root.as)
		}
	}
	return value
}

// replaceFold is a case-insensitive replace, because Windows paths arrive in
// whatever case the caller had.
func replaceFold(value, find, with string) string {
	if find == "" {
		return value
	}
	var out strings.Builder
	lowerValue, lowerFind := strings.ToLower(value), strings.ToLower(find)
	for {
		at := strings.Index(lowerValue, lowerFind)
		if at < 0 {
			out.WriteString(value)
			return out.String()
		}
		out.WriteString(value[:at])
		out.WriteString(with)
		value, lowerValue = value[at+len(find):], lowerValue[at+len(find):]
	}
}

// text redacts one ordinary string: roots kept, then telemetry's rules, then the
// account name and any SID.
func (r *redactor) text(value string) string {
	return r.scrub(value, true)
}

// line redacts one log line. Same rules, no length cut.
func (r *redactor) line(value string) string {
	return r.scrub(value, false)
}

func (r *redactor) scrub(value string, cut bool) string {
	value = r.keepRoots(value)
	// Telemetry's own rules: URLs, paths outside the roots, emails, hosts,
	// addresses and token-shaped runs.
	scrubbed := telemetry.Redact(value)
	if !cut {
		// A log line keeps its length: a cut stack trace is worth nothing. Same
		// rules, from the same place, so the two cannot drift.
		scrubbed = telemetry.RedactFull(value)
	}
	if r.user != "" {
		scrubbed = replaceFold(scrubbed, r.user, "<user>")
	}
	return sidPattern.ReplaceAllString(scrubbed, "<sid>")
}

// any walks a value and redacts every string inside it, so a section added later
// cannot leak by being forgotten here.
func (r *redactor) any(value any) any {
	switch typed := value.(type) {
	case string:
		return r.text(typed)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, inner := range typed {
			out[key] = r.any(inner)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, inner := range typed {
			out = append(out, r.any(inner))
		}
		return out
	default:
		return value
	}
}

// through marshals anything into plain maps first, so struct fields are walked as
// strings rather than skipped.
func (r *redactor) through(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var plain any
	if json.Unmarshal(raw, &plain) != nil {
		return nil
	}
	return r.any(plain)
}

// tail reads the last n lines of a file, redacted. A file over the bound is named
// and skipped, which is (d).
func (r *redactor) tail(path string, lines int) diagnosticsSection {
	name := r.text(path)
	info, err := os.Stat(path)
	if err != nil {
		return diagnosticsSection{Name: name, Means: "This log is not present, which is normal when the matching step has not run on this installation.", Skipped: "absent"}
	}
	if info.Size() > diagnosticsReadBound {
		return diagnosticsSection{Name: name, Means: "This log is larger than the export reads. Nothing is wrong with it; the export refuses to become the slow part of asking for help.", Skipped: fmt.Sprintf("larger than %d bytes", diagnosticsReadBound)}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return diagnosticsSection{Name: name, Means: "This log exists but could not be read by the account Agent_b runs as. The export never elevates to try again.", Skipped: "unreadable"}
	}
	all := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	kept := make([]string, 0, len(all))
	for _, one := range all {
		if strings.TrimSpace(one) == "" {
			continue
		}
		kept = append(kept, r.line(one))
	}
	return diagnosticsSection{Name: name, Means: "The most recent lines of this log, with paths outside the installation, account names and anything token-shaped replaced.", Value: kept}
}

// diagnosticsProbeBound is (d)'s STATED BOUND. Three of the sections (a) names are
// not snapshot keys: local detection, host hardening and the service account each
// answer by running PowerShell with -Inspect. They are read-only and they do not
// elevate — elevation in the hardening manager is on the Apply path only, and the
// replay guard rejects only non-GET — so the export may call them. What it may not do
// is become the slow part of asking for help, so each gets this long and no longer,
// and one that does not answer is NAMED with the reason rather than waited on.
const diagnosticsProbeBound = 2 * time.Second

// bounded runs one existing read-only call under the shared bound. Nothing here is a
// new probe: each is the same call the matching settings page already makes. The bound
// is shared, not per-section, because three of them in a row would make the export
// take three times as long as it promises.
func (r *redactor) bounded(ctx context.Context, name, means string, call func(context.Context) (any, error)) diagnosticsSection {
	type answer struct {
		value any
		err   error
	}
	done := make(chan answer, 1)
	go func() {
		value, err := call(ctx)
		done <- answer{value, err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			return diagnosticsSection{Name: name, Means: means + " This read failed, and the message is the one the matching page would show. Reading it again is safe; it changes nothing.", Skipped: r.text(got.err.Error())}
		}
		return diagnosticsSection{Name: name, Means: means, Value: r.through(got.value)}
	case <-ctx.Done():
		// (d): named, with the reason, and not waited on.
		return diagnosticsSection{Name: name, Means: means + " It is not in this file because reading it takes longer than the export allows; the matching page in Settings shows it in full.", Skipped: fmt.Sprintf("took longer than %s", diagnosticsProbeBound)}
	}
}

// newestMatching returns the newest file matching a glob, or "".
func newestMatching(pattern string) string {
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return ""
	}
	sort.Slice(matches, func(i, j int) bool {
		left, leftErr := os.Stat(matches[i])
		right, rightErr := os.Stat(matches[j])
		if leftErr != nil || rightErr != nil {
			return matches[i] > matches[j]
		}
		return left.ModTime().After(right.ModTime())
	})
	return matches[0]
}

// diagnostics is the route. GET only, read-only, and it elevates nothing.
func (s *Server) diagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	writeJSON(w, 200, s.diagnosticsExport())
}

func (s *Server) diagnosticsExport() map[string]any {
	dataRoot, appRoot := s.roots.Data, s.roots.Application
	red := newRedactor(dataRoot, appRoot)
	snapshot := s.snapshot()

	// (a): everything from the snapshot the server already holds. No new probe.
	fromSnapshot := func(key, means string) diagnosticsSection {
		value, present := snapshot[key]
		if !present {
			return diagnosticsSection{Name: key, Means: means, Skipped: "this installation reports no such state"}
		}
		return diagnosticsSection{Name: key, Means: means, Value: red.through(value)}
	}

	logs := filepath.Join(dataRoot, "logs")
	sections := []diagnosticsSection{
		fromSnapshot("build", "Which build is running, and whether its files are signed. The first thing to check when behaviour does not match the notes."),
		fromSnapshot("update", "What the last update check found and what the last install attempt did. An error here is the installer's own words."),
		fromSnapshot("signature", "The most recent signature verification of the running files."),
		fromSnapshot("serving_facts", "What the connected model server reported about itself when it was last tested."),
		fromSnapshot("shell_identity", "Which account tool commands run as, and why, if it is not the separate one."),
		fromSnapshot("shell_credential", "Whether a credential for the separate account is stored. Never the credential."),
		fromSnapshot("connections", "Every configured connection and its last test result. Keys are masked before they reach this export."),
		fromSnapshot("flow", "The stages a request passes through, as this build defines them."),
		fromSnapshot("tools", "The tools this build offers."),
	}
	sections = append(sections, s.diagnosticsProbes(red)...)
	sections = append(sections,
		red.tail(filepath.Join(logs, "launcher.log"), diagnosticsLogTail),
		red.tail(filepath.Join(logs, "launcher-errors.log"), diagnosticsLogTail),
	)
	if newest := newestMatching(filepath.Join(logs, "installer-*.log")); newest != "" {
		sections = append(sections, red.tail(newest, diagnosticsLogTail))
	}
	if newest := newestMatching(filepath.Join(logs, "startup-*.log")); newest != "" {
		sections = append(sections, red.tail(newest, diagnosticsLogTail))
	}
	// Item 2mt's crash records, which are the reason a crash is readable at all.
	for _, crash := range crashRecordsUnder(logs) {
		sections = append(sections, red.tail(crash, diagnosticsLogTail))
	}
	// The install marker's own outcome, which is 2mk's line.
	if progress := filepath.Join(dataRoot, "install-progress.jsonl"); progress != "" {
		sections = append(sections, red.tail(progress, 40))
	}

	return map[string]any{
		"schema":       1,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"about": "One export of this installation's state, gathered from what Agent_b already knows. " +
			"Paths outside the installation, account names, addresses, hosts and anything token-shaped are replaced before you see it, " +
			"so this file is safe to send to whoever is helping. Nothing here was measured by generating this file.",
		"sections": sections,
	}
}

// diagnosticsProbes are the three sections of (a) that live behind a read rather than
// in the snapshot. A runtime that this installation does not have says so by name.
func (s *Server) diagnosticsProbes(red *redactor) []diagnosticsSection {
	ctx, cancel := context.WithTimeout(context.Background(), diagnosticsProbeBound)
	defer cancel()

	const (
		localMeans   = "What this machine offers a local model: the graphics card, the compute runtimes and whether the interpreters are reachable by the account tools run as."
		hardenMeans  = "Whether the file permissions and the outbound firewall rule that separate the tool account from yours are actually in place. This is inspected, never applied; nothing here changes the machine."
		accountMeans = "Whether the separate low-privilege account tools run as exists and is usable. The account's name is replaced; its password is never held here or anywhere Agent_b can read it back."
	)

	// One closure per section, in the order the export declares them, so the file
	// reads the same every time however fast each one answered. A runtime this
	// installation does not have returns its own line rather than being left out.
	prepared := []func() diagnosticsSection{
		func() diagnosticsSection {
			if s.detectLocal == nil {
				return diagnosticsSection{Name: "local_detection", Means: localMeans, Skipped: "this build has no local detection"}
			}
			s.mu.RLock()
			account := s.cfg.Shell.ServiceAccount.Account
			s.mu.RUnlock()
			return red.bounded(ctx, "local_detection", localMeans, func(ctx context.Context) (any, error) {
				return s.detectLocal(ctx, account)
			})
		},
		func() diagnosticsSection {
			if s.hardening == nil {
				return diagnosticsSection{Name: "hardening", Means: hardenMeans, Skipped: "this build has no hardening runtime"}
			}
			request, err := s.hardeningRequest("")
			if err != nil {
				return diagnosticsSection{Name: "hardening", Means: hardenMeans, Skipped: red.text(err.Error())}
			}
			return red.bounded(ctx, "hardening", hardenMeans, func(ctx context.Context) (any, error) {
				return s.hardening.Status(ctx, request)
			})
		},
		func() diagnosticsSection {
			if s.account == nil {
				return diagnosticsSection{Name: "service_account", Means: accountMeans, Skipped: "this build has no service-account runtime"}
			}
			return red.bounded(ctx, "service_account", accountMeans, func(ctx context.Context) (any, error) {
				return s.account.Status(ctx, managedServiceAccount)
			})
		},
	}

	// Run at once: the three are independent, and the bound is the whole wait rather
	// than one section's share of it.
	out := make([]diagnosticsSection, len(prepared))
	var wait sync.WaitGroup
	for index, prepare := range prepared {
		wait.Add(1)
		go func(index int, prepare func() diagnosticsSection) {
			defer wait.Done()
			out[index] = prepare()
		}(index, prepare)
	}
	wait.Wait()
	return out
}

// crashRecordsUnder lists item 2mt's crash records, newest first, bounded.
func crashRecordsUnder(logs string) []string {
	entries, err := os.ReadDir(logs)
	if err != nil {
		return nil
	}
	var found []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "crash-") && strings.HasSuffix(entry.Name(), ".json") {
			found = append(found, filepath.Join(logs, entry.Name()))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(found)))
	if len(found) > 3 {
		found = found[:3]
	}
	return found
}
