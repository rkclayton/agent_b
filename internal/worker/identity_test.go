package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/events"
	"harness/internal/session"
)

// Existing plans gain ids on first touch, once, and nothing else on the line or
// in the file moves; an item with a leading item-file id keeps it, a duplicate
// and a line with no id get free numbers.
func TestExistingPlanGainsIDsOnFirstTouchAndOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	writeItem(t, dir, "1", "verify: pass")
	before := "# Plan\r\n\r\nprose [ ] not an item\r\n- [ ] 2aa first item\r\n- [x] 2aa first item\r\n* [!] no id here  — stuck: tool_errors\r\n- [ ] [[7]] already named\r\n"
	after := AssignIDs(before, dir)
	want := "# Plan\r\n\r\nprose [ ] not an item\r\n- [ ] [[2aa]] 2aa first item\r\n- [x] [[2]] 2aa first item\r\n* [!] [[3]] no id here  — stuck: tool_errors\r\n- [ ] [[7]] already named\r\n"
	if after != want {
		t.Fatalf("first touch:\n%q\nwant\n%q", after, want)
	}
	if again := AssignIDs(after, dir); again != after {
		t.Fatalf("a second touch changed the plan:\n%q", again)
	}
	for index, item := range Parse(after) {
		original := Parse(before)[index]
		if item.Text != original.Text || item.Marker != original.Marker || item.Index != original.Index {
			t.Fatalf("item %d changed beyond its id: %+v vs %+v", index, item, original)
		}
	}
}

// Two items with identical text resolve independently: each marker lands on
// its own line, whichever order they are marked in.
func TestIdenticalItemsResolveIndependentlyByID(t *testing.T) {
	dir := t.TempDir()
	plan := &Plan{Dir: dir}
	if err := os.WriteFile(plan.Path(), []byte("- [ ] same words\n- [ ] same words\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := plan.AssignIDs(); err != nil {
		t.Fatal(err)
	}
	text, _ := plan.Read()
	items := Parse(text)
	if len(items) != 2 || items[0].ID == items[1].ID {
		t.Fatalf("ids = %+v", items)
	}
	if err := plan.Mark(items[1], "!", "second one"); err != nil {
		t.Fatal(err)
	}
	if err := plan.Mark(items[0], "x", ""); err != nil {
		t.Fatal(err)
	}
	text, _ = plan.Read()
	want := "- [x] [[" + items[0].ID + "]] same words\n- [!] [[" + items[1].ID + "]] same words  — stuck: second one\n"
	if text != want {
		t.Fatalf("plan.md = %q, want %q", text, want)
	}
}

// A marker finds its item by id even after the planner inserted a line above it
// and reworded it while the worker ran.
func TestMarkerFollowsTheIDThroughEditsWhileTheItemRan(t *testing.T) {
	bus := events.NewBus()
	worker := &session.Session{ID: "c1", Role: "c", PlanID: "p1"}
	plan := onePlan(t, "- [ ] 2aa first item")
	writeItem(t, plan.Dir, "2aa", "verify: pass")
	verifier := &fakeVerifier{check: func(string) (bool, string) {
		// The planner's accepted edit lands mid-item, through the same writer.
		_ = session.UpdatePlanFile(plan.Path(), func(current string, _ bool) (string, error) {
			return strings.Replace(current, "# One\n", "# One\n- [ ] 2ab inserted above\n", 1), nil
		})
		_ = session.UpdatePlanFile(plan.Path(), func(current string, _ bool) (string, error) {
			return strings.Replace(current, "2aa first item", "2aa first item, reworded", 1), nil
		})
		return true, "exit=0"
	}}
	driver := New(bus, newFake(bus, worker), nil)
	driver.SetVerifier(verifier)
	writeItem(t, plan.Dir, "2ab", "kind: defect")
	if _, err := driver.Go(context.Background(), worker, plan, `C:\repo`); err != nil {
		t.Fatal(err)
	}
	text, _ := plan.Read()
	if !strings.Contains(text, "- [x] [[2aa]] 2aa first item, reworded\n") {
		t.Fatalf("the marker did not follow its id:\n%s", text)
	}
	if !strings.Contains(text, "- [!] [[2ab]] 2ab inserted above  — stuck: no verifier named") {
		t.Fatalf("the inserted item was not taken in its turn:\n%s", text)
	}
	if _, err := os.Stat(filepath.Join(plan.Dir, "plan.md")); err != nil {
		t.Fatal(err)
	}
}
