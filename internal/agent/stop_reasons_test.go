package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"harness/internal/events"
)

// Item 2lw: every stop reason the code can emit is one the product names.
//
// rel-1.20.0/W0 found two that were not — `emergency` and `safe` — and found
// that neither has had a live emitter for some time. This is the test that
// stops the next one arriving unannounced.

// (a) and (b): the reasons this package can produce, read out of the source
// rather than from a list somebody keeps in step by hand. A copy would agree
// with itself forever, which is the same defect item 2jg's allow-list test
// exists to avoid.
func TestEveryReasonThisPackageCanEmitIsDeclared2lw(t *testing.T) {
	emitted := emittedStopReasons(t)
	if len(emitted) < 8 {
		t.Fatalf("only %d reasons read out of the source; the scan has stopped finding them: %v", len(emitted), emitted)
	}
	for _, reason := range emitted {
		if events.DeclaredStopReason(reason) {
			continue
		}
		if why, historical := events.HistoricalStopReasons[reason]; historical {
			t.Errorf("%q is emitted by live code but is recorded as HISTORICAL (%s) — it belongs in StopReasons, or the emitter is the mistake", reason, why)
			continue
		}
		t.Errorf("%q can be emitted and is not in events.StopReasons — declare it, or find what emits it and stop", reason)
	}
	t.Logf("read %d emittable reasons out of the source: %s", len(emitted), strings.Join(emitted, ", "))
}

// The negative control. Without it a scan that silently found nothing, or a
// membership test that said yes to everything, would pass forever.
func TestAnUndeclaredReasonIsCaught2lw(t *testing.T) {
	if events.DeclaredStopReason("a_reason_nobody_declared") {
		t.Fatal("an invented reason was reported as declared")
	}
	if events.KnownStopReason("a_reason_nobody_declared") {
		t.Fatal("an invented reason was reported as known")
	}
	// And the two the W0 sweep found ARE known, without being declared — which
	// is the distinction the item asks for.
	for _, historical := range []string{"emergency", "safe"} {
		if events.DeclaredStopReason(historical) {
			t.Errorf("%q is declared; it has no live emitter and should not be", historical)
		}
		if !events.KnownStopReason(historical) {
			t.Errorf("%q is not known, so a reader cannot tell it from a reason nobody has heard of", historical)
		}
	}
}

// (c): a declared reason with no words is the same defect as one with no
// declaration. Every declared reason must map to a terminal class of its own
// rather than falling through to "harness-error", which says a defect happened.
func TestEveryDeclaredReasonHasWordsOfItsOwn2lw(t *testing.T) {
	for _, reason := range events.StopReasons {
		got := canonicalTerminalReason(reason)
		if got == "" {
			t.Errorf("%q maps to nothing", reason)
			continue
		}
		if got == "harness-error" {
			t.Errorf("%q falls through to %q — a declared reason that the accounting calls a harness defect sends somebody looking for a bug that is not there", reason, got)
		}
	}
	// The default still exists and still says harness-error, for a reason that
	// genuinely is one: an undeclared reason reaching here IS a defect.
	if got := canonicalTerminalReason("a_reason_nobody_declared"); got != "harness-error" {
		t.Errorf("an undeclared reason mapped to %q, want harness-error", got)
	}
}

// (d): nothing that reads a journal is made stricter. A historical reason still
// loads and still renders — the word passes straight through, unchanged, and
// the accounting places it without refusing it.
func TestAHistoricalReasonStillLoadsAndRenders2lw(t *testing.T) {
	for _, historical := range []string{"emergency", "safe"} {
		if got := canonicalTerminalReason(historical); got == "" {
			t.Errorf("%q rendered as nothing", historical)
		}
		// It lands in harness-error, which is honest: a reason the product can no
		// longer emit appearing in a NEW run would be a defect. What matters for
		// (d) is that it maps at all and nothing refuses it.
	}
}

// emittedStopReasons reads this package's own source for the reasons its run
// loop returns. The run loop's reason-returning shape is `return "<reason>",`
// with a detail and a turn after it, which is narrow enough to find and wide
// enough to catch a new one.
func emittedStopReasons(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	// `return "wall_clock", fmt.Sprintf(...), turn` and `return "done", detail, turn`.
	returned := regexp.MustCompile(`return "([a-z][a-z0-9_]*)",\s*[^\n]*,\s*turn`)
	// abortReason's own returns: `return "aborted_mid_run"` on its own.
	bare := regexp.MustCompile(`^\s*return "(aborted_mid_[a-z]+)"\s*$`)
	seen := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		for _, match := range returned.FindAllStringSubmatch(text, -1) {
			seen[match[1]] = true
		}
		for _, line := range strings.Split(text, "\n") {
			if match := bare.FindStringSubmatch(line); match != nil {
				seen[match[1]] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for reason := range seen {
		out = append(out, reason)
	}
	// Stable order, so a failure names the same reason every run.
	for index := 1; index < len(out); index++ {
		for back := index; back > 0 && out[back] < out[back-1]; back-- {
			out[back], out[back-1] = out[back-1], out[back]
		}
	}
	return out
}
