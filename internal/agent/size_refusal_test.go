package agent

import (
	"errors"
	"strings"
	"testing"
)

// Item 2lf. [[2l8]]'s recognisers required "HTTP 400" in the error text before
// their pattern was applied, so the same refusal under any other status taught the
// connection nothing. The message is the test now, not the status.
//
// The 400 text here is verbatim from the operator's failing chat s29, where the
// body GREW across three attempts because nothing compacted: 404632, 404677,
// 404716 bytes against a 400000 limit.
func TestASizeRefusalIsRecognisedUnderAnyStatus2lf(t *testing.T) {
	for _, status := range []string{"400", "500", "413", "422"} {
		err := errors.New(`chat stream HTTP ` + status + `: {"error": "prompt too large: 404716 bytes (limit 400000)"}`)
		limit, sentence, matched := byteLimitError(err)
		if !matched {
			t.Fatalf("HTTP %s carrying the size refusal was not recognised", status)
		}
		if limit != 400000 {
			t.Fatalf("HTTP %s: limit %d, want 400000", status, limit)
		}
		if !strings.Contains(sentence, "404716 bytes") {
			t.Fatalf("HTTP %s: the stop sentence loses what the server said: %q", status, sentence)
		}
	}
}

// (b): the pattern is still the whole test. An ordinary refusal is not turned into
// a size refusal merely because the status gate is gone — including the 409 real
// HTTP 500s in the operator's journals, which are chat-template execution errors.
func TestAnUnrelatedRefusalIsStillNotASizeRefusal2lf(t *testing.T) {
	for _, text := range []string{
		`chat stream HTTP 500: {"error":{"code":500,"message":"\n------------\nWhile executing CallExpression at"}}`,
		`chat stream HTTP 400: {"error": "System message must be at the beginning."}`,
		`chat stream HTTP 500: internal server error`,
		`chat stream HTTP 400: {"error": "model not found"}`,
	} {
		if _, _, matched := byteLimitError(errors.New(text)); matched {
			t.Fatalf("an unrelated refusal was read as a byte refusal: %s", text)
		}
		if _, _, matched := messageLimitError(errors.New(text)); matched {
			t.Fatalf("an unrelated refusal was read as a message refusal: %s", text)
		}
	}
}

// (c): the message-count currency, verbatim from chat s9 where it was refused and
// resent identically 107 times. It is recognised under any status, and it is NOT
// read as a byte refusal, because a server enforcing a message cap cannot be
// satisfied by a smaller body.
func TestAMessageCountRefusalIsItsOwnCurrency2lf(t *testing.T) {
	for _, status := range []string{"400", "500", "413"} {
		err := errors.New(`chat stream HTTP ` + status + `: {"error": "conversation too long: 61 messages (limit 60)"}`)
		limit, sentence, matched := messageLimitError(err)
		if !matched {
			t.Fatalf("HTTP %s carrying the message refusal was not recognised", status)
		}
		if limit != 60 {
			t.Fatalf("HTTP %s: limit %d, want 60", status, limit)
		}
		if !strings.Contains(sentence, "61 messages") {
			t.Fatalf("HTTP %s: the stop sentence loses what the server said: %q", status, sentence)
		}
		if _, _, matched := byteLimitError(err); matched {
			t.Fatalf("HTTP %s: a message-count refusal was also read as a byte refusal", status)
		}
	}
}
