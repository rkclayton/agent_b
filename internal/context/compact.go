// Package contextmgr batches compaction, largest stale result first (item 2ey).
// Editing the prompt prefix invalidates the model cache, so doing this rarely and
// in one batch minimizes repeated prefill work.
package contextmgr

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"harness/internal/events"
	"harness/internal/session"
)

type Counter func(string) (int, bool)
type Compactor struct{ bus *events.Bus }

func New(bus *events.Bus) *Compactor { return &Compactor{bus: bus} }

// pinIndex returns the first index compaction may not touch. Everything from it
// onward is the running turn's user message and the work answering it.
//
// No run in flight (empty pin) leaves the whole history touchable, which is the
// behaviour outside a run. A pin that is set but no longer present is a bug
// somewhere upstream, and the honest response is to touch nothing rather than
// guess, so it returns 0.
func pinIndex(messages []events.Message, pin string) int {
	if pin == "" {
		return len(messages)
	}
	for index, message := range messages {
		if message.ID == pin {
			return index
		}
	}
	return 0
}

// RecentToolWindow is how many of the newest tool results elision keeps
// verbatim, inside the running turn or not (item 2et).
const RecentToolWindow = 4

func pinPresent(messages []events.Message, pin string) bool {
	for _, message := range messages {
		if message.ID == pin {
			return true
		}
	}
	return false
}

// StubOlderResults returns messages with every un-elided tool result that
// precedes the last user message replaced by its elision stub, the form a
// retained chat is restored in: the JSONL keeps the bytes, the next request
// carries only what the last turn read (item 2et).
func StubOlderResults(messages []events.Message, readDefaultLimit int) []events.Message {
	lastUser := -1
	for index, message := range messages {
		if message.Role == "user" {
			lastUser = index
		}
	}
	out := append([]events.Message(nil), messages...)
	estimate := func(text string) (int, bool) { return (len([]rune(text))*10 + 35) / 36, true }
	for index := 0; index < lastUser; index++ {
		item := out[index]
		if item.Role != "tool" || item.Elided || (item.Category != "files" && item.Category != "results" && item.Category != "fetched") {
			continue
		}
		call, _ := callFor(out, item.ToolCallID)
		out[index] = elide(item, call.Arguments, readDefaultLimit, estimate)
	}
	return out
}

// atomicFoldEnd pulls a summarize span back so no tool call is folded while its
// result is kept. Results are always after their call, so moving the boundary
// to the earliest such call is enough and terminates in one pass.
func atomicFoldEnd(messages []events.Message, foldEnd int) int {
	callIndex := map[string]int{}
	for index, message := range messages {
		for _, call := range message.ToolCalls {
			callIndex[call.ID] = index
		}
	}
	for index := foldEnd; index < len(messages); index++ {
		if messages[index].Role != "tool" || messages[index].ToolCallID == "" {
			continue
		}
		if at, ok := callIndex[messages[index].ToolCallID]; ok && at < foldEnd {
			foldEnd = at
		}
	}
	return foldEnd
}

func (c *Compactor) Supersede(s *session.Session, runID string, turn, readDefaultLimit int, count Counter) bool {
	messages := s.MessagesCopy()
	changed := false
	affected := []string{}
	before := tokenSum(messages)
	pin := pinIndex(messages, s.RunPin())
	for current := range messages {
		item := messages[current]
		if item.Role != "tool" || item.Turn != turn || item.Elided || !successful(item) || (item.Name != "read_file" && item.Name != "search_text") {
			continue
		}
		currentCall, ok := callFor(messages, item.ToolCallID)
		if !ok {
			continue
		}
		for prior := 0; prior < current && prior < pin; prior++ {
			older := messages[prior]
			if older.Role != "tool" || older.Name != item.Name || older.Elided || !successful(older) {
				continue
			}
			olderCall, ok := callFor(messages, older.ToolCallID)
			if !ok || !supersedes(item.Name, olderCall.Arguments, currentCall.Arguments, readDefaultLimit) {
				continue
			}
			messages[prior] = elide(older, olderCall.Arguments, readDefaultLimit, count)
			c.updated(s, runID, messages[prior])
			affected = append(affected, older.ID)
			changed = true
		}
	}
	if changed {
		s.ReplaceMessages(messages)
		after := tokenSum(messages)
		s.RecordCompaction(after - before)
		c.bus.Publish(events.New(events.Compaction, s.ID, runID, map[string]any{"kind": "elide", "before": before, "after": after, "affected_ids": affected}))
	}
	return changed
}

func successful(message events.Message) bool { return message.OK != nil && *message.OK }

func eligibleOldElision(message events.Message) bool {
	if message.OK != nil {
		return *message.OK
	}
	prefix := strings.ToLower(strings.TrimSpace(message.Content))
	return !strings.HasPrefix(prefix, "error:") && !strings.HasPrefix(prefix, "note:")
}

