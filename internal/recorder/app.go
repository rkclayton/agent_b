package recorder

import (
	"context"
	"math/bits"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"harness/internal/events"
)

// Item 2q7: how the app itself is doing — start-up, the page, the phone link,
// installs, resources and what gets used — gathered for an hour and sent as a
// handful of aggregates. Every size and count that could single out a machine
// is a power-of-two bucket. Nothing here is text: words from fixed sets, our own
// file names, numbers.

// AppPeriod is how often the app's aggregates leave.
const AppPeriod = time.Hour

const maxInstalls, maxErrorKinds = 4, 16

// Bucket is the smallest power of two at or above n; zero stays zero.
func Bucket(n int64) int64 {
	if n <= 0 {
		return 0
	}
	return 1 << bits.Len64(uint64(n-1))
}

type App struct {
	mu   sync.Mutex
	sink Sink
	now  func() time.Time
	// Resources is the server's probe for folder sizes, chat count, RAM and
	// OS build; it runs once an hour, off any request.
	Resources func() map[string]any

	started    time.Time
	start      map[string]any
	startSent  bool
	stateBytes int64
	stateMS    []int
	pageLoad   []int
	freeze     int
	jsErrors   map[jsError]int
	connects   int
	drops      map[string]int
	droppedAt  time.Time
	reconnect  []int
	refused    map[string]int
	pushes     map[string]int
	joinBytes  int64
	counts     map[string]int
	tools      map[string]int
	attachment map[string]int
	settings   map[string]int
	approvals  map[string]int
	asked      map[string]string
	installs   []map[string]any
	installed  string
	memoryPeak uint64
}

type jsError struct {
	name, file string
	line       int
}

func NewApp(started time.Time) *App {
	app := &App{now: time.Now, started: started, start: map[string]any{}}
	app.reset()
	return app
}

func (a *App) reset() {
	a.stateBytes, a.stateMS, a.pageLoad, a.freeze = 0, nil, nil, 0
	a.jsErrors, a.drops, a.refused, a.pushes = map[jsError]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	a.connects, a.reconnect, a.joinBytes = 0, nil, 0
	a.counts, a.tools, a.attachment, a.settings, a.approvals = map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	a.installs = nil
	if a.asked == nil {
		a.asked = map[string]string{}
	}
}

func (a *App) SetSink(sink Sink) { a.mu.Lock(); a.sink = sink; a.mu.Unlock() }

// NoteListening is (a): the port answers listenMS after the process began, and
// the last instance ended cleanly or did not.
func (a *App) NoteListening(listenMS int64, previousExit string) {
	a.mu.Lock()
	a.start["listen_ms"], a.start["previous_exit"] = listenMS, word(previousExit)
	a.mu.Unlock()
}

// NoteWindowShown is (a): the host window appeared.
func (a *App) NoteWindowShown() {
	a.mu.Lock()
	if _, seen := a.start["window_ms"]; !seen {
		a.start["window_ms"] = a.now().Sub(a.started).Milliseconds()
	}
	a.mu.Unlock()
}

// NoteState is (b): one state answer's size and time.
func (a *App) NoteState(bytes int64, elapsed time.Duration) {
	a.mu.Lock()
	a.stateBytes = max(a.stateBytes, bytes)
	a.stateMS = append(a.stateMS, int(elapsed.Milliseconds()))
	a.mu.Unlock()
}

// NotePage is (b) and (f) from the page: its longest freeze and the settings
// pages it opened since it last said.
func (a *App) NotePage(freezeMS int, pages map[string]int) {
	a.mu.Lock()
	a.freeze = max(a.freeze, freezeMS)
	for page, count := range pages {
		if pagePattern.MatchString(page) && count > 0 && len(a.settings) < 32 {
			a.settings[page] += count
		}
	}
	a.mu.Unlock()
}

// NoteReport is (f): Report this chat was used.
func (a *App) NoteReport() { a.mu.Lock(); a.counts["reports"]++; a.mu.Unlock() }

// NoteJoin is (c): what one phone join sent.
func (a *App) NoteJoin(bytes int64) {
	a.mu.Lock()
	a.joinBytes = max(a.joinBytes, bytes)
	a.mu.Unlock()
}

