package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Item 2nf (c): THE CONTROL SHOWS THE INSTALLER'S OWN REASON.
//
// The operator pressed Update three times. Each attempt died in under a second and the
// control said "The install stopped during starting (exit 1)", while the transcript
// beside it said exactly what was wrong. The reason was written down the whole time;
// nothing carried it to where he was looking.
func TestTheInstallFailureCarriesTheInstallersOwnReason(t *testing.T) {
	directory := t.TempDir()
	transcript := filepath.Join(directory, "installer-20260927-191144.log")
	// His transcript, in shape: the installer writes the reason twice, once as it
	// fails and once in the transcript's own tail.
	body := strings.Join([]string{
		"2026-09-27T19:11:44.825-05:00 install: starting; data root acme\\AppData\\Local\\Agent_b",
		"2026-09-27T19:11:45.127-05:00 install: verified and extracted the embedded application payload",
		"Transcript: " + transcript,
		"INSTALLATION FAILED: Application, operator-data, and workspace directories must be three disjoint trees.",
		"Transcript: " + transcript,
		"2026-09-27T19:11:45.927-05:00 install: the installer exited 1 during failed",
	}, "\r\n")
	if err := os.WriteFile(transcript, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	reason := installerFailureReason(transcript)
	if !strings.HasPrefix(reason, "INSTALLATION FAILED:") {
		t.Fatalf("the reason does not carry the installer's own line: %q", reason)
	}
	if !strings.Contains(reason, "three disjoint trees") {
		t.Errorf("the reason lost what was actually wrong: %q", reason)
	}
	// One line, not the whole transcript: this text goes in a control the width of a
	// settings sheet.
	if strings.Contains(reason, "\n") || strings.Contains(reason, "install: starting") {
		t.Errorf("the reason is more than the failure line: %q", reason)
	}
}

// The LAST failure wins, because a transcript is appended to and the final refusal is
// the one that ended the run.
func TestTheLastInstallFailureLineIsTheOneReported(t *testing.T) {
	transcript := filepath.Join(t.TempDir(), "installer.log")
	body := "INSTALLATION FAILED: an earlier attempt's reason.\nsomething else\nINSTALLATION FAILED: the reason that ended it.\n"
	if err := os.WriteFile(transcript, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if reason := installerFailureReason(transcript); !strings.Contains(reason, "ended it") {
		t.Errorf("reported %q", reason)
	}
}

// A transcript that is missing, unreadable or carries no failure line gives nothing,
// so the generic sentence stands. A missing log is not a reason.
func TestNoInstallFailureLineMeansNoReasonRatherThanAGuess(t *testing.T) {
	directory := t.TempDir()
	quiet := filepath.Join(directory, "quiet.log")
	if err := os.WriteFile(quiet, []byte("install: starting\nINSTALLATION COMPLETE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"a transcript with no failure line": quiet,
		"a transcript that is not there":    filepath.Join(directory, "absent.log"),
		"no transcript named at all":        "",
		"a blank name":                      "   ",
	} {
		if reason := installerFailureReason(path); reason != "" {
			t.Errorf("%s produced a reason: %q", name, reason)
		}
	}
	// And a bare marker with nothing after it is not a reason either.
	bare := filepath.Join(directory, "bare.log")
	if err := os.WriteFile(bare, []byte("INSTALLATION FAILED:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if reason := installerFailureReason(bare); reason != "" {
		t.Errorf("a marker with no text produced %q", reason)
	}
}
