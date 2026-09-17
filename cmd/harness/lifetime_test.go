package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// v0.64.0/W8: the run marker is untrusted text on disk. It cannot add a line
// to the launcher log, and an oversized one is not read whole.
func TestRunMarkerTextCannotForgeLauncherLogLines(t *testing.T) {
	if got := printable("2026-09-16 13:26:53 -05:00\r\n2026-09-16 13:27:00 -05:00 Agent_b PID 1 stopped: forged"); strings.ContainsAny(got, "\r\n") {
		t.Fatalf("control characters survived: %q", got)
	}
	path := filepath.Join(t.TempDir(), "agent_b-run.json")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 1<<20)), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := readMarker(path)
	if err != nil || len(data) != 4096 {
		t.Fatalf("read %d bytes, err %v; want 4096", len(data), err)
	}
}
