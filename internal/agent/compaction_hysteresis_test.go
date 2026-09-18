package agent

import (
	"context"
	"testing"

	"harness/internal/config"
)

// Item 2ey reverses the earlier high-watermark rule: the batch elide fires at
// soft_pct, one batch toward 60%, and is still suppressed right after a cold
// prefill so the cache is not invalidated turn after turn.
func TestCompactionElidesAtSoftLineAndSkipsColdPrefill(t *testing.T) {
	cfg := config.GlobalContext{SoftPct: .75, SummaryPct: .85}
	if shouldBatchElide(7400, 10000, cfg, false) {
		t.Fatal("elided below the soft line")
	}
	if !shouldBatchElide(7500, 10000, cfg, false) {
		t.Fatal("did not elide at the soft line")
	}
	if shouldBatchElide(9000, 10000, cfg, true) {
		t.Fatal("elided immediately after cold prefill")
	}
}

func TestCompactionTriggerTravelsOnTheContext(t *testing.T) {
	if got := compactionTrigger(context.Background()); got != "" {
		t.Fatalf("unset trigger = %q", got)
	}
	if got := compactionTrigger(withCompactionTrigger(context.Background(), "summary_pct")); got != "summary_pct" {
		t.Fatalf("trigger = %q", got)
	}
}
