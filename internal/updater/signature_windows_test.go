//go:build windows

package updater

import (
	"context"
	"os"
	"strings"
	"testing"
)

// Item 2lb: the script the updater runs must emit nothing of its own and must
// write its answer where only structured data can appear.
func TestInspectionScriptWritesAReportAndSilencesTheConsole2lb(t *testing.T) {
	script := authenticodeScript(`C:\dir\Agent_b-setup.exe`, `C:\temp\report.json`)
	for _, want := range []string{"$ProgressPreference='SilentlyContinue'", "$WarningPreference='SilentlyContinue'", "$VerbosePreference='SilentlyContinue'", "[IO.File]::WriteAllText($report", "unavailable="} {
		if !strings.Contains(script, want) {
			t.Fatalf("script is missing %q: %s", want, script)
		}
	}
}

// The real inspection, against this repository's own unsigned source file: a real
// child and a real report. The outer status is diagnostic and is not the payload gate.
func TestRealInspectionOfAnUnsignedFileIsAccepted2ox(t *testing.T) {
	err := verifySetupSignature(context.Background(), "signature.go")
	if err != nil {
		t.Fatalf("an unsigned outer signature was gated: %v", err)
	}
}

func TestRealHashMismatchIsRefused2ox(t *testing.T) {
	path := os.Getenv("AGENTB_HASH_MISMATCH_SETUP")
	if path == "" {
		t.Skip("set AGENTB_HASH_MISMATCH_SETUP to a tampered signed setup")
	}
	if err := verifySetupSignature(context.Background(), path); err == nil || !strings.Contains(err.Error(), "outer: HashMismatch") {
		t.Fatalf("tampered signed setup was not refused with its outer line: %v", err)
	}
}
