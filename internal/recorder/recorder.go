// Package recorder is item 2pw's flight recorder: every run as a sequence of
// content-free spans, named after the OpenTelemetry GenAI conventions where one
// exists, kept in a bounded ring per chat. "Report this chat" sends what a run
// DID — the step sequence, where loops, wrong tools and lost context show — and
// never a word of what it was about. docs/telemetry-trace.md is the schema.
//
// Nothing here stores text. A tool argument is its key, its length and an
// HMAC under a salt that is made per run and never leaves this process.
package recorder

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"harness/internal/events"
)

// The bounds of (b) and (c).
const (
	RunsPerChat = 20
	TotalBytes  = 256 << 10
	// RunBytes keeps one runaway run from taking the whole ring; spans past it
	// are counted, not kept.
	RunBytes    = 32 << 10
	ReportRuns  = 5
	ReportBytes = 48 << 10
	loopRepeats = 3
)

// Sink receives the per-run telemetry events. The server's sink sends only when
// the anonymous switch is on; the recorder itself records either way, because
// a report is the one thing that may leave with the switch off.
type Sink func(eventType string, data map[string]any)

type Recorder struct {
	mu    sync.Mutex
	chats map[string][]*run
	live  map[string]*run
	order []*run // oldest first; head is the next to evict
	head  int
	kept  int
	total int
	sink  Sink
	// Item 2q6: a stopped run waits Settle for the detectors' verdicts, which
	// are published after run.stopped, before its aggregates leave.
	Settle  time.Duration
	closing map[string]*run
	global  globalState
}

// emitted is one telemetry event the recorder built.
type emitted struct {
	kind string
	data map[string]any
}

type run struct {
	chat     string
	invoke   map[string]any
	spans    []json.RawMessage
	bytes    int
	dropped  int
	evicted  bool
	seq      int
	salt     []byte
	started  time.Time
	asked    time.Time
	ttft     int64
	calls    int
	repeats  map[string]int
	pending  map[string]map[string]any
	nctx     int
	summary  map[string]any
	firstTTF int64
	health   *health
}

func New() *Recorder {
	return &Recorder{chats: map[string][]*run{}, live: map[string]*run{}, closing: map[string]*run{}, Settle: 2 * time.Second}
}

// SetSink installs where run.summary goes.
func (r *Recorder) SetSink(sink Sink) { r.mu.Lock(); r.sink = sink; r.mu.Unlock() }

// Bytes is the ring's total, for the bound's proof.
func (r *Recorder) Bytes() int { r.mu.Lock(); defer r.mu.Unlock(); return r.total }

var observed = map[string]bool{
	events.RunStarted: true, events.RunStopped: true, events.RunAborted: true, events.ChatDeleted: true,
	events.ModelRequest: true, events.ModelDelta: true, events.ModelResponse: true, events.ModelRetry: true,
	events.ToolCallEvent: true, events.ToolResult: true, events.ToolUnoffered: true, events.Compaction: true,
	events.CompactionSummary: true, events.ApprovalRequired: true, events.ApprovalDecided: true,
	events.ProgressShadow: true, events.ModelRefused: true, events.ConnectionHealth: true, events.UpdateChanged: true,
}

// Observe is the bus observer. Every branch costs the same however much the
// ring holds: one span is encoded once, appended once, and eviction pops from
// the front.
func (r *Recorder) Observe(event events.Event) {
	if !observed[event.Type] {
		return
	}
	data := asMap(event.Data)
	r.mu.Lock()
	var emit []emitted
	switch {
	case event.Type == events.ChatDeleted:
		r.forget(text(data["session_id"]))
	case event.SessionID == "":
		emit = r.global.observe(event.Type, data)
	default:
		r.observe(event, data)
	}
	r.mu.Unlock()
	r.send(emit)
}

// send hands events to the sink. The sink may post a full batch, so never on
// the publisher's goroutine.
func (r *Recorder) send(emit []emitted) {
	r.mu.Lock()
	sink := r.sink
	r.mu.Unlock()
	if sink == nil || len(emit) == 0 {
		return
	}
	go func() {
		for _, item := range emit {
			sink(item.kind, item.data)
		}
	}()
}

