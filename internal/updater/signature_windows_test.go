//go:build windows

package updater

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectionReturnsOnlyStructuredEvidence2yx(t *testing.T) {
	result := inspectAuthenticode(context.Background(), "signature.go")
	var evidence authenticodeEvidence
	if result.Err != nil || result.Output != "" || json.Unmarshal(result.Report, &evidence) != nil || evidence.Status != "NotSigned" {
		t.Fatalf("inspection: %+v", result)
	}
	if result := inspectAuthenticode(context.Background(), "missing-file.exe"); result.Err == nil {
		t.Fatal("missing file was not an inspection error")
	}
}

func TestNativeSignatureDetectsPayloadTampering2yx(t *testing.T) {
	path := os.Getenv("AGENTB_SIGNATURE_TEST_FILE")
	if path == "" {
		t.Skip("requires a signed disposable payload")
	}
	before, err := InspectSignature(path)
	if err != nil || before.Status != "Valid" || before.Thumbprint == "" {
		t.Fatalf("signed payload: %+v %v", before, err)
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	bytes[1024] ^= 1
	tampered := filepath.Join(t.TempDir(), "tampered.exe")
	if err := os.WriteFile(tampered, bytes, 0600); err != nil {
		t.Fatal(err)
	}
	after, err := InspectSignature(tampered)
	if err != nil || after.Status != "HashMismatch" || after.Thumbprint != before.Thumbprint {
		t.Fatalf("tampered payload: %+v %v", after, err)
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
