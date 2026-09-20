package agent

import (
	"strings"
	"unicode"

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
