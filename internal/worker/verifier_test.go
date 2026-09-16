package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/events"
	"harness/internal/session"
)

func onePlan(t *testing.T, line string) *Plan {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte("# One\n\n"+line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Plan{Dir: dir}
}

// collect gathers what the bus carried. Publishing is synchronous into a
// buffered channel, so once Go has returned everything it published is waiting.
func collect(bus *events.Bus) (func() []events.Event, func()) {
	stream, release := bus.Subscribe()
	return func() []events.Event {
		release()
		var got []events.Event
		for {
			select {
			case event := <-stream:
				got = append(got, event)
			default:
				return got
			}
		}
	}, release
}

// An item that names no verifier is never started: it is stuck as "no verifier
// named", and the worker asks the planner for one with a proposal the tray shows.
func TestItemWithNoVerifierIsStuckAndProposesOne(t *testing.T) {
	bus := events.NewBus()
	finish, _ := collect(bus)
	worker := &session.Session{ID: "c1", Role: "c", PlanID: "p1"}
	planner := &session.Session{ID: "d1", Role: "d", PlanID: "p1"}
	fake := newFake(bus, worker)
	plan := onePlan(t, "- [ ] 2aa first item")
	writeItem(t, plan.Dir, "2aa", "kind: defect")
	verifier := &fakeVerifier{}
	driver := New(bus, fake, func() []*session.Session { return []*session.Session{planner} })
	driver.SetVerifier(verifier)
	summary, err := driver.Go(context.Background(), worker, plan, `C:\repo`)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := plan.Read()
	if !strings.Contains(text, "- [!] 2aa first item  — stuck: no verifier named") {
		t.Fatalf("plan.md:\n%s", text)
	}
	if fake.runs != 0 || len(verifier.calls) != 0 {
		t.Fatalf("an item with no verifier was started: runs=%d verifier calls=%v", fake.runs, verifier.calls)
	}
	if summary.Done != 0 || summary.Stuck != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	var proposal map[string]any
	var posted string
	for _, event := range finish() {
		if event.Type == events.WorkerJob {
			data, _ := event.Data.(map[string]any)
			proposal, _ = data["proposal"].(map[string]any)
			posted = event.SessionID
		}
	}
	if proposal == nil || proposal["kind"] != "verifier" || proposal["path"] != "plan/items/2aa.md" || proposal["item_id"] != "2aa" || proposal["old_text"] != "2aa first item" {
		t.Fatalf("no verifier proposal for the planner: %v", proposal)
	}
	if posted != planner.ID {
		t.Fatalf("the proposal went to %q, want the planner's thread", posted)
	}
}

// A verifier that fails leaves the item [~] while it runs and [!] with the
// failure after; a clean stop does not rescue it.
func TestFailingVerifierMarksStuckWithTheFailure(t *testing.T) {
	bus := events.NewBus()
	worker := &session.Session{ID: "c1", Role: "c", PlanID: "p1"}
	plan := onePlan(t, "- [ ] 2aa first item")
	writeItem(t, plan.Dir, "2aa", "verify: go test ./broken")
	var during string
	verifier := &fakeVerifier{check: func(command string) (bool, string) {
		during, _ = plan.Read()
		return false, "command failed\nexit=1\n--- FAIL: TestBroken"
	}}
	driver := New(bus, newFake(bus, worker), nil)
	driver.SetVerifier(verifier)
	if _, err := driver.Go(context.Background(), worker, plan, `C:\repo`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(during, "- [~] 2aa first item") {
		t.Fatalf("the item was not [~] while its verifier ran:\n%s", during)
	}
	text, _ := plan.Read()
	if !strings.Contains(text, "- [!] 2aa first item  — stuck: verifier failed: command failed exit=1 --- FAIL: TestBroken") {
		t.Fatalf("plan.md:\n%s", text)
	}
	if len(verifier.calls) != 1 || verifier.calls[0] != "go test ./broken" {
		t.Fatalf("verifier calls = %v", verifier.calls)
	}
}

// [x] follows the verifier's exit 0 and nothing else: not a clean stop, not the
// word "verify:" in the item's prose, and not a missing verifier implementation.
func TestPassingVerifierIsTheOnlyWayToDone(t *testing.T) {
	bus := events.NewBus()
	worker := &session.Session{ID: "c1", Role: "c", PlanID: "p1"}
	plan := onePlan(t, "- [ ] 2aa first item")
	writeItem(t, plan.Dir, "2aa", "verify: pass")

	// No verifier wired: a clean stop still cannot mark done.
	unwired := New(bus, newFake(bus, worker), nil)
	if _, err := unwired.Go(context.Background(), worker, plan, `C:\repo`); err != nil {
		t.Fatal(err)
	}
	text, _ := plan.Read()
	if !strings.Contains(text, "- [!] 2aa first item  — stuck: no verifier available") {
		t.Fatalf("a clean stop without a verifier reached:\n%s", text)
	}

	plan = onePlan(t, "- [ ] 2aa first item")
	writeItem(t, plan.Dir, "2aa", "verify: pass")
	verifier := &fakeVerifier{}
	driver := New(bus, newFake(bus, worker), nil)
	driver.SetVerifier(verifier)
	summary, err := driver.Go(context.Background(), worker, plan, `C:\repo`)
	if err != nil {
		t.Fatal(err)
	}
	text, _ = plan.Read()
	if !strings.Contains(text, "- [x] 2aa first item") || summary.Done != 1 {
		t.Fatalf("a passing verifier did not mark done: %+v\n%s", summary, text)
	}
	if len(verifier.calls) != 1 || verifier.calls[0] != "pass" {
		t.Fatalf("the verifier came from somewhere other than the header: %v", verifier.calls)
	}
}

// The fixture that changes nothing: the model stops cleanly having written
// nothing, and the verifier checks for the file the item asks for. It can never
// reach [x].
func TestAChangesNothingFixtureNeverReachesDone(t *testing.T) {
	bus := events.NewBus()
	repo := t.TempDir()
	worker := &session.Session{ID: "c1", Role: "c", PlanID: "p1"}
	plan := onePlan(t, "- [ ] 2aa write NOTICE")
	writeItem(t, plan.Dir, "2aa", "verify: test -f NOTICE")
	fake := newFake(bus, worker)
	fake.said["2aa write NOTICE"] = "Done — NOTICE is written."
	verifier := &fakeVerifier{check: func(command string) (bool, string) {
		if _, err := os.Stat(filepath.Join(repo, "NOTICE")); err != nil {
			return false, "command failed\nexit=1"
		}
		return true, "exit=0"
	}}
	driver := New(bus, fake, nil)
	driver.SetVerifier(verifier)
	summary, err := driver.Go(context.Background(), worker, plan, repo)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := plan.Read()
	if strings.Contains(text, "[x]") || summary.Done != 0 {
		t.Fatalf("a run that changed nothing reached done: %+v\n%s", summary, text)
	}
	if !strings.Contains(text, "- [!] 2aa write NOTICE  — stuck: verifier failed: command failed exit=1") {
		t.Fatalf("plan.md:\n%s", text)
	}
}

// An item stuck only for want of a verifier is taken again once it names one,
// and starts clean.
func TestItemIsRetriedOnceItNamesAVerifier(t *testing.T) {
	bus := events.NewBus()
	worker := &session.Session{ID: "c1", Role: "c", PlanID: "p1"}
	plan := onePlan(t, "- [!] 2aa first item  — stuck: no verifier named")
	writeItem(t, plan.Dir, "2aa", "kind: defect")
	items, _ := plan.Read()
	if RemainingIn(Parse(items), plan.Dir) {
		t.Fatal("an item with still no verifier is offered to Go")
	}
	writeItem(t, plan.Dir, "2aa", "verify: pass")
	if !RemainingIn(Parse(items), plan.Dir) {
		t.Fatal("an item that now names a verifier is not offered to Go")
	}
	driver := New(bus, newFake(bus, worker), nil)
	driver.SetVerifier(&fakeVerifier{})
	done := make(chan Summary, 1)
	go func() {
		summary, _ := driver.Go(context.Background(), worker, plan, `C:\repo`)
		done <- summary
	}()
	select {
	case summary := <-done:
		if summary.Done != 1 {
			t.Fatalf("summary = %+v", summary)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the retry did not finish")
	}
	text, _ := plan.Read()
	if !strings.Contains(text, "- [x] 2aa first item\n") {
		t.Fatalf("plan.md:\n%s", text)
	}
}
