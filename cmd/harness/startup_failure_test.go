package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Item 2x0: the failure window names the reason, the launcher log's last ten
// lines and this process's own last lines, and the tail of its own log is bounded.
func TestStartupFailureReportNamesReasonAndBothTails2x0(t *testing.T) {
	dataRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataRoot, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	var launcher strings.Builder
	for line := 1; line <= 30; line++ {
		fmt.Fprintf(&launcher, "launcher line %d\n", line)
	}
	if err := os.WriteFile(filepath.Join(dataRoot, "logs", "launcher-errors.log"), []byte(launcher.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	tail := &recentLines{limit: 40}
	for line := 1; line <= 100; line++ {
		fmt.Fprintf(tail, "own line %d\n", line)
	}
	report := startupFailureReport("config: broken", dataRoot, tail.String())
	for _, want := range []string{"Agent_b could not start.", "config: broken", "launcher line 21", "launcher line 30", "own line 61", "own line 100"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report lacks %q:\n%s", want, report)
		}
	}
	for _, unwanted := range []string{"launcher line 20\r", "own line 60\r"} {
		if strings.Contains(report, unwanted) {
			t.Fatalf("report carries %q beyond its bound", unwanted)
		}
	}
}
