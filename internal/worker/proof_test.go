package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"harness/internal/events"
	"harness/internal/session"
)

// fakeSubmitter stands in for the scheduler: it answers each item with a
// scripted stop reason so the whole loop — order, markers, events, routing, the
// done card and Stop — is proved without a model.
type fakeSubmitter struct {
	mu       sync.Mutex
	bus      *events.Bus
	answers  map[string]string
	said     map[string]string
	order    []string
	runs     int
	blockOn  string
	session  *session.Session
	released chan struct{}
}

func (f *fakeSubmitter) Submit(ctx context.Context, sessionID, text string) (string, error) {
	f.mu.Lock()
	f.runs++
	runID := "r" + string(rune('0'+f.runs))
	f.order = append(f.order, text)
	reason := f.answers[text]
	if reason == "" {
		reason = "done"
	}
	said := f.said[text]
	block := f.blockOn == text
	f.mu.Unlock()

	go func() {
		if block {
			select {
			case <-f.released:
			case <-ctx.Done():
			}
		}
		if said != "" {
			f.session.Append(events.Message{ID: "m-" + runID, Role: "assistant", Category: "history", Content: said})
		}
		f.bus.Publish(events.New(events.RunStopped, f.session.ID, runID, map[string]any{"reason": reason, "detail": ""}))
	}()
	return runID, nil
}

func (f *fakeSubmitter) Stop(sessionID string, all bool) int {
	select {
	case <-f.released:
	default:
		close(f.released)
	}
	return 1
}

var _ Submitter = (*fakeSubmitter)(nil)

func newFake(bus *events.Bus, s *session.Session) *fakeSubmitter {
	return &fakeSubmitter{bus: bus, answers: map[string]string{}, said: map[string]string{}, released: make(chan struct{}), session: s}
}

const proofPlan = `# Worker proof plan

- [ ] 2aa first item
- [ ] 2ab seeded stuck item
- [ ] 2ac seeded question item
`

func newProofPlan(t *testing.T) *Plan {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte(proofPlan), 0o600); err != nil {
		t.Fatal(err)
	}
	// Every proof item names a verifier, so [x] is reachable at all.
	for _, id := range []string{"2aa", "2ab", "2ac"} {
		writeItem(t, dir, id, "verify: pass")
	}
	return &Plan{Dir: dir}
}

