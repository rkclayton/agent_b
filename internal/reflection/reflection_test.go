package reflection

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Item 17-i, v1.1.0/W2. The store keeps a run's summary, reads back the recent
// ones, and prunes at the retention window; overviews and reports are kept.
func TestTheStoreKeepsSummariesOverviewsAndReports(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	if _, err := store.PutSummary(Summary{At: now, SessionID: "s1", RunID: "r1", Workspace: `C:\ws`, PlanID: "p1", Connection: "homepc", Read: []string{"a.go", "b.go"}, Written: []string{"a.go"}, Changed: "tightened the guard", Open: "the eval is carded", Text: "one run"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutSummary(Summary{At: now.Add(-40 * 24 * time.Hour), SessionID: "s0", RunID: "r0", Connection: "homepc", Text: "old"}); err != nil {
		t.Fatal(err)
	}
	all, err := store.Summaries(time.Time{}, 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("summaries=%d err=%v", len(all), err)
	}
	if all[0].RunID != "r1" || all[0].Changed != "tightened the guard" || len(all[0].Read) != 2 || all[0].Read[1] != "b.go" {
		t.Fatalf("newest summary came back as %+v", all[0])
	}
	recent, err := store.Summaries(now.Add(-Retention), 0)
	if err != nil || len(recent) != 1 {
		t.Fatalf("recent=%d err=%v", len(recent), err)
	}
	pruned, err := store.Prune(now)
	if err != nil || pruned != 1 {
		t.Fatalf("pruned=%d err=%v", pruned, err)
	}
	if _, err := store.PutOverview(Overview{PlanID: "p1", Tier: "tier 1 (go list)", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutOverview(Overview{PlanID: "p1", Tier: "tier 1 (go list)", Text: "second"}); err != nil {
		t.Fatal(err)
	}
	overviews, err := store.Overviews("p1", 0)
	if err != nil || len(overviews) != 2 || overviews[0].Text != "second" {
		t.Fatalf("overviews=%+v err=%v", overviews, err)
	}
	if _, err := store.PutReport(Report{Text: "report", Clusters: []Cluster{{Tool: "shell", Shape: "go test PATH", Count: 3, Failures: 2}}}); err != nil {
		t.Fatal(err)
	}
	report, err := store.LatestReport()
	if err != nil || report.Text != "report" || len(report.Clusters) != 1 || report.Clusters[0].Tool != "shell" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestAFreshStoreHasNoReport(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	report, err := store.LatestReport()
	if err != nil || report.Text != "" || report.ID != 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

// Normalisation strips the accidental constants so two calls that differ only
// in their file or path collapse to one shape.
func TestNormalisationCollapsesAccidentalConstants(t *testing.T) {
	if a, b := normalize(`node --check game.js`), normalize(`node --check js/game.js`); a != b {
		t.Fatalf("%q != %q", a, b)
	}
	for _, row := range []struct{ text, want string }{
		{`C:\Go\bin\go.exe test ./logic -run ^TestOne$ -v`, `PATH test PATH -run ^TestOne$ -v`},
		{`git commit -m "a message"`, `git commit -m "S"`},
		{`Get-Content C:\Windows\win.ini`, `Get-Content PATH`},
	} {
		if got := normalize(row.text); got != row.want {
			t.Fatalf("normalize(%q) = %q, want %q", row.text, got, row.want)
		}
	}
}

// The pipeline ranks frequent-and-failing first, names the cause, and contrasts
// the failures against the successes. No model is involved.
func TestClustersRankFrequentAndFailingFirstWithTheContrast(t *testing.T) {
	calls := []Call{}
	for i := 0; i < 6; i++ {
		calls = append(calls, Call{Tool: "shell", Text: `cd x && go test ./logic`, Shape: normalize(`cd x && go test ./logic`), OK: false, Failure: "PowerShell 5.1 rejects && or ||"})
	}
	for i := 0; i < 2; i++ {
		calls = append(calls, Call{Tool: "shell", Text: `cd x; go test ./logic`, Shape: normalize(`cd x && go test ./logic`), OK: true})
	}
	for i := 0; i < 20; i++ {
		calls = append(calls, Call{Tool: "read_file", Text: "a.go", Shape: normalize("a.go"), OK: true})
	}
	clusters := Clusters(calls)
	if len(clusters) < 2 {
		t.Fatalf("clusters=%+v", clusters)
	}
	first := clusters[0]
	if first.Tool != "shell" || first.Count != 8 || first.Failures != 6 {
		t.Fatalf("first cluster = %+v", first)
	}
	if len(first.FailureCauses) != 1 || !strings.Contains(first.FailureCauses[0], "PowerShell 5.1 rejects") {
		t.Fatalf("causes = %v", first.FailureCauses)
	}
	if !strings.Contains(first.Contrast, "only in the failures") || !strings.Contains(first.Contrast, "&&") {
		t.Fatalf("contrast = %q", first.Contrast)
	}
	// The frequent, reliable cluster is not a candidate.
	if !strings.Contains(ReportText(clusters, 5), "shell") || strings.Contains(ReportText(clusters, 5), "read_file") {
		t.Fatalf("report = %q", ReportText(clusters, 5))
	}
}

func TestCallsAreReadFromRecordedJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s1.jsonl")
	lines := []string{
		`{"session_id":"s1","type":"tool.call","data":{"call_id":"1","name":"shell","args":{"command":"cd x && dir"}}}`,
		`{"session_id":"s1","type":"tool.result","data":{"call_id":"1","name":"shell","ok":false,"preview":"error: command failed\nThe token '&&' is not a valid statement separator in this version."}}`,
		`{"session_id":"s1","type":"tool.call","data":{"call_id":"2","name":"read_file","args":{"path":"C:\\ws\\a.go"}}}`,
		`{"session_id":"s1","type":"tool.result","data":{"call_id":"2","name":"read_file","ok":true}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls, err := CallsFromJSONL([]string{path, filepath.Join(dir, "missing.jsonl")})
	if err != nil || len(calls) != 2 {
		t.Fatalf("calls=%+v err=%v", calls, err)
	}
	if calls[0].Tool != "shell" || calls[0].OK || calls[0].Failure != "PowerShell 5.1 rejects && or ||" {
		t.Fatalf("first call = %+v", calls[0])
	}
	if !calls[1].OK || calls[1].Shape != "PATH" {
		t.Fatalf("second call = %+v", calls[1])
	}
}