// ElideOld stubs eligible stale results until used falls to target. Item 2ey:
// the largest eligible result goes first (ties oldest first), so one pass on a
// small window frees the most; trigger names the line that asked for the pass
// (soft_pct, overflow) and is recorded on the compaction event.
func (c *Compactor) ElideOld(s *session.Session, runID, trigger string, used, target, readDefaultLimit int, count Counter) (bool, int) {
	messages := s.MessagesCopy()
	toolIndexes := []int{}
	for index, item := range messages {
		if item.Role == "tool" && !item.Elided {
			toolIndexes = append(toolIndexes, index)
		}
	}
	skip := map[int]bool{}
	for _, index := range toolIndexes[max(0, len(toolIndexes)-RecentToolWindow):] {
		skip[index] = true
	}
	// Item 2et: the running turn's user message and the model's own messages are
	// never elided (only tool-result categories are candidates), and neither are
	// the last RecentToolWindow tool results. Older results inside the running
	// turn may become stubs, largest first, before the run stops for context.
	// Summaries still never reach into the running turn (SummarizeSpan).
	// A pin that is set but missing still protects everything.
	if pin := s.RunPin(); pin != "" && !pinPresent(messages, pin) {
		return false, used
	}
	affected := []string{}
	before := used
	order := make([]int, 0, len(messages))
	for index, item := range messages {
		if skip[index] || item.Elided || !eligibleOldElision(item) || (item.Category != "files" && item.Category != "results" && item.Category != "fetched") {
			continue
		}
		order = append(order, index)
	}
	sort.SliceStable(order, func(a, b int) bool { return messages[order[a]].Tokens > messages[order[b]].Tokens })
	for _, index := range order {
		if used <= target {
			break
		}
		item := messages[index]
		call, _ := callFor(messages, item.ToolCallID)
		updated := elide(item, call.Arguments, readDefaultLimit, count)
		// v0.65.0/W11: a result whose stub is no smaller (a fresh in-turn result
		// not yet weighed) frees nothing; eliding it would only hide its bytes
		// and publish a compaction with before == after.
		if updated.Tokens >= item.Tokens {
			continue
		}
		used -= item.Tokens - updated.Tokens
		messages[index] = updated
		affected = append(affected, item.ID)
		c.updated(s, runID, updated)
	}
	if len(affected) == 0 || used >= before {
		return false, before
	}
	s.ReplaceMessages(messages)
	s.RecordCompaction(used - before)
	c.bus.Publish(events.New(events.Compaction, s.ID, runID, map[string]any{"kind": "elide", "trigger": trigger, "before": before, "after": used, "affected_ids": affected}))
	return true, used
}

// SummarizeSpan reports the exclusive end of the span Summarize would fold, so
// the caller can build the note's header and prompt from the same messages that
// are about to disappear. It is a pure read of the session.
func SummarizeSpan(messages []events.Message, pin string) (int, bool) {
	if len(messages) <= 7 {
		return 0, false
	}
	foldEnd := atomicFoldEnd(messages, min(max(1, len(messages)-6), pinIndex(messages, pin)))
	if foldEnd <= 1 {
		return 0, false
	}
	return foldEnd, true
}

func (c *Compactor) Summarize(s *session.Session, runID string, summary events.Message, source events.CompactionSummaryData) bool {
	messages := s.MessagesCopy()
	if len(messages) <= 7 {
		return false
	}
	// The span ends at the earlier of the retention window and the run pin, then
	// retreats far enough that no folded tool call loses its kept result.
	foldEnd := min(max(1, len(messages)-6), pinIndex(messages, s.RunPin()))
	foldEnd = atomicFoldEnd(messages, foldEnd)
	if foldEnd <= 1 {
		source.Outcome = "rejected"
		source.Reason = "nothing outside the running turn left to summarize"
		c.bus.Publish(events.New(events.CompactionSummary, s.ID, runID, source))
		return false
	}
	affected := []string{}
	out := []events.Message{messages[0], summary}
	for index := 1; index < len(messages); index++ {
		if index >= foldEnd {
			out = append(out, messages[index])
		} else {
			affected = append(affected, messages[index].ID)
		}
	}
	if len(affected) == 0 {
		source.Outcome = "rejected"
		source.Reason = "nothing outside the running turn left to summarize"
		c.bus.Publish(events.New(events.CompactionSummary, s.ID, runID, source))
		return false
	}
	before, after := tokenSum(messages), tokenSum(out)
	if after >= before {
		source.Outcome = "rejected"
		source.Reason = "summary did not reduce session context"
		c.bus.Publish(events.New(events.CompactionSummary, s.ID, runID, source))
		return false
	}
	s.ReplaceMessages(out)
	s.RecordCompaction(after - before)
	source.Outcome = "accepted"
	c.bus.Publish(events.New(events.CompactionSummary, s.ID, runID, source))
	c.bus.Publish(events.New(events.Compaction, s.ID, runID, map[string]any{"kind": "summarize", "trigger": source.Trigger, "before": before, "after": after, "affected_ids": affected, "summary_message_id": summary.ID, "role": source.Role, "profile_id": source.ProfileID, "model": source.Model, "fallback_reason": source.FallbackReason, "usage": source.Usage}))
	return true
}