func writeItem(t *testing.T, dir, id, header string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "plan", "items"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "state: live\n" + header + "\n\n# " + id + "\n\n## Acceptance\n\nverify: not-the-header\n"
	if err := os.WriteFile(filepath.Join(dir, "plan", "items", id+".md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeVerifier passes the command "pass", fails anything else, and records what
// it was asked to run, so a test can see [x] follow the verifier and nothing else.
type fakeVerifier struct {
	mu    sync.Mutex
	calls []string
	check func(command string) (bool, string)
}

func (f *fakeVerifier) Verify(_ context.Context, _ *session.Session, command string) (bool, string) {
	f.mu.Lock()
	f.calls = append(f.calls, command)
	f.mu.Unlock()
	if f.check != nil {
		return f.check(command)
	}
	if command == "pass" {
		return true, "exit=0"
	}
	return false, "command failed\nexit=1\nassertion failed"
}

func verified(driver *Driver) *Driver {
	driver.SetVerifier(&fakeVerifier{})
	return driver
}

func TestAutoContinueCapsCodeOrdersAndStopsBeforeUIScope(t *testing.T) {
	for _, test := range []struct {
		name, secondSurfaces string
		limit, wantRuns      int
		wantStopped          bool
	}{{"cap", "run-loop, tests", 2, 2, false}, {"scope", "ui, tests", -1, 1, true}} {
		t.Run(test.name, func(t *testing.T) {
			plan := newProofPlan(t)
			_ = os.WriteFile(filepath.Join(plan.Dir, "plan.md"), []byte("# P\n\n- [ ] [[2aa]] first\n- [ ] [[2ab]] second\n"), 0o600)
			writeItem(t, plan.Dir, "2aa", "kind: feature\nverify: pass\nsurfaces: run-loop, tests")
			writeItem(t, plan.Dir, "2ab", "kind: defect\nverify: pass\nsurfaces: "+test.secondSurfaces)
			worker := &session.Session{ID: "c", Role: "c", PlanID: "p"}
			fake := newFake(events.NewBus(), worker)
			driver := verified(New(fake.bus, fake, func() []*session.Session { return nil }))
			summary, err := driver.GoAuto(context.Background(), worker, plan, `C:\repo`, test.limit)
			if err != nil || len(fake.order) != test.wantRuns || summary.Stopped != test.wantStopped {
				t.Fatalf("runs=%d summary=%+v err=%v", len(fake.order), summary, err)
			}
			notes, _ := os.ReadFile(filepath.Join(plan.Dir, "NOTES.md"))
			if strings.Count(string(notes), "planner:") < test.wantRuns*2 {
				t.Fatalf("planner notes=%q", notes)
			}
		})
	}
}

// The whole loop, on a three-item plan with a seeded stuck item and a seeded
// question: order, markers, events, the thread post, both routings, the done
// card's numbers, and the plan-text refusal.
func TestWorkerProof(t *testing.T) {
	bus := events.NewBus()
	stream, release := bus.Subscribe()
	defer release()
	received := []events.Event{}
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		for event := range stream {
			mu.Lock()
			received = append(received, event)
			mu.Unlock()
			if event.Type == events.PlanDone {
				close(done)
				return
			}
		}
	}()

	worker := &session.Session{ID: "c1", Role: "c", PlanID: "p1"}
	planner := &session.Session{ID: "d1", Role: "d", PlanID: "p1"}
	fake := newFake(bus, worker)
	fake.answers["2ab seeded stuck item"] = "tool_errors"
	fake.answers["2ac seeded question item"] = "context_exhausted"
	fake.said["2ac seeded question item"] = "Which database should the cache use?"

	plan := newProofPlan(t)
	driver := verified(New(bus, fake, func() []*session.Session { return []*session.Session{planner} }))
	summary, err := driver.Go(context.Background(), worker, plan, `C:\repo`)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("plan.done never arrived")
	}

	// Order: items ran in plan order, one at a time.
	if strings.Join(fake.order, "|") != "2aa first item|2ab seeded stuck item|2ac seeded question item" {
		t.Fatalf("items ran out of order: %v", fake.order)
	}

	// Markers: [x] for the clean finish, [!] with the reason for the others.
	text, _ := plan.Read()
	for _, want := range []string{
		"- [x] [[2aa]] 2aa first item",
		"- [!] [[2ab]] 2ab seeded stuck item  — stuck: tool_errors",
		"- [!] [[2ac]] 2ac seeded question item  — stuck: context_exhausted",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("plan.md is missing %q:\n%s", want, text)
		}
	}

	// Events: item.done once, item.stuck twice, plan.done once.
	mu.Lock()
	defer mu.Unlock()
	counts := map[string]int{}
	var job events.Event
	for _, event := range received {
		counts[event.Type]++
		if event.Type == events.WorkerJob {
			job = event
		}
	}
	if counts[events.ItemDone] != 1 || counts[events.ItemStuck] != 2 || counts[events.PlanDone] != 1 {
		t.Errorf("events = %v", counts)
	}

	// The thread post carries the worker's own words, routed to the bound d.
	data, _ := job.Data.(map[string]any)
	if data["question"] != "Which database should the cache use?" {
		t.Errorf("c.job question = %v", data["question"])
	}
	if data["routed_to"] != "d" {
		t.Errorf("routed to %v, want d", data["routed_to"])
	}
	// It is posted IN that thread, tagged as the worker speaking.
	if job.SessionID != planner.ID {
		t.Errorf("the question was posted on %q, want the planner's thread %q", job.SessionID, planner.ID)
	}
	if data["role"] != "c" || data["worker"] != worker.ID {
		t.Errorf("the post is not attributed to the worker: %v", data)
	}

	// The done card's numbers.
	if summary.Done != 1 || summary.Stuck != 2 || summary.Waiting != 0 || summary.Stopped {
		t.Errorf("summary = %+v", summary)
	}
	if len(summary.Reasons) != 2 {
		t.Errorf("reasons = %v", summary.Reasons)
	}
}

