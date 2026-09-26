package agent

import (
	"errors"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/llm"
)

// Item 2l8. The operator's own refusal, verbatim from his screenshot, and the
// message-count refusal that appears 105 times in production chat s9 — the byte
// reader must take the first and leave the second to the message-limit reader.
func TestAByteRefusalIsReadFromWhatTheServerSaid2l8(t *testing.T) {
	err := errors.New(`chat stream HTTP 400: {"error": "prompt too large: 401628 bytes (limit 400000)"}`)
	limit, sentence, matched := byteLimitError(err)
	if !matched {
		t.Fatal("the operator's own refusal was not recognised as a size refusal")
	}
	if limit != 400000 {
		t.Fatalf("limit %d, want 400000", limit)
	}
	if !strings.Contains(sentence, "401628 bytes") {
		t.Fatalf("the stop sentence loses what the server said: %q", sentence)
	}
	if _, _, matched := byteLimitError(errors.New(`chat stream HTTP 400: {"error": "conversation too long: 61 messages (limit 60)"}`)); matched {
		t.Fatal("a message-count refusal was read as a byte refusal")
	}
	if _, _, matched := byteLimitError(errors.New("chat stream HTTP 500: server on fire")); matched {
		t.Fatal("a 500 was read as a size refusal")
	}
}

// Compaction leaves headroom rather than fitting to the byte, because the next
// turn adds a message of its own.
func TestTheByteTargetLeavesHeadroom2l8(t *testing.T) {
	if target := byteLimitTarget(400000); target != 380000 {
		t.Fatalf("target %d, want 380000", target)
	}
	if target := byteLimitTarget(400000); target >= 400000 {
		t.Fatal("the target does not leave headroom")
	}
	if target := byteLimitTarget(1); target != 1 {
		t.Fatalf("a tiny limit must stay usable, got %d", target)
	}
}

// The measurement itself: the serialized request in the bytes the server counts,
// which is the number the token budget cannot see.
func TestTheSerializedRequestIsMeasuredInBytes2l8(t *testing.T) {
	connection := &config.Connection{ID: "c", Model: "m"}
	small := llm.Request{Messages: []llm.Message{{Role: "user", Content: "hello"}}}
	large := llm.Request{Messages: []llm.Message{{Role: "user", Content: strings.Repeat("x", 100000)}}}
	smallBytes := llm.SerializedBytes(connection, small, true)
	largeBytes := llm.SerializedBytes(connection, large, true)
	if smallBytes <= 0 {
		t.Fatal("a request measured as zero bytes")
	}
	if largeBytes <= smallBytes+90000 {
		t.Fatalf("the measurement does not track the body: %d then %d", smallBytes, largeBytes)
	}
	// A body the token budget would call small is still over a 400,000-byte cap.
	if largeBytes < byteLimitTarget(100000) {
		t.Fatalf("measurement %d is implausible for a 100,000-character message", largeBytes)
	}
}
