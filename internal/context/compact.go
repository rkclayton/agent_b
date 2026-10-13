// Package contextmgr owns request-context replacement and restore-time stubs.
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

// StubOlderResults restores retained chats without replaying old result bodies.
func StubOlderResults(messages []events.Message, readDefaultLimit int) []events.Message {
	return StubResultsBefore(messages, "", readDefaultLimit)
}

func StubResultsBefore(messages []events.Message, anchor string, readDefaultLimit int) []events.Message {
	older := func(int) bool { return false }
	if limit, ok := idNumber(anchor); ok {
		older = func(index int) bool {
			value, ok := idNumber(messages[index].ID)
			return ok && value < limit
		}
	} else {
		lastUser := -1
		for index, message := range messages {
			if message.Role == "user" {
				lastUser = index
			}
		}
		older = func(index int) bool { return index < lastUser }
	}
	out := append([]events.Message(nil), messages...)
	estimate := func(text string) (int, bool) { return (len([]rune(text))*10 + 35) / 36, true }
	for index := range out {
		item := out[index]
		if !older(index) || item.Role != "tool" || item.Elided || (item.Category != "files" && item.Category != "results" && item.Category != "fetched") {
			continue
		}
		call, _ := callFor(out, item.ToolCallID)
		out[index] = elide(item, call.Arguments, readDefaultLimit, estimate)
		clearReasoningForCall(out, item.ToolCallID)
	}
	return out
}

func idNumber(id string) (int64, bool) {
	end, start := len(id), len(id)
	for start > 0 && id[start-1] >= '0' && id[start-1] <= '9' {
		start--
	}
	if start == end || start == 0 || id[start-1] != '-' || end-start > 15 {
		return 0, false
	}
	var value int64
	for _, digit := range id[start:end] {
		value = value*10 + int64(digit-'0')
	}
	return value, true
}

type Remint struct {
	Index int    `json:"index"`
	From  string `json:"from"`
	To    string `json:"to"`
}

func RemintDuplicateIDs(messages []events.Message, floor int64) ([]events.Message, []Remint, int64) {
	seen := map[string]bool{}
	out := append([]events.Message(nil), messages...)
	changes := []Remint{}
	for index, message := range out {
		if message.ID == "" {
			continue
		}
		if !seen[message.ID] {
			seen[message.ID] = true
			continue
		}
		floor++
		to := fmt.Sprintf("m-%d", floor)
		changes = append(changes, Remint{Index: index, From: message.ID, To: to})
		out[index].ID, seen[to] = to, true
	}
	return out, changes, floor
}

// Settle is the operator-selected design-chat replacement outside compaction.
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
	messages, affected := s.MessagesCopy(), []string{}
	before := tokenSum(messages)
	for index, message := range messages {
		if !wanted[message.ID] || message.Elided || (message.Role != "user" && message.Role != "assistant") {
			continue
		}
		message.Content, message.ToolCalls, message.Reasoning, message.Elided = pointer, nil, "", true
		message.Tokens, message.Estimated = count(pointer)
		messages[index] = message
		if c.bus != nil {
			c.bus.Publish(events.New(events.MessageUpdated, s.ID, runID, map[string]any{"id": message.ID, "patch": map[string]any{"content": message.Content, "tokens": message.Tokens, "elided": true}}))
		}
		affected = append(affected, message.ID)
	}
	if len(affected) == 0 {
		return false
	}
	s.ReplaceMessages(messages)
	after := tokenSum(messages)
	s.RecordCompaction(after - before)
	if c.bus != nil {
		c.bus.Publish(events.New(events.Compaction, s.ID, runID, map[string]any{"kind": "settled", "trigger": "settle", "before": before, "after": after, "affected_ids": affected}))
	}
	return true
}

func elide(message events.Message, rawArgs string, readDefaultLimit int, count Counter) events.Message {
	stub := fmt.Sprintf("[elided: %s %s, %d tokens]", message.Name, keyArgs(message.Name, rawArgs, readDefaultLimit), message.Tokens)
	message.Content, message.Elided = stub, true
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

func clearReasoningForCall(messages []events.Message, callID string) {
	for index := range messages {
		for _, call := range messages[index].ToolCalls {
			if call.ID == callID {
				messages[index].Reasoning = ""
				return
			}
		}
	}
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
		offset, limit := number(args["offset"], 1), number(args["limit"], readDefaultLimit)
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
