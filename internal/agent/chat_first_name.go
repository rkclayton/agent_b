package agent

import (
	"context"
	"strings"
	"time"
	"unicode"

	"harness/internal/events"
	"harness/internal/llm"
	"harness/internal/session"
)

// Item 2go (v1.2.5): a chat is named ONCE, from the first message the operator
// sends, and the harness never changes it again.
//
// What this replaces: a name the aux model rewrote every twenty model turns.
// That meant a tab could be called one thing while you were reading it and
// something else when you came back, for no reason you had asked for. The
// operator asked for the opposite - "a tab is named for its chat, once" - so
// the name comes from the words HE wrote, the first time he wrote any.
//
// The rule, in his words: the first clause or the first six words, whichever is
// shorter, trimmed of punctuation. A clause ends at the first sentence mark or
// the first comma, semicolon, colon or dash that separates thoughts. Before the
// first message there is no name at all and the tab says so.

const firstNameMaxWords = 6

// firstMessageName derives the name a chat takes from its first operator
// message. It returns "" when the message carries no words at all, which leaves
// the chat unnamed rather than naming it something meaningless.
func firstMessageName(text string) string {
	clause := firstClause(text)
	words := strings.Fields(clause)
	if len(words) > firstNameMaxWords {
		words = words[:firstNameMaxWords]
	}
	for index, word := range words {
		words[index] = strings.TrimFunc(word, func(r rune) bool {
			return unicode.IsPunct(r) || unicode.IsSymbol(r)
		})
	}
	kept := words[:0]
	for _, word := range words {
		if word != "" {
			kept = append(kept, word)
		}
	}
	name := strings.Join(kept, " ")
	if len([]rune(name)) > 80 {
		name = strings.TrimSpace(string([]rune(name)[:80]))
	}
	return name
}

// firstClause cuts at the first mark that ends a thought. A decimal point or a
// mark inside a word - "v1.2.5", "fix the bug in user.go" - is not one of them,
// so a clause is cut only where the mark is followed by space or by nothing.
func firstClause(text string) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	for index, r := range runes {
		if !strings.ContainsRune(".!?,;:", r) && r != '—' && r != '–' {
			continue
		}
		if index == 0 {
			continue
		}
		if index+1 < len(runes) && !unicode.IsSpace(runes[index+1]) {
			continue
		}
		return string(runes[:index])
	}
	// A line break ends a thought as surely as a full stop.
	if cut := strings.IndexAny(text, "\r\n"); cut >= 0 {
		return text[:cut]
	}
	return text
}

// nameFromFirstMessage gives a chat its name when it has none and the operator
// has just spoken for the first time. A pinned name - one the operator typed
// himself - is never touched, and neither is a name already given.
func (r *Runner) nameFromFirstMessage(item *session.Session, text string) {
	if r.renameSession == nil {
		return
	}
	snapshot := item.Snapshot()
	if snapshot.NamePinned || strings.TrimSpace(snapshot.Label) != "" {
		return
	}
	name := firstMessageName(text)
	if name == "" {
		return
	}
	_ = r.renameSession(item.ID, name, "c")
}

// nameAfterFirstRun replaces the temporary mechanical name once, after the
// first ordinary chat run closes. It is deliberately not a run: no tools,
// memory, journal messages, retry, or worker session is involved.
func (r *Runner) nameAfterFirstRun(item *session.Session, runID string) {
	snapshot := item.Snapshot()
	if snapshot.Role != "b" || snapshot.NamePinned || r.renameSession == nil {
		return
	}
	var firstUser, firstReply string
	for _, message := range item.MessagesCopy() {
		switch message.Role {
		case "user":
			if firstUser == "" {
				firstUser = message.Content
			}
		case "assistant":
			if firstReply == "" && strings.TrimSpace(message.Content) != "" {
				firstReply = message.Content
			}
		}
	}
	if firstUser == "" || firstReply == "" {
		return
	}
	if _, loaded := r.nameAttempts.LoadOrStore(item.ID, true); loaded {
		return
	}
	connection, ok := r.connection(snapshot.ConnectionID)
	if !ok {
		r.publishNameAttempt(item.ID, runID, "failed", "connection not found", "")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := llm.New(connection).Chat(ctx, llm.Request{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: "Name this chat in five words or fewer. Return plain text only."},
		{Role: "user", Content: "First message:\n" + firstUser + "\n\nFirst reply:\n" + firstReply},
	}, MaxTokens: 24})
	if err != nil {
		r.publishNameAttempt(item.ID, runID, "failed", err.Error(), "")
		return
	}
	name := modelChatName(response.Content)
	if name == "" {
		r.publishNameAttempt(item.ID, runID, "failed", "empty or unusable response", "")
		return
	}
	if item.Snapshot().NamePinned {
		r.publishNameAttempt(item.ID, runID, "skipped", "operator renamed the chat", "")
		return
	}
	if err := r.renameSession(item.ID, name, "c"); err != nil {
		r.publishNameAttempt(item.ID, runID, "failed", err.Error(), "")
		return
	}
	r.publishNameAttempt(item.ID, runID, "named", "", name)
}

func (r *Runner) publishNameAttempt(sessionID, runID, outcome, reason, name string) {
	r.bus.Publish(events.New(events.ChatNamed, sessionID, runID, map[string]any{"outcome": outcome, "reason": reason, "name": name}))
}

func modelChatName(value string) string {
	value = strings.TrimSpace(strings.Trim(value, "`\"'"))
	if strings.EqualFold(value, "null") {
		return ""
	}
	if strings.ContainsAny(value, "\r\n") {
		value = strings.TrimSpace(strings.Split(strings.ReplaceAll(value, "\r", "\n"), "\n")[0])
	}
	words := strings.Fields(value)
	if len(words) == 0 {
		return ""
	}
	if len(words) > 5 {
		words = words[:5]
	}
	value = strings.Join(words, " ")
	if len([]rune(value)) > 80 {
		value = strings.TrimSpace(string([]rune(value)[:80]))
	}
	return value
}
