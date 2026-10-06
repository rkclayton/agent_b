package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// Item 2qq CHECKS 2 and 4: every observed exit has one closed cause, and the
// next start can report that cause without carrying any operator content.
func TestEveryExitCauseIsOneLineAndSurvivesForTheNextStart2qq(t *testing.T) {
	for _, cause := range []string{"user", "installer", "session_end", "crash", "killed", "unknown"} {
		t.Run(cause, func(t *testing.T) {
			root := t.TempDir()
			clock := time.Date(2026, 10, 6, 9, 0, 0, 0, time.FixedZone("CDT", -5*60*60))
			life := newLifetime(root, func() time.Time { return clock })
			life.begin()
			life.stoppedCause(cause, "first line\nsecret second line")
			life.stoppedCause("unknown", "duplicate")
			body, err := os.ReadFile(life.logPath)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(body)), "\n")
			if len(lines) != 1 || !strings.Contains(lines[0], "cause="+cause) || strings.Contains(lines[0], "secret second line") {
				t.Fatalf("%s launcher line = %q", cause, body)
			}
			var marker runMarker
			if data, err := os.ReadFile(life.markerPath); err != nil || json.Unmarshal(data, &marker) != nil || marker.ExitCause != cause || marker.UptimeS != 0 {
				t.Fatalf("%s marker = %+v, err=%v", cause, marker, err)
			}
		})
	}
}

func TestHardKillIsUnrecordedAtTheNextStart2qq(t *testing.T) {
	root := t.TempDir()
	dead := &lifetime{logPath: filepath.Join(root, "logs", launcherLogName), markerPath: filepath.Join(root, "agent_b-run.json"), pid: 999999, created: 1, now: time.Now}
	marker, _ := json.Marshal(runMarker{PID: dead.pid, Created: dead.created, Started: "2026-10-06 03:00:00 -05:00"})
	if err := os.WriteFile(dead.markerPath, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	running := newLifetime(root, time.Now)
	running.begin()
	if running.previousExit != "unrecorded" {
		t.Fatalf("previous exit = %q, want unrecorded", running.previousExit)
	}
	body, _ := os.ReadFile(running.logPath)
	if strings.Count(string(body), "cause=killed") != 1 {
		t.Fatalf("launcher log = %q", body)
	}
}
