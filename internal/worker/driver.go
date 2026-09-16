package worker

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"harness/internal/events"
	"harness/internal/session"
)

// Submitter is the scheduler seam: the worker drives ordinary runs, so it goes
// through the same queue, approvals and backstops any chat does.
type Submitter interface {
	Submit(ctx context.Context, sessionID, text string) (string, error)
	Stop(sessionID string, all bool) int
}

// Driver walks a plan's waiting items, one at a time, in plan order.
type Driver struct {
	bus       *events.Bus
	submit    Submitter
	sessions  func() []*session.Session
	mu        sync.Mutex
	running   map[string]context.CancelFunc
	lastError map[string]string
	last      map[string]Summary
}

func New(bus *events.Bus, submit Submitter, sessions func() []*session.Session) *Driver {
	return &Driver{bus: bus, submit: submit, sessions: sessions, running: map[string]context.CancelFunc{}, lastError: map[string]string{}, last: map[string]Summary{}}
}

// Running reports whether a worker is on this plan right now. Go is disabled
// while one runs, and this is what answers that.
func (d *Driver) Running(planID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.running[planID]
	return ok
}

// Stop ends the worker on a plan the way Stop ends any run.
func (d *Driver) Stop(planID, sessionID string) bool {
	d.mu.Lock()
	cancel, ok := d.running[planID]
	d.mu.Unlock()
	if !ok {
		return false
	}
	cancel()
	if d.submit != nil && sessionID != "" {
		d.submit.Stop(sessionID, false)
	}
	return true
}

// Summary is what the done card shows: terse, and honest about what is unfinished.
type Summary struct {
	PlanID  string   `json:"plan_id"`
	Done    int      `json:"done"`
	Stuck   int      `json:"stuck"`
	Waiting int      `json:"waiting"`
	Reasons []string `json:"reasons,omitempty"`
	Stopped bool     `json:"stopped,omitempty"`
}

// Go runs the plan. It returns when no waiting item remains, when every
// remaining item is stuck, or when it is stopped.
func (d *Driver) Go(parent context.Context, s *session.Session, plan *Plan, repo string) (Summary, error) {
	planID := s.Snapshot().PlanID
	d.mu.Lock()
	if _, busy := d.running[planID]; busy {
		d.mu.Unlock()
		return Summary{}, fmt.Errorf("a worker is already running on this plan")
	}
	ctx, cancel := context.WithCancel(parent)
	d.running[planID] = cancel
	d.mu.Unlock()
	defer func() {
		cancel()
		d.mu.Lock()
		delete(d.running, planID)
		d.mu.Unlock()
	}()

	summary := Summary{PlanID: planID}
	for {
		if ctx.Err() != nil {
			summary.Stopped = true
			break
		}
		text, err := plan.Read()
		if err != nil {
			return d.recorded(planID, summary, err)
		}
		items := Parse(text)
		item, ok := Next(items)
		if !ok {
			break
		}
		if err := plan.Mark(item, "~", ""); err != nil {
			return d.recorded(planID, summary, err)
		}
		s.SetWorkerJob(Brief(item, plan.Dir, repo))
		outcome := d.runItem(ctx, s, item)
		if outcome.Marker == "x" {
			summary.Done++
		} else {
			summary.Stuck++
			if outcome.Reason != "" {
				summary.Reasons = append(summary.Reasons, item.Text+": "+outcome.Reason)
			}
		}
		// Re-read: the run may have changed plan.md's length under us.
		text, err = plan.Read()
		if err != nil {
			return d.recorded(planID, summary, err)
		}
		current, found := findByText(Parse(text), item.Text)
		if !found {
			return summary, fmt.Errorf("item %q left plan.md while the worker was on it", item.Text)
		}
		if err := plan.Mark(current, outcome.Marker, outcome.Reason); err != nil {
			return d.recorded(planID, summary, err)
		}
		// A worker that could not finish has one permitted piece of speech: the
		// question it could not answer from the plan or the repo. It is posted in
		// the design thread and routed — to d when a bound d-session can answer
		// from the plan, to the operator otherwise.
		if outcome.Marker != "x" {
			if question := lastSaid(s); question != "" {
				target, routedTo := RouteTarget(d.sessionList(), planID)
				Ask(d.bus, s, target, "", question, routedTo)
			}
		}
		Publish(d.bus, s, "", outcome)
		if ctx.Err() != nil {
			summary.Stopped = true
			break
		}
	}
	text, err := plan.Read()
	if err == nil {
		summary.Waiting = 0
		for _, item := range Parse(text) {
			if item.Marker == " " {
				summary.Waiting++
			}
		}
	}
	d.record(planID, summary, nil)
	if !summary.Stopped && summary.Waiting == 0 {
		PublishPlanDone(d.bus, s, "", summary.Done, summary.Stuck)
	}
	return summary, nil
}

