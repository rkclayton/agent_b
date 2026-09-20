package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Item 2gl (v1.2.0/W2). The marker is the answer to "should i re-run?", so
// these pin what makes its presence mean something.

func TestAnInstallMarkerSurvivesUntilAnInstallFinishes(t *testing.T) {
	root := t.TempDir()
	if _, found, err := readInstallMarker(root); err != nil || found {
		t.Fatalf("a fresh root has no marker: found=%v err=%v", found, err)
	}
	if err := writeInstallMarker(root, InstallMarker{Phase: "preflight", Version: "v1.2.0", Source: root}); err != nil {
		t.Fatal(err)
	}
	marker, found, err := readInstallMarker(root)
	if err != nil || !found {
		t.Fatalf("the marker was not read back: found=%v err=%v", found, err)
	}
	if marker.Phase != "preflight" || marker.Version != "v1.2.0" {
		t.Fatalf("marker lost its fields: %+v", marker)
	}
	if marker.StartedAt == "" || marker.UpdatedAt == "" || marker.PID == 0 {
		t.Fatalf("marker is missing when/who: %+v", marker)
	}
	// A later phase keeps the start time: the operator is told when the
	// install began, not when it last moved.
	started := marker.StartedAt
	marker.Phase = "copying the application"
	if err := writeInstallMarker(root, marker); err != nil {
		t.Fatal(err)
	}
	next, _, err := readInstallMarker(root)
	if err != nil {
		t.Fatal(err)
	}
	if next.StartedAt != started || next.Phase != "copying the application" {
		t.Fatalf("phase move rewrote the start: %+v", next)
	}
	// Only a finished install clears it.
	if err := clearInstallMarker(root); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := readInstallMarker(root); found {
		t.Fatal("the marker outlived the install that cleared it")
	}
	// Clearing again is not an error: an install that never wrote one still
	// finishes cleanly.
	if err := clearInstallMarker(root); err != nil {
		t.Fatalf("clearing an absent marker is not a failure: %v", err)
	}
}

func TestAnUnreadableMarkerIsReportedRatherThanIgnored(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, installMarkerName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, found, err := readInstallMarker(root)
	if !found || err == nil {
		t.Fatalf("something wrote that file; pretending it is absent is how an interrupted install becomes invisible: found=%v err=%v", found, err)
	}
}

func TestTheInterruptedInstallLineAnswersTheOperatorsQuestion(t *testing.T) {
	line := describeInterruptedInstall(InstallMarker{Phase: "copying the application", Version: "v1.2.0", StartedAt: "2026-09-21T08:00:00Z"})
	for _, want := range []string{"did not finish", "copying the application", "v1.2.0", "safe to repeat"} {
		if !strings.Contains(line, want) {
			t.Fatalf("the line must say %q: %s", want, line)
		}
	}
	// It must never be blank, whatever the marker holds.
	if bare := describeInterruptedInstall(InstallMarker{}); len(bare) < 40 {
		t.Fatalf("an empty marker still gets a sentence: %q", bare)
	}
}

// The installer's own parameters are passed through untouched, so the two
// vocabularies cannot drift.
func TestInstallArgumentsSplitByWhoOwnsThem(t *testing.T) {
	arguments := []string{
		"--install", "--quiet", "--install-data", `C:\data`,
		"-SourceDirectory", `C:\candidate`, "-TestMode",
		"-UninstallRegistryPath", `HKCU:\Software\X\Agent_b`,
	}
	mine := installFlagArgs(arguments)
	theirs := installPassthrough(arguments)
	for _, want := range []string{"--install", "--quiet", "--install-data", `C:\data`} {
		if !contains(mine, want) {
			t.Fatalf("this mode keeps %q: %v", want, mine)
		}
	}
	for _, want := range []string{"-SourceDirectory", `C:\candidate`, "-TestMode", "-UninstallRegistryPath", `HKCU:\Software\X\Agent_b`} {
		if !contains(theirs, want) {
			t.Fatalf("the installer keeps %q: %v", want, theirs)
		}
	}
	// Nothing this mode owns may reach the installer, and nothing of the
	// installer's may be eaten here.
	for _, unwanted := range []string{"--install", "--quiet", `C:\data`} {
		if contains(theirs, unwanted) {
			t.Fatalf("%q leaked into the installer's arguments: %v", unwanted, theirs)
		}
	}
	if contains(mine, "-TestMode") {
		t.Fatalf("-TestMode is the installer's: %v", mine)
	}
	// Order is preserved, because -Name value pairs depend on it.
	if len(theirs) != 5 || theirs[0] != "-SourceDirectory" || theirs[1] != `C:\candidate` {
		t.Fatalf("the installer's arguments lost their order: %v", theirs)
	}
}

func TestInstallPhasesComeFromTheInstallersOwnLines(t *testing.T) {
	for line, want := range map[string]string{
		"PREFLIGHT COMPLETE":            "preflight",
		"CANDIDATE: Agent_b.exe …":      "checking the candidate",
		"STOPPING Agent_b":              "stopping the running application",
		"INSTALLATION COMPLETE":         "finishing",
		"INSTALLATION FAILED: …":        "failed",
		"something the installer wrote": "",
	} {
		if got := phaseFor(line); got != want {
			t.Fatalf("phaseFor(%q) = %q, want %q", line, got, want)
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
