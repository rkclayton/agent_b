package web

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"
)

// Item 2gy (v1.2.5, landed v1.2.6): A PROBE HAS THREE OUTCOMES.
//
// Yes, no, and INCONCLUSIVE - a timeout, a 503, a busy slot, no answer at all,
// or a reply that is neither a tool call nor a refusal. Only a clear no may
// downgrade a stored finding.
//
// HomePC's 08:06:27 findings are what this is for: `apply-template tools:
// available` written beside `tool calls: unavailable`, recorded while another
// agent held the GPU. A model that could not answer in time was written down as
// a model that cannot call tools, and that stored finding then stopped every
// run. The server had not changed; only its availability had.
//
// So an inconclusive probe keeps the previous finding, marks it `unverified
// since <time>`, and tries again on a backoff.

// probeBackoff is the retry ladder for an inconclusive probe: soon, then less
// soon, then give it a rest. A server that is busy now is often free in a
// minute, and hammering it is the one thing that cannot help.
var probeBackoff = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}

// probeOutcome is which of the three a probe reached.
type probeOutcome int

const (
	probeAnswered probeOutcome = iota
	probeRefused
	probeInconclusive
)

func (o probeOutcome) String() string {
	switch o {
	case probeAnswered:
		return "yes"
	case probeRefused:
		return "no"
	default:
		return "inconclusive"
	}
}

// inconclusiveMarkers are what "could not get an answer" looks like in the
// error text a probe returns. Each is a failure to REACH a conclusion, not a
// conclusion: the server never said what it can do.
var inconclusiveMarkers = []string{
	"timeout", "timed out", "deadline exceeded",
	"connection refused", "connection reset", "no such host", "unreachable",
	"eof", "broken pipe", "temporarily unavailable",
	"503", "502", "504", "429",
	"busy", "overloaded", "model is loading", "slot unavailable",
}

// classifyProbe reads a probe's error and says which outcome it is. No error at
// all is an answer; anything that names an unreachable or busy server is
// inconclusive; a server that answered and said no is a refusal.
func classifyProbe(err error) probeOutcome {
	if err == nil {
		return probeAnswered
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return probeInconclusive
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return probeInconclusive
	}
	text := strings.ToLower(err.Error())
	for _, marker := range inconclusiveMarkers {
		if strings.Contains(text, marker) {
			return probeInconclusive
		}
	}
	// The server answered with something this probe could read as a refusal.
	return probeRefused
}

// unverifiedFinding is the line an inconclusive probe leaves behind. The stored
// capabilities are untouched; this says only that nobody has confirmed them
// since a given moment, which is a different claim from "this server cannot".
func unverifiedFinding(at time.Time, reason string) string {
	return "unverified since " + at.UTC().Format(time.RFC3339) + ": " + reason
}

// keepFindingsUnverified returns the capabilities to store after an
// inconclusive probe: the previous ones, with the unverified line added and any
// earlier unverified line replaced, so the findings never accumulate one per
// failed attempt.
func keepFindingsUnverified(previous []string, at time.Time, reason string) []string {
	kept := make([]string, 0, len(previous)+1)
	for _, finding := range previous {
		if strings.HasPrefix(finding, "unverified since ") || strings.HasPrefix(finding, "probe failed: ") {
			continue
		}
		kept = append(kept, finding)
	}
	return append(kept, unverifiedFinding(at, reason))
}

// scheduleProbeRetry books the next attempt for a connection whose probe could not
// reach a conclusion, walking the backoff ladder and stopping at its end. A
// conclusive probe resets it.
func (s *Server) scheduleProbeRetry(connectionID string) {
	s.probeMu.Lock()
	if s.probeRetries == nil {
		s.probeRetries = map[string]int{}
	}
	attempt := s.probeRetries[connectionID]
	if attempt >= len(probeBackoff) {
		s.probeMu.Unlock()
		return
	}
	delay := probeBackoff[attempt]
	s.probeRetries[connectionID] = attempt + 1
	s.probeMu.Unlock()
	time.AfterFunc(delay, func() {
		snapshot := s.ConfigSnapshot()
		for i := range snapshot.Connections {
			if snapshot.Connections[i].ID == connectionID {
				s.startProbe(&snapshot.Connections[i])
				return
			}
		}
	})
}

// resetProbeRetries is called when a probe reaches a conclusion, so the next
// inconclusive run starts at the top of the ladder again.
func (s *Server) resetProbeRetries(connectionID string) {
	s.probeMu.Lock()
	delete(s.probeRetries, connectionID)
	s.probeMu.Unlock()
}
