package events

import (
	"encoding/json"
	"fmt"
	"strings"
)

// HistoryCall is compact manifest metadata. Arguments are sanitized when the
// immutable message event is indexed; result bodies remain only in JSONL.
type HistoryCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type HistoryEntry struct {
	Ref       string        `json:"ref"`
	Role      string        `json:"role"`
	Category  string        `json:"category"`
	Turn      int           `json:"turn"`
	Name      string        `json:"name,omitempty"`
	Call      *HistoryCall  `json:"call,omitempty"`
	Calls     []HistoryCall `json:"calls,omitempty"`
	OK        *bool         `json:"ok,omitempty"`
	Elided    bool          `json:"elided,omitempty"`
	Compacted bool          `json:"compacted,omitempty"`
}

type HistoryLookup struct {
	Kind    string
	Root    string
	Entries []HistoryEntry
	Message Message
	Found   bool
}

type historyLocation struct {
	path   string
	offset int64
	length int
}

type historyRecord struct {
	meta     Message
	location historyLocation
	full     *Message
}

type historyIndex struct {
	records   map[string]historyRecord
	order     []string
	lineage   map[string][]string
	latest    string
	elided    map[string]bool
	compacted map[string]bool
	calls     map[string]HistoryCall
}

func newHistoryIndex() *historyIndex {
	return &historyIndex{
		records:   map[string]historyRecord{},
		lineage:   map[string][]string{},
		elided:    map[string]bool{},
		compacted: map[string]bool{},
		calls:     map[string]HistoryCall{},
	}
}

func (h *historyIndex) append(message Message, location historyLocation, keepFull bool) {
	if message.ID == "" {
		return
	}
	meta := message
	meta.Content, meta.Reasoning = "", ""
	meta.ToolCalls = make([]ToolCall, 0, len(message.ToolCalls))
	for _, call := range message.ToolCalls {
		compact := HistoryCall{ID: call.ID, Name: call.Name, Arguments: sanitizeHistoryArguments(call.Arguments)}
		h.calls[call.ID] = compact
		meta.ToolCalls = append(meta.ToolCalls, ToolCall{ID: call.ID, Name: call.Name, Arguments: compact.Arguments})
	}
	record := historyRecord{meta: meta, location: location}
	if keepFull {
		full := message
		record.full = &full
	}
	if _, exists := h.records[message.ID]; !exists {
		h.order = append(h.order, message.ID)
	}
	h.records[message.ID] = record
}

func (h *historyIndex) update(id string, patch map[string]any) {
	if replayBool(patch["elided"]) {
		h.elided[id] = true
	}
}

func (h *historyIndex) compact(summaryID string, affected []string) {
	if summaryID == "" {
		return
	}
	h.latest = summaryID
	h.lineage[summaryID] = append([]string(nil), affected...)
	for _, id := range affected {
		h.compacted[id] = true
	}
}

func (h *historyIndex) recordReplay(event Event) {
	data := replayMap(event.Data)
	switch event.Type {
	case MessageAppended:
		var wrapper struct {
			Message Message `json:"message"`
		}
		if decodeReplay(event.Data, &wrapper) == nil {
			h.append(wrapper.Message, historyLocation{}, true)
		}
	case MessageUpdated:
		h.update(replayString(data["id"]), replayMap(data["patch"]))
	case Compaction:
		if replayString(data["kind"]) == "summarize" {
			h.compact(replayString(data["summary_message_id"]), replayStrings(data["affected_ids"]))
		}
	}
}

func (h *historyIndex) resolve(ref string, load func(historyRecord) (Message, error)) (HistoryLookup, error) {
	if ref == "latest" || h.lineage[ref] != nil {
		latest := ref == "latest"
		root := ref
		if latest {
			root = h.latest
		}
		selected := map[string]bool{}
		var walk func(string)
		walk = func(parent string) {
			for _, id := range h.lineage[parent] {
				if selected[id] {
					continue
				}
				selected[id] = true
				walk(id)
			}
		}
		if root != "" {
			walk(root)
		}
		if latest {
			for id := range h.compacted {
				selected[id] = true
			}
			for id := range h.elided {
				selected[id] = true
			}
		}
		entries := make([]HistoryEntry, 0, len(selected))
		for _, id := range h.order {
			if !selected[id] {
				continue
			}
			record, exists := h.records[id]
			if !exists {
				continue
			}
			entry := historyEntry(record.meta, h.calls)
			entry.Elided = h.elided[id]
			entry.Compacted = h.compacted[id]
			entries = append(entries, entry)
		}
		if len(entries) == 0 {
			return HistoryLookup{}, fmt.Errorf("no compacted history is available")
		}
		return HistoryLookup{Kind: "manifest", Root: root, Entries: entries, Found: true}, nil
	}
	record, exists := h.records[ref]
	if !exists || (!h.elided[ref] && !h.compacted[ref]) {
		return HistoryLookup{}, fmt.Errorf("history ref %q is not available", ref)
	}
	message, err := load(record)
	if err != nil {
		return HistoryLookup{}, err
	}
	return HistoryLookup{Kind: "message", Root: ref, Message: message, Found: true}, nil
}

func historyEntry(message Message, calls map[string]HistoryCall) HistoryEntry {
	entry := HistoryEntry{Ref: message.ID, Role: message.Role, Category: message.Category, Turn: message.Turn, Name: message.Name, OK: message.OK}
	for _, call := range message.ToolCalls {
		entry.Calls = append(entry.Calls, calls[call.ID])
	}
	if call, ok := calls[message.ToolCallID]; ok {
		copy := call
		entry.Call = &copy
	}
	return entry
}

func sanitizeHistoryArguments(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "{}"
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return `"unavailable"`
	}
	sanitizeHistoryValue(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return `"unavailable"`
	}
	return string(encoded)
}

func sanitizeHistoryValue(value any) {
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			if key == "content" || key == "new_string" || key == "note" || key == "old_string" {
				item[key] = "[omitted]"
				continue
			}
			if text, ok := child.(string); ok {
				runes := []rune(text)
				if len(runes) > 256 {
					item[key] = string(runes[:256]) + "…"
				}
				continue
			}
			sanitizeHistoryValue(child)
		}
	case []any:
		for _, child := range item {
			sanitizeHistoryValue(child)
		}
	}
}

func (h *historyIndex) resolveReplay(ref string) (HistoryLookup, error) {
	return h.resolve(ref, func(record historyRecord) (Message, error) {
		if record.full == nil {
			return Message{}, fmt.Errorf("recorded history body is unavailable")
		}
		return *record.full, nil
	})
}