// Settle replaces an operator-selected design-chat span with one readable
// pointer after its proposal is accepted. It never selects the span itself.
func (c *Compactor) Settle(s *session.Session, runID, pointer string, ids []string, count Counter) bool {
	wanted := map[string]bool{}
	for _, id := range ids {
		if strings.TrimSpace(id) != "" {
			wanted[id] = true
		}
	}
	if len(wanted) == 0 {
		return false
	}
	messages := s.MessagesCopy()
	affected := []string{}
	before := tokenSum(messages)
	for index, message := range messages {
		if !wanted[message.ID] || message.Elided || (message.Role != "user" && message.Role != "assistant") {
			continue
		}
		message.Content = pointer
		message.ToolCalls = nil
		message.Reasoning = ""
		message.Elided = true
		message.Tokens, message.Estimated = count(pointer)
		messages[index] = message
		c.updated(s, runID, message)
		affected = append(affected, message.ID)
	}
	if len(affected) == 0 {
		return false
	}
	s.ReplaceMessages(messages)
	after := tokenSum(messages)
	s.RecordCompaction(after - before)
	c.bus.Publish(events.New(events.Compaction, s.ID, runID, map[string]any{"kind": "settled", "before": before, "after": after, "affected_ids": affected}))
	return true
}

func (c *Compactor) updated(s *session.Session, runID string, message events.Message) {
	c.bus.Publish(events.New(events.MessageUpdated, s.ID, runID, map[string]any{"id": message.ID, "patch": map[string]any{"content": message.Content, "tokens": message.Tokens, "elided": true}}))
}
func elide(message events.Message, rawArgs string, readDefaultLimit int, count Counter) events.Message {
	label := keyArgs(message.Name, rawArgs, readDefaultLimit)
	stub := fmt.Sprintf("[elided: %s %s, %d tokens]", message.Name, label, message.Tokens)
	message.Content = stub
	message.Elided = true
	message.Tokens, message.Estimated = count(stub)
	return message
}
func callFor(messages []events.Message, id string) (events.ToolCall, bool) {
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if call.ID == id {
				return call, true
			}
		}
	}
	return events.ToolCall{}, false
}
func supersedes(name, older, current string, readDefaultLimit int) bool {
	if name == "search_text" {
		return canonical(older) == canonical(current)
	}
	var a, b struct {
		Path    string          `json:"path"`
		Offset  int             `json:"offset"`
		Limit   int             `json:"limit"`
		Line    int             `json:"line"`
		Lines   int             `json:"lines"`
		Windows json.RawMessage `json:"windows"`
	}
	if json.Unmarshal([]byte(older), &a) != nil || json.Unmarshal([]byte(current), &b) != nil || a.Path != b.Path || len(a.Windows) > 0 || len(b.Windows) > 0 {
		return false
	}
	if a.Line > 0 || b.Line > 0 {
		if a.Line == 0 || b.Line == 0 {
			return false
		}
		if a.Lines == 0 {
			a.Lines = 200
		}
		if b.Lines == 0 {
			b.Lines = 200
		}
		return a.Line <= b.Line+b.Lines-1 && b.Line <= a.Line+a.Lines-1
	}
	if a.Offset == 0 {
		a.Offset = 1
	}
	if b.Offset == 0 {
		b.Offset = 1
	}
	if a.Limit == 0 {
		a.Limit = readDefaultLimit
	}
	if b.Limit == 0 {
		b.Limit = readDefaultLimit
	}
	return a.Offset <= b.Offset+b.Limit-1 && b.Offset <= a.Offset+a.Limit-1
}
func canonical(raw string) string {
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return raw
	}
	data, _ := json.Marshal(value)
	return string(data)
}
func keyArgs(name, raw string, readDefaultLimit int) string {
	var args map[string]any
	_ = json.Unmarshal([]byte(raw), &args)
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := []string{}
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s %v", key, args[key]))
	}
	if name == "read_file" {
		path, _ := args["path"].(string)
		if line := number(args["line"], 0); line > 0 {
			lines := number(args["lines"], 200)
			return fmt.Sprintf("%s lines %d–%d", path, line, line+lines-1)
		}
		offset := number(args["offset"], 1)
		limit := number(args["limit"], readDefaultLimit)
		return fmt.Sprintf("%s bytes %d–%d", path, offset, offset+limit-1)
	}
	return strings.Join(parts, " ")
}
func number(value any, fallback int) int {
	if n, ok := value.(float64); ok {
		return int(n)
	}
	return fallback
}
func tokenSum(messages []events.Message) int {
	total := 0
	for _, message := range messages {
		total += message.Tokens
	}
	return total
}
