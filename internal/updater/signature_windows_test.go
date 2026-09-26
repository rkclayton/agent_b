//go:build windows

package updater

import (
	"context"
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
// child, a real report, and an answer that is a refusal with a reason rather than
// a JSON decode error.
func TestRealInspectionOfAnUnsignedFileRefusesWithAReason2lb(t *testing.T) {
	err := verifySetupSignature(context.Background(), "signature.go")
	if err == nil {
		t.Fatal("an unsigned source file was accepted")
	}
	if strings.Contains(err.Error(), "invalid character") {
		t.Fatalf("the combined-stream decode is still in the path: %v", err)
	}
	t.Logf("unsigned file refused with: %v", err)
}
