package llm

import (
	"fmt"
	"strings"
)

const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
	RoleHarness   = "harness"
)

const harnessNotePrefix = "[harness note]\n"

// BuildMessageList is the single normalization and validation boundary for a
// model-facing conversation. Durable journals may retain legacy system notes
// and the harness role; outbound requests never attribute either to the user.
func BuildMessageList(system string, history []Message) ([]Message, error) {
	out, _ := PrepareHistory(history)
	if strings.TrimSpace(system) != "" {
		out = append([]Message{{Role: RoleSystem, Content: system}}, out...)
	}
	if err := ValidateMessageList(out); err != nil {
		return nil, err
	}
	return out, nil
}

// PrepareHistory places notes before the latest user message, without changing
// journal records. The index map keeps token accounting aligned after the move;
// -1 identifies the existing no-user continuation message.
func PrepareHistory(history []Message) ([]Message, []int) {
	lastUser := -1
	for i, m := range history {
		if m.Role == RoleUser {
			lastUser = i
		}
	}
	originalLen := len(history)
	if lastUser < 0 {
		history = append(append([]Message(nil), history...), Message{Role: RoleUser, Content: harnessNotePrefix + "Continue from the recorded context."})
		lastUser = len(history) - 1
	}
	note := func(m Message) bool { return m.Role == RoleHarness || m.Role == RoleSystem }
	order := make([]int, 0, len(history))
	for i, m := range history {
		if i == lastUser {
			for j := lastUser + 1; j < len(history); j++ {
				if note(history[j]) {
					order = append(order, j)
				}
			}
		}
		if i > lastUser && note(m) {
			continue
		}
		order = append(order, i)
	}
	out := make([]Message, 0, len(order))
	for n, i := range order {
		m := history[i]
		if note(m) {
			m.Role = RoleAssistant
			m.Content = harnessNotePrefix + plainPriorRunNote(fmt.Sprint(m.Content))
		}
		out = append(out, m)
		if i == originalLen {
			order[n] = -1
		}
	}
	return out, order
}

func plainPriorRunNote(text string) string {
	switch {
	case strings.HasPrefix(text, "The prior run ended with length:"):
		return "The prior run was cut off for length."
	case strings.HasPrefix(text, "The prior run ended with aborted") || text == "The prior run ended because you stopped it.":
		return "The prior run was stopped by you."
	case strings.HasPrefix(text, "The prior run ended because the model server ") || strings.HasPrefix(text, "The prior run ended with model unreachable:"):
		return "The prior run ended because the server was unreachable."
	}
	return text
}

func ValidateMessageList(messages []Message) error {
	seenUser := false
	callIDs := map[string]bool{}
	for index, message := range messages {
		switch message.Role {
		case RoleSystem:
			if index != 0 {
				return fmt.Errorf("message-list rule system-first failed at message index %d", index)
			}
		case RoleUser:
			seenUser = true
		case RoleAssistant:
			for _, call := range message.ToolCalls {
				if call.ID != "" {
					callIDs[call.ID] = true
				}
			}
		case RoleTool:
			if message.ToolCallID == "" || !callIDs[message.ToolCallID] {
				return fmt.Errorf("message-list rule tool-result-after-call failed at message index %d", index)
			}
			delete(callIDs, message.ToolCallID)
		default:
			return fmt.Errorf("message-list rule known-role failed at message index %d: %q", index, message.Role)
		}
	}
	if !seenUser {
		return fmt.Errorf("message-list rule user-turn failed at message index %d: no user turn is present", len(messages))
	}
	return nil
}
