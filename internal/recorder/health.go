package recorder

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"harness/internal/events"
)

// Item 2q6: what went wrong, said without anyone relaying it. A run's health is
// gathered as it goes and leaves as a handful of aggregates when it stops,
// never one event per request: a forty-call run adds a dozen events at most.
// Every value is a count, a duration, a size or a fixed word.

const maxWaits, maxRefusals = 4, 4

var perfMetrics = []string{"ttft_ms", "prompt_ms", "tokens_per_second", "prompt_tokens", "cached_tokens", "completion_tokens", "reasoning_tokens"}

var behaviourCounts = []string{"empty_replies", "unparseable_tool_calls", "unoffered_tool_calls", "cut_by_length", "thinking_only_answers"}

type health struct {
	kind                     string
	asked                    time.Time
	streamed                 bool
	samples                  map[string][]int
	prompt, cached           int
	completion, reasoning    int
	compacted                bool
	promptAfter, cachedAfter int
	estimate, drift          int
	measured                 bool
	tools                    map[string]*toolHealth
	seenCalls                map[string]bool
	callTool                 map[string]string
	behaviour                map[string]int
	detectors                []string
	compactions              int
	compaction               map[string]any
	outcomes                 map[string]int
	approvals                map[string]approval
	waits                    []map[string]any
	waitSeconds              int
	refusals                 map[string]map[string]any
}

type toolHealth struct {
	calls, cut, repeats, maxBytes int
	errors                        map[string]int
	durations                     []int
}

type approval struct {
	kind string
	at   time.Time
}

func newHealth() *health {
	return &health{samples: map[string][]int{}, tools: map[string]*toolHealth{}, seenCalls: map[string]bool{}, callTool: map[string]string{},
		behaviour: map[string]int{}, outcomes: map[string]int{}, approvals: map[string]approval{}, refusals: map[string]map[string]any{}}
}

func (h *health) observe(eventType string, data map[string]any, at time.Time) {
	switch eventType {
	case events.ModelRequest:
		if h.kind == "" {
			h.kind = word(data["connection_kind"])
		}
		h.estimate, h.asked, h.streamed = number(data["est_prompt_tokens"]), at, false
	case events.ModelDelta:
		if !h.streamed && !h.asked.IsZero() {
			h.streamed = true
			h.sample("ttft_ms", int(at.Sub(h.asked).Milliseconds()))
		}
	case events.ModelResponse:
		h.response(data)
	case events.ToolCallEvent:
		name := toolName(data["name"])
		tool := h.tool(name)
		tool.calls++
		h.callTool[text(data["call_id"])] = name
		encoded, _ := json.Marshal(data["args"])
		if key := name + "\x00" + string(encoded); h.seenCalls[key] {
			tool.repeats++
		} else {
			h.seenCalls[key] = true
		}
		if invalid, _ := data["args_invalid"].(bool); invalid {
			h.behaviour["unparseable_tool_calls"]++
		}
	case events.ToolUnoffered:
		h.behaviour["unoffered_tool_calls"]++
	case events.ModelRetry:
		switch text(data["reason"]) {
		case "malformed_tool_turn", "truncated_tool_call", "malformed_tool_history":
			h.behaviour["unparseable_tool_calls"]++
		}
	case events.ToolResult:
		id := text(data["call_id"])
		tool := h.tool(h.callTool[id])
		delete(h.callTool, id)
		if ok, _ := data["ok"].(bool); !ok {
			tool.errors[resultClass(data)]++
		}
		tool.durations = append(tool.durations, number(data["ms"]))
		tool.maxBytes = max(tool.maxBytes, number(data["bytes"]))
		for _, flag := range []string{"result_too_large", "truncated", "elided"} {
			if cut, _ := data[flag].(bool); cut {
				tool.cut++
				break
			}
		}
	case events.Compaction:
		h.compactions++
		h.compacted = true
		before := number(data["before"])
		if h.compaction != nil {
			before = max(before, number(h.compaction["before"]))
		}
		h.compaction = map[string]any{"kind": word(data["kind"]), "trigger": word(data["trigger"]), "before": before, "after": number(data["after"])}
	case events.CompactionSummary:
		h.outcomes[word(data["outcome"])]++
	case events.ApprovalRequired:
		kind := word(data["kind"])
		if kind == "" {
			kind = word(data["name"])
		}
		h.approvals[text(data["call_id"])] = approval{kind: kind, at: at}
	case events.ApprovalDecided:
		h.decided(data, at)
	case events.ModelRefused:
		key := text(data["error_type"]) + "\x00" + text(data["status"])
		if refusal := h.refusals[key]; refusal != nil {
			refusal["count"] = number(refusal["count"]) + 1
		} else if len(h.refusals) < maxRefusals {
			h.refusals[key] = map[string]any{"status": number(data["status"]), "error_type": word(data["error_type"]), "connection_kind": word(data["connection_kind"]), "count": 1}
		}
	case events.ProgressShadow:
		if fired, _ := data["would_fire"].(bool); fired {
			h.detectors = append(h.detectors, word(data["detector"]))
		}
	}
}