func (r *Recorder) observe(event events.Event, data map[string]any) {
	key := event.SessionID + "\x00" + event.RunID
	if event.Type == events.RunStarted {
		r.start(key, event.SessionID, stamp(event.TS))
		return
	}
	if closing := r.closing[key]; closing != nil && event.Type == events.ProgressShadow {
		closing.health.observe(event.Type, data, stamp(event.TS))
		return
	}
	current := r.live[key]
	if current == nil {
		return
	}
	at := stamp(event.TS)
	current.health.observe(event.Type, data, at)
	switch event.Type {
	case events.ModelRequest:
		if _, seen := current.invoke["gen_ai.request.model"]; !seen {
			current.invoke["gen_ai.provider.name"] = word(data["connection_kind"])
			current.invoke["gen_ai.request.model"] = word(filepath.Base(text(data["model_file"])))
			current.invoke["context_size"] = number(data["n_ctx"])
			current.invoke["prompt_template_hash"] = word(data["prompt_hash"])
			current.invoke["tools_offered"] = toolNames(data["tools_offered"])
		}
		current.nctx = number(data["n_ctx"])
		current.asked, current.ttft = at, -1
	case events.ModelDelta:
		if current.ttft < 0 && !current.asked.IsZero() {
			current.ttft = at.Sub(current.asked).Milliseconds()
		}
	case events.ModelResponse:
		usage := asMap(data["usage"])
		input := number(usage["prompt_tokens"])
		span := map[string]any{"span": "chat",
			"gen_ai.usage.input_tokens": input, "gen_ai.usage.output_tokens": number(usage["completion_tokens"]),
			"duration_ms": number(data["duration_ms"]), "gen_ai.response.finish_reasons": []string{word(data["finish_reason"])},
			"tool_calls_since_user": current.calls, "repeat_4gram_ratio": repeatRatio(text(data["content"])),
		}
		if cached, ok := usage["cached_tokens"]; ok && cached != nil {
			span["gen_ai.usage.cache_read.input_tokens"] = number(cached)
		}
		if current.nctx > 0 {
			fill := input * 100 / current.nctx
			span["context_fill_pct"] = fill
			current.summary["max_fill_pct"] = max(number(current.summary["max_fill_pct"]), fill)
		}
		if current.ttft >= 0 {
			span["ttft_ms"] = current.ttft
			if current.firstTTF < 0 {
				current.firstTTF = current.ttft
			}
		}
		current.summary["inference_calls"] = number(current.summary["inference_calls"]) + 1
		r.add(current, span)
	case events.ToolCallEvent:
		name := toolName(data["name"])
		args := asMap(data["args"])
		span := map[string]any{"span": "execute_tool", "gen_ai.tool.name": name, "offered": true, "args": current.describe(args)}
		hash := current.sign(name, args)
		span["retry_index"] = current.repeats[name+hash]
		current.repeats[name+hash]++
		current.pending[text(data["call_id"])] = span
		current.calls++
		current.summary["tool_calls"] = number(current.summary["tool_calls"]) + 1
		if invalid, _ := data["args_invalid"].(bool); invalid {
			r.add(current, map[string]any{"span": "tool_parse_error", "class": "arguments"})
		}
		if current.repeats[name+hash] == loopRepeats {
			current.summary["loop"] = true
			span["_loop"] = true // the loop span follows this call's own span
		}
	case events.ToolResult:
		span := current.pending[text(data["call_id"])]
		if span == nil {
			return
		}
		delete(current.pending, text(data["call_id"]))
		_, loop := span["_loop"]
		delete(span, "_loop")
		span["result"], span["result_bytes"], span["duration_ms"] = resultClass(data), number(data["bytes"]), number(data["ms"])
		r.add(current, span)
		if loop {
			r.add(current, map[string]any{"span": "loop", "gen_ai.tool.name": span["gen_ai.tool.name"], "repeats": loopRepeats})
		}
	case events.ToolUnoffered:
		r.add(current, map[string]any{"span": "execute_tool", "gen_ai.tool.name": toolName(data["name"]), "offered": false, "result": "not_offered"})
	case events.ModelRetry:
		switch reason := text(data["reason"]); reason {
		case "malformed_tool_turn", "truncated_tool_call", "malformed_tool_history":
			r.add(current, map[string]any{"span": "tool_parse_error", "class": reason})
		}
	case events.Compaction:
		r.add(current, map[string]any{"span": "condensed", "kind": word(data["kind"]), "trigger": word(data["trigger"]), "before": number(data["before"]), "after": number(data["after"])})
	case events.RunAborted:
		r.add(current, map[string]any{"span": "cancelled"})
	case events.RunStopped:
		reason := word(data["reason"])
		switch {
		case strings.HasPrefix(reason, "context_"):
			r.add(current, map[string]any{"span": "context_exceeded", "reason": reason})
		case strings.HasPrefix(reason, "aborted_"):
			r.add(current, map[string]any{"span": "cancelled", "reason": reason})
		}
		current.invoke["stop_reason"] = reason
		delete(r.live, key)
		current.summary["stop_reason"] = reason
		if current.firstTTF >= 0 {
			current.summary["ttft_ms"] = current.firstTTF
		}
		if !current.started.IsZero() && !at.IsZero() {
			current.summary["wall_seconds"] = int(at.Sub(current.started).Seconds())
		}
		current.repeats, current.pending = nil, nil
		r.closing[key] = current
		time.AfterFunc(r.Settle, func() { r.close(key) })
	}
}