// Result is the last worker's outcome on a plan, and whether one has finished
// at all. The done card reads this.
func (d *Driver) Result(planID string) (Summary, string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	summary, ok := d.last[planID]
	return summary, d.lastError[planID], ok
}

// recorded is the error path's record-and-return, so no exit from Go leaves the
// done card reading a stale outcome.
func (d *Driver) recorded(planID string, summary Summary, err error) (Summary, error) {
	d.record(planID, summary, err)
	return summary, err
}

func (d *Driver) record(planID string, summary Summary, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.last[planID] = summary
	delete(d.lastError, planID)
	if err != nil {
		d.lastError[planID] = err.Error()
	}
}

func findByText(items []Item, text string) (Item, bool) {
	for _, item := range items {
		if item.Text == text || WithReason(item.Text, "") == text {
			return item, true
		}
	}
	return Item{}, false
}

// runItem submits one item and waits for its run to stop, then reads the stop
// reason. A run that stops for any reason other than a clean finish is stuck:
// the worker never decides for itself that acceptance passed.
func (d *Driver) runItem(ctx context.Context, s *session.Session, item Item) Outcome {
	job := s.WorkerJob()
	stream, release := d.bus.Subscribe()
	defer release()

	runID, err := d.submit.Submit(ctx, s.ID, item.Text)
	if err != nil {
		return Outcome{ItemID: job.ItemID, Marker: "!", Reason: "could not start: " + err.Error()}
	}
	deadline := time.NewTimer(6 * time.Hour)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return Outcome{ItemID: job.ItemID, Marker: "!", Reason: "stopped"}
		case <-deadline.C:
			return Outcome{ItemID: job.ItemID, Marker: "!", Reason: "worker wall clock"}
		case event, open := <-stream:
			if !open {
				return Outcome{ItemID: job.ItemID, Marker: "!", Reason: "event stream closed"}
			}
			if event.Type != events.RunStopped || event.SessionID != s.ID {
				continue
			}
			if runID != "" && event.RunID != runID {
				continue
			}
			return classify(job.ItemID, event)
		}
	}
}

// classify turns a stop reason into a marker. "done" is the only marker that can
// produce [x], and even then the run has to have said something: a run that
// ends without a final answer has not shown its work.
func classify(itemID string, event events.Event) Outcome {
	data, _ := event.Data.(map[string]any)
	reason, _ := data["reason"].(string)
	detail, _ := data["detail"].(string)
	switch reason {
	case "done":
		return Outcome{ItemID: itemID, Marker: "x"}
	case "context_exhausted", "tool_errors", "cycle", "turn_ceiling", "wall_clock", "tool_budget":
		return Outcome{ItemID: itemID, Marker: "!", Reason: reason}
	case "":
		return Outcome{ItemID: itemID, Marker: "!", Reason: "run stopped without a reason"}
	default:
		text := reason
		if detail != "" {
			text = reason + ": " + strings.TrimSpace(detail)
		}
		if len(text) > 160 {
			text = text[:157] + "..."
		}
		return Outcome{ItemID: itemID, Marker: "!", Reason: text}
	}
}

// lastSaid is the worker's final assistant message: what it said when it could
// not finish. Empty when it said nothing, in which case there is no question to
// post and the stuck reason stands alone.
func lastSaid(s *session.Session) string {
	messages := s.MessagesCopy()
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role != "assistant" || messages[index].Elided {
			continue
		}
		text := strings.TrimSpace(messages[index].Content)
		if text == "" {
			continue
		}
		if len(text) > 400 {
			text = text[:397] + "..."
		}
		return text
	}
	return ""
}

func (d *Driver) sessionList() []*session.Session {
	if d.sessions == nil {
		return nil
	}
	return d.sessions()
}