func (h *health) response(data map[string]any) {
	usage, timings := asMap(data["usage"]), asMap(data["timings"])
	prompt, completion, reasoning := number(usage["prompt_tokens"]), number(usage["completion_tokens"]), number(data["reasoning_tokens"])
	cached := number(usage["cached_tokens"])
	h.prompt, h.cached, h.completion, h.reasoning = h.prompt+prompt, h.cached+cached, h.completion+completion, h.reasoning+reasoning
	// The first call after a compaction is where the cache drops (s52: 17-27%
	// against 99% either side); later calls have recovered.
	if h.compacted {
		h.promptAfter, h.cachedAfter, h.compacted = h.promptAfter+prompt, h.cachedAfter+cached, false
	}
	h.sample("prompt_tokens", prompt)
	h.sample("cached_tokens", cached)
	h.sample("completion_tokens", completion)
	h.sample("reasoning_tokens", reasoning)
	if value, ok := timings["prompt_ms"]; ok {
		h.sample("prompt_ms", number(value))
	}
	if value, ok := timings["predicted_per_second"]; ok {
		h.sample("tokens_per_second", number(value))
	} else if duration := number(data["duration_ms"]); duration > 0 && completion > 0 {
		h.sample("tokens_per_second", completion*1000/duration)
	}
	if h.estimate > 0 && prompt > 0 {
		gap := h.estimate - prompt
		if gap < 0 {
			gap = -gap
		}
		h.drift, h.measured = max(h.drift, gap*100/prompt), true
	}
	calls, _ := data["tool_calls"].([]any)
	if typed, ok := data["tool_calls"].([]events.ToolCall); ok {
		calls = make([]any, len(typed))
	}
	if strings.TrimSpace(text(data["content"])) == "" && len(calls) == 0 {
		if reasoning > 0 {
			h.behaviour["thinking_only_answers"]++
		} else {
			h.behaviour["empty_replies"]++
		}
	}
	if text(data["finish_reason"]) == "length" {
		h.behaviour["cut_by_length"]++
	}
}

func (h *health) decided(data map[string]any, at time.Time) {
	id := text(data["call_id"])
	waiting, ok := h.approvals[id]
	if !ok {
		return
	}
	delete(h.approvals, id)
	seconds := 0
	if !waiting.at.IsZero() && !at.IsZero() {
		seconds = int(at.Sub(waiting.at).Seconds())
	}
	h.waitSeconds += seconds
	outcome := "answered"
	switch decision := text(data["decision"]); {
	case decision == "deny" && data["unanswered"] == true:
		outcome = "refused_after_10_minutes"
	case decision == "deny":
		outcome = "denied"
	case decision == "dismissed":
		outcome = "dismissed"
	}
	if len(h.waits) < maxWaits {
		h.waits = append(h.waits, map[string]any{"card_kind": waiting.kind, "seconds": seconds, "outcome": outcome})
	}
}

func (h *health) sample(metric string, value int) {
	h.samples[metric] = append(h.samples[metric], value)
}

func (h *health) tool(name string) *toolHealth {
	if name == "" {
		name = "<invalid>"
	}
	tool := h.tools[name]
	if tool == nil {
		tool = &toolHealth{errors: map[string]int{}}
		h.tools[name] = tool
	}
	return tool
}

// struggling is item 2qa's closed list. It is evaluated once when the run
// settles, after late progress shadows have had their existing grace period.
func (h *health) struggling(stopReason string) bool {
	if stopReason != "done" || len(h.detectors) > 0 || len(h.refusals) > 0 ||
		h.behaviour["unparseable_tool_calls"] > 0 || h.behaviour["unoffered_tool_calls"] > 0 {
		return true
	}
	errors := 0
	for _, tool := range h.tools {
		for _, count := range tool.errors {
			errors += count
		}
	}
	return errors >= 2
}