// close sends a stopped run's aggregates once the detectors have spoken.
func (r *Recorder) close(key string) {
	r.mu.Lock()
	current := r.closing[key]
	delete(r.closing, key)
	var emit []emitted
	if current != nil {
		emit = current.health.emits(current.summary)
		current.summary, current.health = nil, nil
	}
	r.mu.Unlock()
	r.send(emit)
}

func (r *Recorder) start(key, chat string, at time.Time) {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	current := &run{chat: chat, salt: salt, started: at, ttft: -1, firstTTF: -1,
		invoke:  map[string]any{"span": "invoke_agent", "run": randomID()},
		repeats: map[string]int{}, pending: map[string]map[string]any{},
		summary: map[string]any{"inference_calls": 0, "tool_calls": 0, "max_fill_pct": 0, "loop": false}, health: newHealth(),
	}
	r.live[key] = current
	r.chats[chat] = append(r.chats[chat], current)
	r.order = append(r.order, current)
	r.kept++
	if list := r.chats[chat]; len(list) > RunsPerChat {
		r.drop(list[0])
	}
}

// add stamps, encodes once and appends: the per-event cost of (b).
func (r *Recorder) add(current *run, span map[string]any) {
	current.seq++
	span["seq"] = current.seq
	if current.evicted {
		return
	}
	encoded, err := json.Marshal(span)
	if err != nil || current.bytes+len(encoded) > RunBytes {
		current.dropped++
		return
	}
	current.spans = append(current.spans, encoded)
	current.bytes += len(encoded)
	r.total += len(encoded)
	for r.total > TotalBytes && r.head < len(r.order) {
		oldest := r.order[r.head]
		r.order[r.head] = nil
		r.head++
		r.drop(oldest)
	}
}