var (
	pagePattern  = regexp.MustCompile(`^[a-z]{1,24}$`)
	errorName    = regexp.MustCompile(`^[A-Za-z]{1,40}$`)
	ourFile      = regexp.MustCompile(`^[a-z0-9-]{1,40}\.m?js$`)
	refusalCode  = regexp.MustCompile(`^ERROR code=([a-z_]{1,40})`)
	pushAnswer   = regexp.MustCompile(`^PUSH kind=[a-z_]+ answer=([a-z_]{1,24})`)
	dropReasonIs = []struct{ class, needle string }{{"eof", "EOF"}, {"timeout", "timeout"}, {"timeout", "deadline"}, {"reset", "reset"}, {"refused", "refused"}, {"closed", "closed"}}
)

// Link is (c): one line of the broker session's own record. Only the shape of
// the line is read — a reason's words become a class, a refusal its code.
func (a *App) Link(message string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	switch {
	case message == "connected":
		a.connects++
		if !a.droppedAt.IsZero() {
			a.reconnect = append(a.reconnect, int(now.Sub(a.droppedAt).Milliseconds()))
			a.droppedAt = time.Time{}
		}
	case strings.HasPrefix(message, "disconnected reason="):
		class := "other"
		for _, rule := range dropReasonIs {
			if strings.Contains(message, rule.needle) {
				class = rule.class
				break
			}
		}
		a.drops[class]++
		a.droppedAt = now
	default:
		if match := refusalCode.FindStringSubmatch(message); match != nil && len(a.refused) < maxErrorKinds {
			a.refused[match[1]]++
		} else if match := pushAnswer.FindStringSubmatch(message); match != nil && len(a.pushes) < maxErrorKinds {
			a.pushes[match[1]]++
		}
	}
}

// Observe is the bus's part: (a) the first answer, (b) page loads and our own
// JavaScript errors, (d) installs, (f) what gets used.
func (a *App) Observe(event events.Event) {
	switch event.Type {
	case events.ModelResponse, events.NavigationMeasured, events.UIError, events.UpdateChanged, events.SessionCreated,
		events.MessageAppended, events.ToolResult, events.Speech, events.ApprovalRequired, events.ApprovalDecided:
	default:
		return
	}
	data := asMap(event.Data)
	a.mu.Lock()
	defer a.mu.Unlock()
	switch event.Type {
	case events.ModelResponse:
		if _, seen := a.start["first_answer_ms"]; !seen {
			a.start["first_answer_ms"] = a.now().Sub(a.started).Milliseconds()
		}
	case events.NavigationMeasured:
		if ms, ok := data["end_to_end_ms"]; ok {
			a.pageLoad = append(a.pageLoad, number(ms))
		}
	case events.UIError:
		key := jsError{name: text(data["name"]), file: text(data["file"]), line: number(data["line"])}
		if !errorName.MatchString(key.name) {
			key.name = "unnamed"
		}
		if !ourFile.MatchString(key.file) {
			key.file, key.line = "", 0
		}
		if _, seen := a.jsErrors[key]; seen || len(a.jsErrors) < maxErrorKinds {
			a.jsErrors[key] += max(1, number(data["repeat_count"]))
		}
	case events.UpdateChanged:
		outcome := asMap(data["outcome"])
		if at := text(outcome["at"]); at != "" && at != a.installed && len(a.installs) < maxInstalls {
			a.installed = at
			class := "ok"
			if outcome["ok"] != true {
				class = "failed"
			}
			a.installs = append(a.installs, map[string]any{"step": word(outcome["phase"]), "class": class, "from": word(data["current_version"]), "to": word(outcome["version"])})
		}
	case events.SessionCreated:
		a.counts["chats_created"]++
	case events.MessageAppended:
		message := asMap(data["message"])
		if text(message["role"]) != "user" {
			return
		}
		a.counts["messages_sent"]++
		attachments, _ := message["attachments"].([]any)
		for _, item := range attachments {
			if kind := word(asMap(item)["kind"]); kind != "" && len(a.attachment) < maxErrorKinds {
				a.attachment[kind]++
			}
		}
	case events.ToolResult:
		if name := toolName(data["name"]); name != "<invalid>" {
			a.tools[name]++
		}
	case events.Speech:
		if text(data["stage"]) == "route" {
			a.counts["voice"]++
		}
	case events.ApprovalRequired:
		kind := word(data["kind"])
		if kind == "" {
			kind = word(data["name"])
		}
		if len(a.asked) < 256 {
			a.asked[text(data["call_id"])] = kind
		}
	case events.ApprovalDecided:
		id := text(data["call_id"])
		kind, ok := a.asked[id]
		if !ok {
			return
		}
		delete(a.asked, id)
		answer := word(data["decision"])
		if data["unanswered"] == true {
			answer = "unanswered"
		}
		if len(a.approvals) < maxErrorKinds {
			a.approvals[kind+"."+answer]++
		}
	}
}

