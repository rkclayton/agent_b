package stats

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"harness/internal/events"
)

// Item 2ji (a): where a run's time went.
//
// Every run already journals what this needs — model.response carries
// duration_ms and, when the server reports them, timings.prompt_ms and
// timings.predicted_ms; tool.result carries ms; compaction.summary carries
// duration_ms; approval.required and approval.decided delimit a card the
// operator was looking at. Nothing added them up.
//
// The fold below is deliberately a fold over (type, ts, data) rather than over
// events.Event, for one reason that matters: the same code that enriches a live
// run.stopped on the bus can be run over a RETAINED JOURNAL offline, and is.
// That is how these sums are checked against real runs rather than against a
// fixture written to agree with them.
//
// @consequence-if-false, in force: a bucket a server cannot supply is ABSENT,
// never zero. The pointer fields below are the mechanism, and they carry
// omitempty so the wire shows nothing rather than a false zero.

// RunTime is one run's wall clock, broken into the buckets item 2ji names, in
// milliseconds.
type RunTime struct {
	// TotalMS is the span the operator waited: from the moment the run was
	// queued, when it was, through the moment it stopped. A queued run's wait is
	// part of what he experienced, so the shares are shares of this.
	TotalMS int64 `json:"total_ms"`
	// ModelMS is what we measured around the model call, so it is always
	// available. PromptMS and GenerationMS are what the SERVER reported, so they
	// are absent when it reported nothing — which is the whole point of the
	// narrowing, and is what makes "mostly writing" honest when it is said.
	ModelMS      int64  `json:"model_ms"`
	PromptMS     *int64 `json:"prompt_ms,omitempty"`
	GenerationMS *int64 `json:"generation_ms,omitempty"`
	// ToolMS is the SUM of tool durations. Tools run in parallel, so this can
	// exceed the wall clock, and a reader comparing it to TotalMS must know that.
	ToolMS int64 `json:"tool_ms"`
	// WaitingMS is time waiting on the operator or on a slot: approval cards from
	// required to decided, plus the queue span before the run started.
	WaitingMS    int64 `json:"waiting_ms"`
	CompactionMS int64 `json:"compaction_ms"`
	// Unaccounted is what is left of the wall clock after the buckets above. It
	// is reported rather than hidden, because a bucket nobody named is exactly
	// what a summary like this is tempted to bury.
	UnaccountedMS int64 `json:"unaccounted_ms"`
}

// RunOutcome is the whole of what item 2ji adds to run.stopped: the time, and
// the reliability counters, and no text.
type RunOutcome struct {
	Time          RunTime `json:"time"`
	Retries       int     `json:"retries"`
	Compactions   int     `json:"compactions"`
	EmptyReplies  int     `json:"empty_replies"`
	RepeatedCalls int     `json:"repeated_calls"`
}

// Fields renders the outcome as the run.stopped data keys, with no prose. The
// wire schema names them time, retries, compactions, empty_replies and
// repeated_calls.
func (o RunOutcome) Fields() map[string]any {
	encoded, err := json.Marshal(o)
	if err != nil {
		return nil
	}
	fields := map[string]any{}
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil
	}
	return fields
}

type runAccumulator struct {
	queuedAt, startedAt time.Time
	out                 RunOutcome
	promptMS            float64
	generationMS        float64
	sawTimings          bool
	// A card is open from required to decided. Parallel cards overlap, so the
	// waiting span is the UNION of the open windows, not their sum.
	openCards  map[string]time.Time
	cardWindow []span
	seenCalls  map[string]bool
}

type span struct{ from, to time.Time }

// RunTally folds live events into one outcome per run. It is safe for the bus to
// call from any goroutine.
type RunTally struct {
	mu   sync.Mutex
	runs map[string]*runAccumulator
}

func NewRunTally() *RunTally { return &RunTally{runs: map[string]*runAccumulator{}} }

// observedKinds is every event the fold reads. A caller holding events whose
// data is a struct rather than a journal-shaped map uses this to normalize only
// the handful that matter, instead of paying for every event on the bus.
var observedKinds = map[string]bool{
	"run.queued": true, "run.started": true, "run.resumed": true, "run.stopped": true,
	"model.response": true, "model.retry": true,
	"tool.call": true, "tool.result": true,
	"compaction": true, "compaction.summary": true,
	"approval.required": true, "approval.decided": true,
}

// Observes reports whether Observe reads anything from this event type.
func Observes(kind string) bool { return observedKinds[kind] }

