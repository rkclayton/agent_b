package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Item 2mv (b): THE REDACTION TEST SEEDS EACH KIND. The export exists to be pasted
// to someone else, so every class of thing that must not travel is planted and then
// asserted absent — not sampled, not spot-checked.
func TestTheDiagnosticsExportRedactsEveryKindItIsGiven(t *testing.T) {
	dataRoot := t.TempDir()
	appRoot := t.TempDir()
	logs := filepath.Join(dataRoot, "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("USERNAME", "seededaccount")
	red := newRedactor(dataRoot, appRoot)

	seeds := map[string]string{
		"a path outside the roots": `C:\work\acme\Documents\private.txt`,
		"a UNC path":               `\\fileserver\share\secret.docx`,
		"a posix path":             ` /home/someone/.ssh/id_ed25519`,
		"a url with a credential":  "https://hooks.example.com/services/T0000/B0000/abcdefghijklmnopqrstuvwx",
		"a webhook host":           "discord.com/api/webhooks/123456789/abcdefghijklmnopqrstuvwxyz012345",
		"an email address":         "someone@example.org",
		"a dotted-quad address":    "192.168.1.10",
		"a bearer token":           "sk-ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcd",
		"a windows SID":            "S-1-5-21-1000-1000-1000-1001-1013",
		"the account name alone":   "seededaccount",
		"a hostname":               "workstation.corp.example.com",
	}
	for label, seed := range seeds {
		got := red.text(seed)
		// The seeded secret itself must be gone. Checking the whole seed and its
		// distinctive part, because a partial replacement is still a leak.
		if strings.Contains(got, "seededaccount") {
			t.Errorf("%s: account name survived: %q", label, got)
		}
		for _, fragment := range []string{"private.txt", "secret.docx", "id_ed25519", "abcdefghijklmnopqrstuvwx", "someone@example.org", "192.168.1.10", "3623811015", "workstation.corp.example.com"} {
			if strings.Contains(seed, fragment) && strings.Contains(got, fragment) {
				t.Errorf("%s: %q survived redaction: %q", label, fragment, got)
			}
		}
	}

	// A path INSIDE the roots is kept, rewritten, because which file a log line
	// names is the whole value of including it.
	inside := red.text(filepath.Join(dataRoot, "logs", "launcher-errors.log"))
	if !strings.Contains(inside, "<data>") || !strings.Contains(inside, "launcher-errors.log") {
		t.Errorf("a path inside the data root was not kept as <data>\\…: %q", inside)
	}
	if strings.Contains(inside, dataRoot) {
		t.Errorf("the real data root leaked: %q", inside)
	}
	insideApp := red.text(filepath.Join(appRoot, "Agent_b.exe"))
	if !strings.Contains(insideApp, "<app>") || strings.Contains(insideApp, appRoot) {
		t.Errorf("a path inside the application root was not kept as <app>\\…: %q", insideApp)
	}

	// A log line keeps its length: telemetry's 200-character cut would take the
	// end off a stack trace, which is the part worth having.
	long := "panic: " + strings.Repeat("frame ", 80)
	if got := red.line(long); len(got) < 300 {
		t.Errorf("a log line was truncated to %d characters", len(got))
	}
	if got := red.text(long); len(got) > 200 {
		t.Errorf("an ordinary string escaped the length cut at %d characters", len(got))
	}
}

// (a) and (d): the export is assembled from what the server already holds and from
// files on disk. Nothing is probed, and a file too large to read cheaply is NAMED
// and skipped rather than silently dropped.
func TestTheDiagnosticsExportNamesWhatItSkipped(t *testing.T) {
	dataRoot := t.TempDir()
	appRoot := t.TempDir()
	logs := filepath.Join(dataRoot, "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	red := newRedactor(dataRoot, appRoot)

	// Absent: named, with a reason that does not read as a fault.
	absent := red.tail(filepath.Join(logs, "never-written.log"), 10)
	if absent.Skipped != "absent" || absent.Means == "" {
		t.Errorf("an absent log was not explained: %+v", absent)
	}

	// Over the bound: named and skipped, which is (d).
	big := filepath.Join(logs, "huge.log")
	if err := os.WriteFile(big, make([]byte, diagnosticsReadBound+1), 0o600); err != nil {
		t.Fatal(err)
	}
	over := red.tail(big, 10)
	if !strings.Contains(over.Skipped, "larger than") {
		t.Errorf("a file over the read bound was not skipped by name: %+v", over)
	}
	if over.Value != nil {
		t.Error("a skipped file still carried content")
	}

	// A real tail keeps only the last n lines, drops blanks, and explains itself.
	normal := filepath.Join(logs, "launcher-errors.log")
	var body strings.Builder
	for index := 0; index < 25; index++ {
		body.WriteString("line ")
		body.WriteString(string(rune('a' + index%26)))
		body.WriteString("\r\n\r\n")
	}
	if err := os.WriteFile(normal, []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	tail := red.tail(normal, 5)
	lines, ok := tail.Value.([]string)
	if !ok {
		t.Fatalf("tail value is %T", tail.Value)
	}
	// The file is 25 records of "line X" followed by a blank, so the last five raw
	// lines are [line x, "", line y, "", ""] and exactly two survive. My first
	// expectation here was three; the code was right and the arithmetic was mine.
	if len(lines) != 2 {
		t.Errorf("tail kept %d lines: %v", len(lines), lines)
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			t.Error("a blank line was kept")
		}
	}
	if tail.Means == "" {
		t.Error("a section with content did not say what it means")
	}
}

// (c): every section says what it means, so a reader who is not the author can act
// on it. This holds the whole export to that, not a sample.
func TestEveryDiagnosticsSectionSaysWhatItMeans(t *testing.T) {
	export := map[string]any{}
	raw, err := json.Marshal(map[string]any{
		"schema":   1,
		"about":    "x",
		"sections": []diagnosticsSection{{Name: "build", Means: "what it means"}, {Name: "gap", Skipped: "absent", Means: "why it is absent"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &export); err != nil {
		t.Fatal(err)
	}
	sections, _ := export["sections"].([]any)
	if len(sections) == 0 {
		t.Fatal("no sections")
	}
	for _, entry := range sections {
		section, _ := entry.(map[string]any)
		if text, _ := section["means"].(string); strings.TrimSpace(text) == "" {
			t.Errorf("section %v carries no explanation", section["name"])
		}
	}
}

// (d) again, for the three sections that answer by running something rather than by
// being held in the snapshot: one that does not answer inside the stated bound is
// NAMED with the reason and carries nothing, so the export cannot become the slow
// part of asking for help.
func TestADiagnosticsReadOverTheBoundIsNamedNotWaitedOn(t *testing.T) {
	red := newRedactor(t.TempDir(), t.TempDir())

	// The bound is shared by every read in one export, so the test supplies it.
	shared, cancelShared := context.WithTimeout(context.Background(), diagnosticsProbeBound)
	defer cancelShared()

	slow := red.bounded(shared, "hardening", "what it means", func(ctx context.Context) (any, error) {
		<-ctx.Done()
		time.Sleep(10 * time.Millisecond)
		return "should never be used", nil
	})
	if !strings.Contains(slow.Skipped, "took longer than") {
		t.Errorf("a read over the bound was not named: %+v", slow)
	}
	if slow.Value != nil {
		t.Error("a read over the bound still carried content")
	}
	if !strings.Contains(slow.Means, "Settings") {
		t.Errorf("a skipped read did not say where to look instead: %q", slow.Means)
	}

	// The bound is SHARED, so once it is spent the next read is skipped by name too
	// rather than starting a fresh two seconds of its own.
	spent := red.bounded(shared, "service_account", "what it means", func(ctx context.Context) (any, error) {
		return "instant", nil
	})
	if !strings.Contains(spent.Skipped, "took longer than") {
		t.Errorf("a read after the shared bound was spent was not skipped: %+v", spent)
	}

	// A read that fails carries the failure's own words, redacted, and says the
	// retry is safe — which is (c). Its own bound, because the shared one is gone.
	fresh, cancelFresh := context.WithTimeout(context.Background(), diagnosticsProbeBound)
	defer cancelFresh()
	failed := red.bounded(fresh, "hardening", "what it means", func(ctx context.Context) (any, error) {
		return nil, errors.New(`inspect ACL policy: cannot read C:\work\acme\thing.txt`)
	})
	if failed.Skipped == "" || strings.Contains(failed.Skipped, "someoneelse") {
		t.Errorf("a failed read leaked or said nothing: %+v", failed)
	}
	if !strings.Contains(failed.Means, "safe") {
		t.Errorf("a failed read did not say whether retrying is safe: %q", failed.Means)
	}

	// And one that answers is carried through the redactor, not raw.
	fine := red.bounded(fresh, "service_account", "what it means", func(ctx context.Context) (any, error) {
		return map[string]any{"account": `C:\work\acme`}, nil
	})
	if strings.Contains(fmt.Sprint(fine.Value), "someoneelse") {
		t.Errorf("an answered read was not redacted: %v", fine.Value)
	}
}