// emits is the run's aggregates, run.summary first.
func (h *health) emits(summary map[string]any) []emitted {
	if h == nil || summary == nil {
		return nil
	}
	summary["reasoning_tokens"], summary["compactions"], summary["approval_wait_seconds"] = h.reasoning, h.compactions, h.waitSeconds
	out := []emitted{{events.RunSummary, summary}}
	if calls := len(h.samples["prompt_tokens"]); calls > 0 {
		perf := map[string]any{"connection_kind": h.kind, "calls": calls, "cache_hit_pct": percent(h.cached, h.prompt), "reasoning_share_pct": percent(h.reasoning, h.completion)}
		for _, metric := range perfMetrics {
			if values := h.samples[metric]; len(values) > 0 {
				perf[metric] = spread(values, true)
			}
		}
		if h.promptAfter > 0 {
			perf["cache_hit_pct_after_compaction"] = percent(h.cachedAfter, h.promptAfter)
		}
		out = append(out, emitted{events.ModelPerf, perf})
	}
	if len(h.tools) > 0 {
		tools := map[string]any{}
		for name, tool := range h.tools {
			tools[name] = map[string]any{"calls": tool.calls, "errors": tool.errors, "duration_ms": spread(tool.durations, false),
				"size_bucket": sizeBucket(tool.maxBytes), "cut": tool.cut, "repeats": tool.repeats}
		}
		out = append(out, emitted{events.ToolPerf, map[string]any{"tools": tools}})
	}
	behaviour := map[string]any{"detectors": append([]string{}, h.detectors...)}
	for _, name := range behaviourCounts {
		behaviour[name] = h.behaviour[name]
	}
	out = append(out, emitted{events.ModelBehaviour, behaviour})
	if h.measured {
		out = append(out, emitted{events.BudgetDrift, map[string]any{"max_pct": h.drift}})
	}
	if h.compaction != nil {
		h.compaction["count"], h.compaction["outcomes"] = h.compactions, h.outcomes
		out = append(out, emitted{events.Compaction, h.compaction})
	}
	for _, wait := range h.waits {
		out = append(out, emitted{events.ApprovalWait, wait})
	}
	keys := make([]string, 0, len(h.refusals))
	for key := range h.refusals {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, emitted{events.ModelRefused, h.refusals[key]})
	}
	return out
}

// spread is p50, p95 and, when asked, the maximum.
func spread(values []int, withMax bool) map[string]int {
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	if len(sorted) == 0 {
		return map[string]int{"p50": 0, "p95": 0}
	}
	out := map[string]int{"p50": sorted[(len(sorted)-1)*50/100], "p95": sorted[(len(sorted)-1)*95/100]}
	if withMax {
		out["max"] = sorted[len(sorted)-1]
	}
	return out
}

func percent(part, whole int) int {
	if whole <= 0 {
		return 0
	}
	return part * 100 / whole
}

func sizeBucket(bytes int) string {
	switch {
	case bytes < 1<<10:
		return "under_1k"
	case bytes < 10<<10:
		return "under_10k"
	case bytes < 100<<10:
		return "under_100k"
	}
	return "100k_or_more"
}

// globalState watches what happens around the runs: a connection's state
// changing, and the updater's checks and installs. Connection ids stay here.
type globalState struct {
	lamps     map[string]string
	checked   string
	installed string
}

func (g *globalState) observe(eventType string, data map[string]any) []emitted {
	switch eventType {
	case events.ConnectionHealth:
		state := asMap(data["health"])
		lamp := word(state["lamp"])
		if lamp == "" {
			lamp = "checking"
		}
		if g.lamps == nil {
			g.lamps = map[string]string{}
		}
		id := text(data["connection_id"])
		from, seen := g.lamps[id]
		g.lamps[id] = lamp
		if seen && from == lamp {
			return nil
		}
		if !seen {
			from = "unknown"
		}
		return []emitted{{events.ConnectionState, map[string]any{"from": from, "to": lamp, "cause": word(strings.ReplaceAll(text(state["word"]), " ", "_"))}}}
	case events.UpdateChanged:
		var out []emitted
		current := word(data["current_version"])
		if at := text(data["checked_at"]); at != "" && at != g.checked {
			g.checked = at
			check := "current"
			if text(data["error"]) != "" {
				check = "error"
			} else if data["available"] == true {
				check = "available"
			}
			out = append(out, emitted{events.UpdateResult, map[string]any{"check": check, "from": current, "to": word(data["version"])}})
		}
		outcome := asMap(data["outcome"])
		if at := text(outcome["at"]); at != "" && at != g.installed {
			g.installed = at
			install := "ok"
			if outcome["ok"] != true {
				install = "failed_" + word(outcome["phase"])
			}
			out = append(out, emitted{events.UpdateResult, map[string]any{"install": install, "from": current, "to": word(outcome["version"])}})
		}
		return out
	}
	return nil
}