// InstallRunTally puts the fold on the bus, so every run.stopped carries the time
// buckets and the reliability counters BEFORE the journal is written and before
// any subscriber sees it. The chat line, Activity's row and the telemetry
// receiver then read the same numbers from the same place and cannot disagree.
//
// This lives here, beside the fold, rather than in main(), so a test exercises
// the wiring the binary actually runs.
func InstallRunTally(bus *events.Bus) *RunTally {
	tally := NewRunTally()
	bus.SetEnricher(func(event *events.Event) {
		if !Observes(event.Type) {
			return
		}
		// Event.Data is any: most events carry a map, but a few carry a struct --
		// compaction.summary is events.CompactionSummaryData. The fold reads
		// journal-shaped maps, which is exactly what that struct becomes on the
		// wire, so it is normalized the same way rather than missed.
		data, _ := event.Data.(map[string]any)
		if data == nil && event.Data != nil {
			data = eventData(event.Data)
		}
		fields := tally.Observe(event.Type, event.SessionID, event.RunID, event.TS, data)
		if len(fields) == 0 {
			return
		}
		if data == nil {
			data = map[string]any{}
		}
		for name, value := range fields {
			// The scheduler owns reason, detail and turns. The tally only adds.
			if _, taken := data[name]; !taken {
				data[name] = value
			}
		}
		event.Data = data
	})
	return tally
}

// Observe folds one event. It returns nil for every event except run.stopped,
// where it returns the fields to merge into that event's data and forgets the
// run.
func (t *RunTally) Observe(kind, sessionID, runID, ts string, data map[string]any) map[string]any {
	if runID == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	key := sessionID + "\x00" + runID
	entry := t.runs[key]
	if entry == nil {
		entry = &runAccumulator{openCards: map[string]time.Time{}, seenCalls: map[string]bool{}}
		t.runs[key] = entry
	}
	at := parseEventTime(ts)
	switch kind {
	case "run.queued":
		if entry.queuedAt.IsZero() {
			entry.queuedAt = at
		}
	case "run.started", "run.resumed":
		if entry.startedAt.IsZero() {
			entry.startedAt = at
		}
	case "model.response":
		entry.out.Time.ModelMS += int64Value(data["duration_ms"])
		// timings can arrive as a named map type (llm.Timings) rather than as
		// map[string]any, and an assertion to the unnamed type does not match a
		// named one. That silently cost every live timing until it was read
		// structurally instead.
		if timings := mapField(data["timings"]); timings != nil {
			prompt, hasPrompt := floatValue(timings["prompt_ms"])
			predicted, hasPredicted := floatValue(timings["predicted_ms"])
			if hasPrompt || hasPredicted {
				entry.sawTimings = true
				entry.promptMS += prompt
				entry.generationMS += predicted
			}
		}
		if isEmptyReply(data) {
			entry.out.EmptyReplies++
		}
	case "model.retry":
		entry.out.Retries++
	case "tool.call":
		signature := toolSignature(data)
		if signature != "" {
			if entry.seenCalls[signature] {
				entry.out.RepeatedCalls++
			}
			entry.seenCalls[signature] = true
		}
	case "tool.result":
		entry.out.Time.ToolMS += int64Value(data["ms"])
	case "compaction":
		entry.out.Compactions++
	case "compaction.summary":
		entry.out.Time.CompactionMS += int64Value(data["duration_ms"])
	case "approval.required":
		if id := stringField(data["call_id"]); id != "" && !at.IsZero() {
			entry.openCards[id] = at
		}
	case "approval.decided":
		id := stringField(data["call_id"])
		opened, ok := entry.openCards[id]
		if ok && !at.IsZero() {
			entry.cardWindow = append(entry.cardWindow, span{opened, at})
		}
		delete(entry.openCards, id)
	case "run.stopped":
		delete(t.runs, key)
		return entry.finish(at).Fields()
	}
	return nil
}

// Forget drops a run the tally will never see stopped, so a long-lived process
// does not accumulate one accumulator per abandoned run.
func (t *RunTally) Forget(sessionID, runID string) {
	t.mu.Lock()
	delete(t.runs, sessionID+"\x00"+runID)
	t.mu.Unlock()
}