// drop evicts one run from its chat and the total. The order list is compacted
// when evicted entries outnumber kept ones, so it is bounded too.
func (r *Recorder) drop(old *run) {
	if old == nil || old.evicted {
		return
	}
	old.evicted = true
	r.total -= old.bytes
	r.kept--
	old.spans, old.bytes = nil, 0
	list := r.chats[old.chat]
	for index, item := range list {
		if item == old {
			list = append(list[:index:index], list[index+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(r.chats, old.chat)
	} else {
		r.chats[old.chat] = list
	}
	if live := len(r.order) - r.head; live > 2*r.kept+64 {
		next := make([]*run, 0, r.kept)
		for _, item := range r.order[r.head:] {
			if item != nil && !item.evicted {
				next = append(next, item)
			}
		}
		r.order, r.head = next, 0
	}
}

// forget is (f): a chat's ring goes with the chat.
func (r *Recorder) forget(chat string) {
	for _, item := range append([]*run(nil), r.chats[chat]...) {
		r.drop(item)
	}
	for key, item := range r.live {
		if item.chat == chat {
			delete(r.live, key)
		}
	}
}

// Trace is (c): the chat's last five runs as one trace event's data, oldest
// runs dropped first until it fits. False when nothing was recorded.
func (r *Recorder) Trace(chat string) (map[string]any, bool) {
	r.mu.Lock()
	list := r.chats[chat]
	if len(list) > ReportRuns {
		list = list[len(list)-ReportRuns:]
	}
	runs := make([]json.RawMessage, 0, len(list))
	size := 0
	for _, item := range list {
		invoke, _ := json.Marshal(item.invoke)
		spans := append([]json.RawMessage{invoke}, item.spans...)
		encoded, _ := json.Marshal(map[string]any{"spans": spans, "spans_dropped": item.dropped})
		runs = append(runs, encoded)
		size += len(encoded)
	}
	r.mu.Unlock()
	for len(runs) > 0 && size > ReportBytes-1024 {
		size -= len(runs[0])
		runs = runs[1:]
	}
	if len(runs) == 0 {
		return nil, false
	}
	return map[string]any{"report_id": randomID(), "runs": runs}, true
}

// describe is an argument as its keys, each value's length, and an HMAC that
// says "the same value" without saying what it was.
func (current *run) describe(args map[string]any) []map[string]any {
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		value := text(args[key])
		if _, isText := args[key].(string); !isText {
			encoded, _ := json.Marshal(args[key])
			value = string(encoded)
		}
		out = append(out, map[string]any{"key": keyName(key), "len": len([]rune(value)), "hmac": current.mac(value)})
	}
	return out
}

func (current *run) sign(name string, args map[string]any) string {
	encoded, _ := json.Marshal(args)
	return current.mac(name + "\x00" + string(encoded))
}

func (current *run) mac(value string) string {
	mac := hmac.New(sha256.New, current.salt)
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil)[:4])
}

// repeatRatio is the share of the output's word 4-grams that repeat an earlier
// one: a number, and nothing of the words.
func repeatRatio(content string) float64 {
	words := strings.Fields(content)
	if len(words) > 4096 {
		words = words[:4096]
	}
	if len(words) < 4 {
		return 0
	}
	seen := make(map[string]bool, len(words))
	repeated := 0
	for index := 0; index+4 <= len(words); index++ {
		gram := strings.Join(words[index:index+4], " ")
		if seen[gram] {
			repeated++
		}
		seen[gram] = true
	}
	return float64(int(float64(repeated)/float64(len(words)-3)*100)) / 100
}

func resultClass(data map[string]any) string {
	if ok, _ := data["ok"].(bool); !ok {
		if class := word(data["class"]); class != "" {
			return class
		}
		return "error"
	}
	if number(data["bytes"]) == 0 {
		return "empty"
	}
	return "ok"
}

var (
	wordPattern = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,64}$`)
	namePattern = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,39}$`)
	keyPattern  = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,31}$`)
)

// word admits a fixed-vocabulary word and nothing that could be text.
func word(value any) string {
	if candidate := text(value); wordPattern.MatchString(candidate) {
		return candidate
	}
	return ""
}

func toolName(value any) string {
	if candidate := text(value); namePattern.MatchString(candidate) {
		return candidate
	}
	return "<invalid>"
}

func keyName(key string) string {
	if keyPattern.MatchString(key) {
		return key
	}
	return "<key>"
}

func toolNames(value any) []string {
	var names []string
	switch list := value.(type) {
	case []string:
		names = list
	case []any:
		for _, item := range list {
			names = append(names, text(item))
		}
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, toolName(name))
	}
	return out
}

func randomID() string {
	var raw [4]byte
	_, _ = rand.Read(raw[:])
	return hex.EncodeToString(raw[:])
}

func stamp(value string) time.Time {
	at, _ := time.Parse(time.RFC3339Nano, value)
	return at
}

func text(value any) string { candidate, _ := value.(string); return candidate }

func number(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case *int:
		if typed != nil {
			return *typed
		}
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	}
	return 0
}

// asMap reads an event's data; the few events that carry a struct are read in
// their wire form.
func asMap(value any) map[string]any {
	if data, ok := value.(map[string]any); ok {
		return data
	}
	if value == nil {
		return map[string]any{}
	}
	encoded, err := json.Marshal(value)
	out := map[string]any{}
	if err == nil {
		_ = json.Unmarshal(encoded, &out)
	}
	return out
}
