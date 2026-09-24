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
	out := make([]Message, 0, len(history)+1)
	if strings.TrimSpace(system) != "" {
		out = append(out, Message{Role: RoleSystem, Content: system})
	}
	pendingNotes := make([]Message, 0)
	seenUser := false
	for _, original := range history {
		message := original
		if message.Role == RoleHarness || message.Role == RoleSystem {
			message.Role = RoleAssistant
			message.Content = harnessNotePrefix + fmt.Sprint(message.Content)
			if !seenUser {
				pendingNotes = append(pendingNotes, message)
				continue
			}
		}
		if message.Role == RoleUser && !seenUser {
			seenUser = true
			out = append(out, message)
			out = append(out, pendingNotes...)
			pendingNotes = nil
			continue
		}
		out = append(out, message)
	}
	if !seenUser {
		out = append(out, Message{Role: RoleUser, Content: harnessNotePrefix + "Continue from the recorded context."})
		seenUser = true
		out = append(out, pendingNotes...)
		pendingNotes = nil
	}
	if len(pendingNotes) > 0 {
		out = append(out, pendingNotes...)
	}
	if err := ValidateMessageList(out); err != nil {
		return nil, err
	}
	return out, nil
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
