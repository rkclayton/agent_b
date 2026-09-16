// Package worker runs accepted plan items, one at a time, in plan order.
//
// The worker is a c-role session with no chat. It marks the item it is on,
// works under the same approvals and backstops as any run, and writes the
// repository — never the plan's text. Markers are the harness's writes, which is
// why they live here and not in the model's tool surface.
package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"harness/internal/events"
	"harness/internal/session"
)

// Item is one line of plan.md carrying a marker.
type Item struct {
	Line   string // the whole line, verbatim, so a rewrite can be exact
	Marker string // " ", "~", "x" or "!"
	Text   string // what follows the marker
	Index  int    // line number, zero-based
}

var markerLine = regexp.MustCompile(`^(\s*[-*]\s*)\[([ ~x!])\]\s?(.*)$`)

// Parse reads plan.md into its marker lines, in plan order. Lines without a
// marker are not items and are left alone.
func Parse(text string) []Item {
	items := []Item{}
	for index, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		match := markerLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		items = append(items, Item{Line: line, Marker: match[2], Text: strings.TrimSpace(match[3]), Index: index})
	}
	return items
}

// Next is the first item still waiting, in plan order.
func Next(items []Item) (Item, bool) {
	for _, item := range items {
		if item.Marker == " " {
			return item, true
		}
	}
	return Item{}, false
}

// Remaining reports whether any item is still waiting.
func Remaining(items []Item) bool {
	_, ok := Next(items)
	return ok
}