// Run samples memory each minute and sends the aggregates each period, until
// ctx ends.
func (a *App) Run(ctx context.Context) {
	sample, flush := time.NewTicker(time.Minute), time.NewTicker(AppPeriod)
	defer sample.Stop()
	defer flush.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sample.C:
			a.sampleMemory()
		case <-flush.C:
			a.send(a.Flush())
		}
	}
}

func (a *App) sampleMemory() {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	a.mu.Lock()
	a.memoryPeak = max(a.memoryPeak, stats.Sys)
	a.mu.Unlock()
}

func (a *App) send(emit []emitted) {
	a.mu.Lock()
	sink := a.sink
	a.mu.Unlock()
	if sink == nil {
		return
	}
	for _, item := range emit {
		sink(item.kind, item.data)
	}
}

// Flush builds the period's events and starts the next period.
func (a *App) Flush() []emitted {
	a.sampleMemory()
	resources := map[string]any{}
	if a.Resources != nil {
		resources = a.Resources()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []emitted
	if !a.startSent {
		a.startSent = true
		start := map[string]any{}
		for key, value := range a.start {
			start[key] = value
		}
		out = append(out, emitted{events.AppStart, start})
	}
	page := map[string]any{"state_bytes": Bucket(a.stateBytes), "longest_freeze_ms": a.freeze, "js_errors": a.errorList()}
	if len(a.stateMS) > 0 {
		page["state_ms"] = spread(a.stateMS, false)
	}
	if len(a.pageLoad) > 0 {
		page["page_load_ms"] = spread(a.pageLoad, false)
	}
	out = append(out, emitted{events.PageHealth, page})
	if a.connects+len(a.drops)+len(a.refused)+len(a.pushes) > 0 || a.joinBytes > 0 {
		link := map[string]any{"connects": a.connects, "drops": a.drops, "refused": a.refused, "pushes": a.pushes, "join_bytes": Bucket(a.joinBytes)}
		if len(a.reconnect) > 0 {
			link["reconnect_ms"] = spread(a.reconnect, false)
		}
		out = append(out, emitted{events.LinkHealth, link})
	}
	for _, install := range a.installs {
		out = append(out, emitted{events.Install, install})
	}
	resource := map[string]any{"memory_peak_bytes": Bucket(int64(a.memoryPeak)), "arch": runtime.GOARCH}
	for _, key := range []string{"data_bytes", "chats_bytes", "chats", "ram_bytes"} {
		if value, ok := resources[key].(int64); ok {
			resource[key] = Bucket(value)
		}
	}
	if build, ok := resources["os_version"].(string); ok {
		resource["os_version"] = build
	}
	out = append(out, emitted{events.Resource, resource})
	use := map[string]any{"tools": a.tools, "attachments": a.attachment, "settings_pages": a.settings, "approvals": a.approvals}
	for _, key := range []string{"chats_created", "messages_sent", "reports", "voice"} {
		use[key] = a.counts[key]
	}
	out = append(out, emitted{events.FeatureUse, use})
	a.memoryPeak = 0
	a.reset()
	return out
}

func (a *App) errorList() []map[string]any {
	keys := make([]jsError, 0, len(a.jsErrors))
	for key := range a.jsErrors {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return a.jsErrors[keys[i]] > a.jsErrors[keys[j]] })
	out := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		out = append(out, map[string]any{"name": key.name, "file": key.file, "line": key.line, "count": a.jsErrors[key]})
	}
	return out
}

// SendNow sends the period's aggregates at once and starts the next period.
func (a *App) SendNow() { a.send(a.Flush()) }
