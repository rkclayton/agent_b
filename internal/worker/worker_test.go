package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/events"
	"harness/internal/session"
)

const plan = `# Demo plan

## Items

- [ ] 2aa first item
- [ ] 2ab second item
- [x] 2ac already done
- [ ] 2ad fourth item

Some prose that is not an item.
`

func TestParseTakesOnlyMarkerLinesInPlanOrder(t *testing.T) {
	items := Parse(plan)
	if len(items) != 4 {
		t.Fatalf("parsed %d items, want 4", len(items))
	}
	if items[0].Text != "2aa first item" || items[2].Marker != "x" {
		t.Fatalf("items out of order or mis-parsed: %+v", items)
	}
	// Prose is not an item.
	for _, item := range items {
		if strings.Contains(item.Text, "Some prose") {
			t.Fatal("prose was parsed as an item")
		}
	}
}

func TestNextIsTheFirstWaitingItemNotTheFirstUnfinished(t *testing.T) {
	items := Parse(plan)
	item, ok := Next(items)
	if !ok || item.Text != "2aa first item" {
		t.Fatalf("next = %+v", item)
	}
	// A running item is not waiting: the worker does not pick it up twice.
	running := Parse(strings.Replace(plan, "- [ ] 2aa first item", "- [~] 2aa first item", 1))
	item, ok = Next(running)
	if !ok || item.Text != "2ab second item" {
		t.Fatalf("next past a running item = %+v", item)
	}
}

func TestSetMarkerRewritesOnlyThatLine(t *testing.T) {
	items := Parse(plan)
	next, err := SetMarker(plan, items[0], "~")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(next, "- [~] 2aa first item") {
		t.Fatalf("marker not written:\n%s", next)
	}
	if strings.Count(next, "\n") != strings.Count(plan, "\n") {
		t.Fatal("the rewrite changed the line count")
	}
	for _, untouched := range []string{"- [ ] 2ab second item", "- [x] 2ac already done", "Some prose that is not an item."} {
		if !strings.Contains(next, untouched) {
			t.Fatalf("rewrite disturbed %q", untouched)
		}
	}
}

// The worker may only mark. A line that moved under it is a refusal, not a
// best-effort write somewhere else.
func TestSetMarkerRefusesWhenTheLineMoved(t *testing.T) {
	items := Parse(plan)
	changed := strings.Replace(plan, "- [ ] 2aa first item", "- [ ] 2aa first item, edited", 1)
	if _, err := SetMarker(changed, items[0], "~"); err == nil {
		t.Fatal("expected a refusal when the line changed under the worker")
	}
}

func TestSetMarkerRefusesAnUnknownMarker(t *testing.T) {
	items := Parse(plan)
	for _, marker := range []string{"y", "", "xx"} {
		if _, err := SetMarker(plan, items[0], marker); err == nil {
			t.Fatalf("marker %q was accepted", marker)
		}
	}
}

// A second failure must not stack a second reason on the same line.
func TestStuckReasonReplacesRatherThanStacks(t *testing.T) {
	once := WithReason("2aa first item", "tool_errors")
	twice := WithReason(once, "context_exhausted")
	if strings.Count(twice, "— stuck:") != 1 {
		t.Fatalf("reason stacked: %q", twice)
	}
	if !strings.HasSuffix(twice, "context_exhausted") {
		t.Fatalf("latest reason lost: %q", twice)
	}
	if cleared := WithReason(twice, ""); cleared != "2aa first item" {
		t.Fatalf("clearing left %q", cleared)
	}
}

func TestPlanMarkWritesThroughToDisk(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &Plan{Dir: dir}
	items := Parse(plan)
	if err := p.Mark(items[0], "~", ""); err != nil {
		t.Fatal(err)
	}
	text, err := p.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "- [~] 2aa first item") {
		t.Fatalf("not marked:\n%s", text)
	}
	stuck, _ := Next(Parse(text))
	if err := p.Mark(stuck, "!", "tool_errors"); err != nil {
		t.Fatal(err)
	}
	text, _ = p.Read()
	if !strings.Contains(text, "- [!] 2ab second item  — stuck: tool_errors") {
		t.Fatalf("stuck reason not recorded:\n%s", text)
	}
}

func TestBriefTakesTheItemIDFromThePlanLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "plan", "items"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "state: live\n\n# 2aa — a title\n\n## Contract\n\nthe approach\n\n## Acceptance\n\nthe acceptance\n"
	if err := os.WriteFile(filepath.Join(dir, "plan", "items", "2aa.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	job := Brief(Parse(plan)[0], dir, `C:\repo`)
	if job.ItemID != "2aa" {
		t.Fatalf("item id = %q", job.ItemID)
	}
	if job.Acceptance != "the acceptance" || job.Approach != "the approach" {
		t.Fatalf("brief = %+v", job)
	}
	if job.Repo != `C:\repo` {
		t.Fatalf("repo = %q", job.Repo)
	}
}

// Acceptance is never self-declared: only a clean finish can produce [x].
func TestOnlyACleanFinishProducesADoneMarker(t *testing.T) {
	for reason, want := range map[string]string{
		"done":              "x",
		"tool_errors":       "!",
		"context_exhausted": "!",
		"cycle":             "!",
		"turn_ceiling":      "!",
		"wall_clock":        "!",
		"model_error":       "!",
		"":                  "!",
	} {
		outcome := classify("2aa", stoppedEvent(reason, "detail text"))
		if outcome.Marker != want {
			t.Errorf("reason %q produced marker %q, want %q", reason, outcome.Marker, want)
		}
		if want == "!" && outcome.Reason == "" {
			t.Errorf("reason %q produced no stuck reason", reason)
		}
	}
}

func stoppedEvent(reason, detail string) events.Event {
	return events.Event{Type: events.RunStopped, SessionID: "s1", Data: map[string]any{"reason": reason, "detail": detail}}
}

// Routing: a bound d-session that can answer from the plan gets the question;
// otherwise it is the operator's.
func TestRouteGoesToDWhenOneIsBoundAndToTheOperatorOtherwise(t *testing.T) {
	planned := &session.Session{ID: "d1", Role: "d", PlanID: "p1"}
	other := &session.Session{ID: "d2", Role: "d", PlanID: "p2"}
	chat := &session.Session{ID: "b1", Role: "b"}
	closed := &session.Session{ID: "d3", Role: "d", PlanID: "p1", Closed: true}

	if got := Route([]*session.Session{planned, chat}, "p1"); got != "d" {
		t.Errorf("a bound d-session should answer, got %q", got)
	}
	if got := Route([]*session.Session{other, chat}, "p1"); got != "operator" {
		t.Errorf("a d-session on another plan cannot answer, got %q", got)
	}
	if got := Route([]*session.Session{closed}, "p1"); got != "operator" {
		t.Errorf("a closed d-session cannot answer, got %q", got)
	}
	if got := Route(nil, "p1"); got != "operator" {
		t.Errorf("no sessions means the operator, got %q", got)
	}
}