// SetMarker rewrites one item's marker in place. It replaces only the first
// bracket on that exact line, so nothing else in plan.md can move.
func SetMarker(text string, item Item, marker string) (string, error) {
	if !strings.Contains("~x! ", marker) || len(marker) != 1 {
		return "", fmt.Errorf("marker %q is not one of [ ] [~] [x] [!]", marker)
	}
	newline := "\n"
	if strings.Contains(text, "\r\n") {
		newline = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if item.Index < 0 || item.Index >= len(lines) {
		return "", fmt.Errorf("item line %d is outside plan.md", item.Index)
	}
	if lines[item.Index] != item.Line {
		return "", fmt.Errorf("plan.md line %d changed under the worker", item.Index)
	}
	match := markerLine.FindStringSubmatch(item.Line)
	if match == nil {
		return "", fmt.Errorf("line %d carries no marker", item.Index)
	}
	rest := match[3]
	// A stuck reason is written after the text, once: re-running must not stack.
	lines[item.Index] = match[1] + "[" + marker + "] " + rest
	return strings.Join(lines, newline), nil
}

// WithReason appends a stuck reason to an item's text, replacing any previous
// one so a second failure does not stack a second parenthetical.
func WithReason(line, reason string) string {
	reason = oneLine(reason)
	trimmed := strings.TrimRight(line, " \t")
	if index := strings.LastIndex(trimmed, "  — stuck: "); index >= 0 {
		trimmed = trimmed[:index]
	}
	if reason == "" {
		return trimmed
	}
	return trimmed + "  — stuck: " + reason
}

// Plan is the file the worker marks. Reads and writes go through here so the
// path is resolved once and every write is a whole-file replace of known text.
type Plan struct {
	Dir string
	mu  sync.Mutex
}

func (p *Plan) Path() string { return filepath.Join(p.Dir, "plan.md") }

func (p *Plan) Read() (string, error) {
	data, err := os.ReadFile(p.Path())
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Mark flips one item's marker. A stuck marker records its reason once; any
// other marker clears an earlier reason. The line must still read exactly as it
// did when the item was found, or nothing is written.
func (p *Plan) Mark(item Item, marker, reason string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	text, err := p.Read()
	if err != nil {
		return err
	}
	next, err := markLine(text, item, marker, reason)
	if err != nil {
		return err
	}
	return os.WriteFile(p.Path(), []byte(next), 0o600)
}

func markLine(text string, item Item, marker, reason string) (string, error) {
	if !strings.Contains("~x! ", marker) || len(marker) != 1 {
		return "", fmt.Errorf("marker %q is not one of [ ] [~] [x] [!]", marker)
	}
	newline := "\n"
	if strings.Contains(text, "\r\n") {
		newline = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if item.Index < 0 || item.Index >= len(lines) {
		return "", fmt.Errorf("item line %d is outside plan.md", item.Index)
	}
	if lines[item.Index] != item.Line {
		return "", fmt.Errorf("plan.md line %d changed under the worker", item.Index)
	}
	match := markerLine.FindStringSubmatch(item.Line)
	if match == nil {
		return "", fmt.Errorf("line %d carries no marker", item.Index)
	}
	if marker != "!" {
		reason = ""
	}
	lines[item.Index] = match[1] + "[" + marker + "] " + WithReason(match[3], reason)
	return strings.Join(lines, newline), nil
}

// Outcome is why a worker stopped on one item.
type Outcome struct {
	ItemID string
	Marker string
	Reason string
}

// Publish emits the plan events 2dz already declares and dispatches.
func Publish(bus *events.Bus, s *session.Session, runID string, outcome Outcome) {
	if bus == nil {
		return
	}
	switch outcome.Marker {
	case "x":
		bus.Publish(events.New(events.ItemDone, s.ID, runID, map[string]any{"item": outcome.ItemID, "plan_id": s.PlanID}))
	case "!":
		bus.Publish(events.New(events.ItemStuck, s.ID, runID, map[string]any{"item": outcome.ItemID, "plan_id": s.PlanID, "reason": outcome.Reason}))
	}
}

// PublishPlanDone says the plan has no waiting items left.
func PublishPlanDone(bus *events.Bus, s *session.Session, runID string, done, stuck int) {
	if bus == nil {
		return
	}
	bus.Publish(events.New(events.PlanDone, s.ID, runID, map[string]any{"plan_id": s.PlanID, "done": done, "stuck": stuck}))
}

// Ask posts the worker's one permitted kind of speech: a question it cannot
// answer from the plan or the repo, routed to d when a bound d-session can
// answer from the plan and to the operator otherwise.
// Ask posts the worker's one piece of speech where it was routed: in the bound
// planner's thread when one can answer, and on the worker's own session — where
// the notifications manager still carries it to the operator — when none can.
// role:"c" is what makes the thread show it as the worker speaking.
func Ask(bus *events.Bus, s *session.Session, target *session.Session, runID, question, routedTo string) {
	if bus == nil {
		return
	}
	sessionID := s.ID
	if target != nil {
		sessionID = target.ID
	}
	bus.Publish(events.New(events.WorkerJob, sessionID, runID, map[string]any{
		"plan_id": s.PlanID, "item": s.WorkerJob().ItemID, "question": question,
		"routed_to": routedTo, "role": "c", "worker": s.ID,
	}))
}

// oneLine flattens anything that would break the item line it is appended to.
// Tool errors and model text arrive with newlines, tabs and runs of spaces, and
// a newline here would split one item into an item and a line of prose.
func oneLine(value string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(value), " "))
}

// Route picks where a worker's question goes. A bound d-session that is not
// itself running can answer from the plan; otherwise it is the operator's.
func Route(sessions []*session.Session, planID string) string {
	_, routed := RouteTarget(sessions, planID)
	return routed
}

// RouteTarget is Route plus the session that answers, so the post can be made
// in that thread rather than only announced.
func RouteTarget(sessions []*session.Session, planID string) (*session.Session, string) {
	for _, item := range sessions {
		if item == nil {
			continue
		}
		snapshot := item.Snapshot()
		if snapshot.Role == "d" && snapshot.PlanID == planID && !snapshot.Closed {
			return item, "d"
		}
	}
	return nil, "operator"
}

// Brief turns an item's plan line into the fields the worker prompt renders.
// The plan line is the intent; the item file, when one exists, carries the rest.
func Brief(item Item, planDir, repo string) session.WorkerJob {
	job := session.WorkerJob{ItemID: itemID(item.Text), Intent: item.Text, Repo: repo}
	if job.ItemID == "" {
		return job
	}
	data, err := os.ReadFile(filepath.Join(planDir, "plan", "items", job.ItemID+".md"))
	if err != nil {
		return job
	}
	body := string(data)
	job.Verify = field(body, "verify")
	job.Acceptance = section(body, "## Acceptance")
	job.Approach = section(body, "## Contract")
	job.Negative = section(body, "## Not in scope")
	return job
}

var idPattern = regexp.MustCompile(`^([0-9]+[a-z]*)\b`)

func itemID(text string) string {
	if match := idPattern.FindStringSubmatch(strings.TrimSpace(text)); match != nil {
		return match[1]
	}
	return ""
}

// field reads one "name: value" line from an item file's header, which ends at
// its first heading. Only the header counts, so prose that happens to start a
// line with the same word cannot become the item's verifier.
func field(body, name string) string {
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "#") {
			break
		}
		if value, ok := strings.CutPrefix(line, name+":"); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// NoVerifier is the stuck reason for an item that names no verifier command.
const NoVerifier = "no verifier named"

// Retryable is an item stuck only for want of a verifier that has one now.
func Retryable(item Item, planDir string) bool {
	if item.Marker != "!" || !strings.HasSuffix(item.Text, "  — stuck: "+NoVerifier) {
		return false
	}
	bare := item
	bare.Text = WithReason(item.Text, "")
	return Brief(bare, planDir, "").Verify != ""
}

// NextIn is the next item the worker takes: the first waiting one, or one that
// was stuck only for want of a verifier and now names one.
func NextIn(items []Item, planDir string) (Item, bool) {
	for _, item := range items {
		if item.Marker == " " || Retryable(item, planDir) {
			return item, true
		}
	}
	return Item{}, false
}

// RemainingIn reports whether the worker has anything to take.
func RemainingIn(items []Item, planDir string) bool {
	_, ok := NextIn(items, planDir)
	return ok
}

// AskVerifier is the worker telling the planner an item names no verifier. It is
// the worker's speech, not a turn: a c.job notice carrying a proposal the tray
// shows, which only the planner can complete because only it can name the command.
func AskVerifier(bus *events.Bus, s *session.Session, target *session.Session, routedTo string, item Item, planDir string) {
	if bus == nil {
		return
	}
	job := s.WorkerJob()
	path := "plan.md"
	if job.ItemID != "" {
		if _, err := os.Stat(filepath.Join(planDir, "plan", "items", job.ItemID+".md")); err == nil {
			path = "plan/items/" + job.ItemID + ".md"
		}
	}
	itemID := job.ItemID
	if itemID == "" {
		itemID = "item"
	}
	sessionID := s.ID
	if target != nil {
		sessionID = target.ID
	}
	bus.Publish(events.New(events.WorkerJob, sessionID, "", map[string]any{
		"plan_id": s.PlanID, "item": job.ItemID, "question": "This item names no verifier command; name one with verify: so the worker can check it.",
		"routed_to": routedTo, "role": "c", "worker": s.ID,
		"proposal": map[string]any{"id": "verify-" + itemID, "kind": "verifier", "path": path, "old_text": WithReason(item.Text, ""), "new_text": "", "item_id": itemID},
	}))
}

func section(body, heading string) string {
	index := strings.Index(body, heading)
	if index < 0 {
		return ""
	}
	rest := body[index+len(heading):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

var _ = context.Background
