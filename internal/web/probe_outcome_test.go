package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// Item 2gy (v1.2.5, landed v1.2.6): a probe has three outcomes, and only a
// clear NO may downgrade a stored finding.
func TestProbeClassifiesItsThreeOutcomes(t *testing.T) {
	for _, item := range []struct {
		name string
		err  error
		want probeOutcome
	}{
		{"an answer", nil, probeAnswered},
		{"a deadline", context.DeadlineExceeded, probeInconclusive},
		{"a cancellation", context.Canceled, probeInconclusive},
		{"a network error", &net.OpError{Op: "dial", Err: errors.New("no route")}, probeInconclusive},
		{"a timeout in the text", errors.New("post /v1/chat: context deadline exceeded"), probeInconclusive},
		{"a busy server", errors.New("HTTP 503 Service Unavailable"), probeInconclusive},
		{"too many requests", errors.New("HTTP 429 slow down"), probeInconclusive},
		{"a loading model", errors.New("model is loading, try again"), probeInconclusive},
		{"a refused connection", errors.New("dial tcp 127.0.0.1:1: connection refused"), probeInconclusive},
		// The server answered and said no. That is a conclusion.
		{"a plain refusal", errors.New("this server does not support tool calls"), probeRefused},
		{"an unsupported field", errors.New("400 unknown field tools"), probeRefused},
	} {
		if got := classifyProbe(item.err); got != item.want {
			t.Fatalf("%s: got %s, want %s", item.name, got, item.want)
		}
	}
}

// An inconclusive probe keeps what was known and says since when nobody has
// confirmed it. It never writes "cannot" for a server that merely did not answer.
func TestInconclusiveProbeKeepsThePreviousFinding(t *testing.T) {
	previous := []string{"tool calls: available", "apply-template tools: available", "probe failed: an older attempt"}
	at := time.Date(2026, 9, 22, 8, 6, 27, 0, time.UTC)
	kept := keepFindingsUnverified(previous, at, "context deadline exceeded")

	joined := strings.Join(kept, " | ")
	if !strings.Contains(joined, "tool calls: available") {
		t.Fatalf("the stored finding was lost: %v", kept)
	}
	if strings.Contains(joined, "probe failed:") {
		t.Fatalf("an older failure line survived: %v", kept)
	}
	if want := "unverified since 2026-09-22T08:06:27Z: context deadline exceeded"; kept[len(kept)-1] != want {
		t.Fatalf("last finding=%q, want %q", kept[len(kept)-1], want)
	}
	// A second inconclusive probe replaces the line rather than stacking them.
	again := keepFindingsUnverified(kept, at.Add(time.Minute), "still busy")
	count := 0
	for _, finding := range again {
		if strings.HasPrefix(finding, "unverified since ") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("unverified lines stacked: %v", again)
	}
}

// The backoff is the ladder the item names, and it stops at its end.
func TestProbeBackoffLadder(t *testing.T) {
	want := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}
	if fmt.Sprint(probeBackoff) != fmt.Sprint(want) {
		t.Fatalf("backoff=%v, want %v", probeBackoff, want)
	}
	server := &Server{}
	for attempt := 0; attempt < len(want)+2; attempt++ {
		server.scheduleProbeRetry("p1")
	}
	server.probeMu.Lock()
	booked := server.probeRetries["p1"]
	server.probeMu.Unlock()
	if booked != len(want) {
		t.Fatalf("the ladder booked %d attempts, want %d", booked, len(want))
	}
	server.resetProbeRetries("p1")
	server.probeMu.Lock()
	after := server.probeRetries["p1"]
	server.probeMu.Unlock()
	if after != 0 {
		t.Fatalf("a conclusive probe must reset the ladder, got %d", after)
	}
}