func (a *runAccumulator) finish(stoppedAt time.Time) RunOutcome {
	out := a.out
	began := a.queuedAt
	if began.IsZero() {
		began = a.startedAt
	}
	if !began.IsZero() && !stoppedAt.IsZero() && stoppedAt.After(began) {
		out.Time.TotalMS = stoppedAt.Sub(began).Milliseconds()
	}
	// The queue span is waiting too: it is time the operator spent with nothing
	// happening on his behalf.
	if !a.queuedAt.IsZero() && !a.startedAt.IsZero() && a.startedAt.After(a.queuedAt) {
		out.Time.WaitingMS += a.startedAt.Sub(a.queuedAt).Milliseconds()
	}
	// A card still open when the run stopped was open until it stopped.
	windows := append([]span(nil), a.cardWindow...)
	for _, opened := range a.openCards {
		if !stoppedAt.IsZero() && stoppedAt.After(opened) {
			windows = append(windows, span{opened, stoppedAt})
		}
	}
	out.Time.WaitingMS += unionMS(windows)
	if a.sawTimings {
		prompt := int64(a.promptMS + 0.5)
		generation := int64(a.generationMS + 0.5)
		out.Time.PromptMS, out.Time.GenerationMS = &prompt, &generation
	}
	// Tool time can overlap itself, so the accounted total is clamped at the wall
	// clock rather than allowed to produce a negative remainder.
	accounted := out.Time.ModelMS + out.Time.ToolMS + out.Time.WaitingMS + out.Time.CompactionMS
	if remainder := out.Time.TotalMS - accounted; remainder > 0 {
		out.Time.UnaccountedMS = remainder
	}
	return out
}

// unionMS measures the union of a set of windows, so two cards open at once are
// one wait rather than two.
func unionMS(windows []span) int64 {
	if len(windows) == 0 {
		return 0
	}
	sort.Slice(windows, func(i, j int) bool { return windows[i].from.Before(windows[j].from) })
	total := time.Duration(0)
	current := windows[0]
	for _, window := range windows[1:] {
		if window.from.After(current.to) {
			total += current.to.Sub(current.from)
			current = window
			continue
		}
		if window.to.After(current.to) {
			current.to = window.to
		}
	}
	total += current.to.Sub(current.from)
	if total < 0 {
		return 0
	}
	return total.Milliseconds()
}

// isEmptyReply is the same shape the run loop stops on: nothing said and nothing
// called. A reply that is empty because the model called a tool is not empty.
//
// The length is measured structurally, and that is not fussiness. Live, this
// field is []events.ToolCall; in a journal it is []any. An assertion to []any
// matches only the journal, so the first version of this counted EVERY
// tool-calling turn as an empty reply while the journal tests passed. The
// screenshot gate is what showed it: "One empty reply." under a run that had
// done nothing of the kind.
func isEmptyReply(data map[string]any) bool {
	if strings.TrimSpace(stringField(data["content"])) != "" {
		return false
	}
	return sliceLength(data["tool_calls"]) == 0
}

// sliceLength is len() for a value of any slice or array type, and 0 for
// anything else.
func sliceLength(value any) int {
	if value == nil {
		return 0
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Slice, reflect.Array:
		return reflected.Len()
	}
	return 0
}

// toolSignature is the name and the arguments, canonically ordered, so "the same
// call again" means the same call and not merely the same tool.
func toolSignature(data map[string]any) string {
	name := stringField(data["name"])
	if name == "" {
		return ""
	}
	args, err := json.Marshal(canonical(data["args"]))
	if err != nil {
		return ""
	}
	return name + "\x00" + string(args)
}

// canonical rewrites maps so Marshal emits their keys in a fixed order. Go
// already sorts map keys, so this exists for the nested case and for slices.
func canonical(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, v := range typed {
			out[k] = canonical(v)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, v := range typed {
			out = append(out, canonical(v))
		}
		return out
	default:
		return value
	}
}

// mapField reads a value that is some kind of string-keyed map, whatever its
// declared type.
func mapField(value any) map[string]any {
	if value == nil {
		return nil
	}
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() != reflect.Map || reflected.Type().Key().Kind() != reflect.String {
		return nil
	}
	out := make(map[string]any, reflected.Len())
	for _, key := range reflected.MapKeys() {
		out[key.String()] = reflected.MapIndex(key).Interface()
	}
	return out
}

func stringField(value any) string {
	text, _ := value.(string)
	return text
}

func floatValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	}
	return 0, false
}

func parseEventTime(ts string) time.Time {
	if ts == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, ts); err == nil {
			return parsed
		}
	}
	return time.Time{}
}