// Without a bound d-session the question is the operator's.
func TestWorkerQuestionRoutesToTheOperatorWithoutABoundPlanner(t *testing.T) {
	bus := events.NewBus()
	stream, release := bus.Subscribe()
	defer release()
	got := make(chan string, 4)
	go func() {
		for event := range stream {
			if event.Type == events.WorkerJob {
				data, _ := event.Data.(map[string]any)
				routed, _ := data["routed_to"].(string)
				if event.SessionID != "c1" {
					routed = "posted on " + event.SessionID
				}
				got <- routed
			}
		}
	}()
	worker := &session.Session{ID: "c1", Role: "c", PlanID: "p1"}
	fake := newFake(bus, worker)
	fake.answers["2aa first item"] = "tool_errors"
	fake.said["2aa first item"] = "Where does the config live?"
	fake.answers["2ab seeded stuck item"] = "tool_errors"
	fake.answers["2ac seeded question item"] = "tool_errors"
	driver := verified(New(bus, fake, func() []*session.Session { return nil }))
	if _, err := driver.Go(context.Background(), worker, newProofPlan(t), `C:\repo`); err != nil {
		t.Fatal(err)
	}
	select {
	case routed := <-got:
		if routed != "operator" {
			t.Fatalf("routed to %q, want operator", routed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no c.job arrived")
	}
}

// Stop ends the worker, the plan keeps what it finished, and Go can run again.
func TestStopEndsTheWorkerAndGoReEnables(t *testing.T) {
	bus := events.NewBus()
	worker := &session.Session{ID: "c1", Role: "c", PlanID: "p1"}
	fake := newFake(bus, worker)
	fake.blockOn = "2ab seeded stuck item"
	plan := newProofPlan(t)
	driver := verified(New(bus, fake, func() []*session.Session { return nil }))

	finished := make(chan Summary, 1)
	go func() {
		summary, _ := driver.Go(context.Background(), worker, plan, `C:\repo`)
		finished <- summary
	}()
	deadline := time.After(5 * time.Second)
	// Stop while the blocked second item runs, so the first has finished: the
	// test is about keeping what was done, not about where a Stop happens to land.
	for {
		text, _ := plan.Read()
		if driver.Running("p1") && strings.Contains(text, "[~] [[2ab]]") {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("the worker never reached the blocked item:\n%s", text)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !driver.Stop("p1", "c1") {
		t.Fatal("Stop did not reach a running worker")
	}
	select {
	case summary := <-finished:
		if !summary.Stopped && summary.Waiting == 0 {
			t.Fatalf("a stopped worker reported a finished plan: %+v", summary)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the worker did not stop")
	}
	if driver.Running("p1") {
		t.Fatal("Go did not re-enable after Stop")
	}
	// What it finished before the stop is kept.
	text, _ := plan.Read()
	if !strings.Contains(text, "- [x] [[2aa]] 2aa first item") {
		t.Fatalf("the first item's result was lost:\n%s", text)
	}
}

// The worker writes the repo, never plan text. This is the jail's rule, proved
// where the jail decides it.
func TestWorkerIsRefusedPlanText(t *testing.T) {
	root := t.TempDir()
	plans := filepath.Join(root, "plans")
	if err := os.MkdirAll(filepath.Join(plans, "p1"), 0o700); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	worker := &session.Session{ID: "c1", Role: "c", PlanID: "p1", PlanDir: filepath.Join(plans, "p1"), PlansRoot: plans, PlanRepo: repo, Workspace: repo}
	if _, err := worker.WriteRoot(filepath.Join(plans, "p1", "plan.md")); err == nil {
		t.Fatal("the worker was allowed to write plan text")
	}
	if _, err := worker.WriteRoot(filepath.Join(plans, "p1", "plan", "items", "2aa.md")); err == nil {
		t.Fatal("the worker was allowed to write an item file")
	}
	if root, err := worker.WriteRoot(filepath.Join(repo, "main.go")); err != nil || root == "" {
		t.Fatalf("the worker cannot write its own repo: %v", err)
	}
}
