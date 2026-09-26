package updater

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Item 2lb. The updater refused a sound artifact because it decoded a child's
// combined stdout and stderr as JSON, and one console line made the decode fail.
// The decision now comes from the report the child writes, so console noise is
// irrelevant, and "I could not look" is a different answer from "the signature is
// bad".
func TestNoisyConsoleDoesNotSpoilAValidReport2lb(t *testing.T) {
	result := inspection{
		Report: []byte(`{"status":"Valid","signer":true,"timestamped":true}`),
		Output: "Get-AuthenticodeSignature : a warning nobody asked for\nWARNING: module autoload\n",
	}
	if err := readVerification(result); err != nil {
		t.Fatalf("a valid report was refused because the console said something: %v", err)
	}
}

func TestNoReportIsUnavailableNotInvalid2lb(t *testing.T) {
	result := inspection{Output: "Get-AuthenticodeSignature : The term is not recognized as the name of a cmdlet.\nsecond line\n"}
	err := readVerification(result)
	if !errors.Is(err, ErrVerificationUnavailable) {
		t.Fatalf("a missing report was not reported as unavailable: %v", err)
	}
	if !strings.Contains(err.Error(), "not recognized") {
		t.Fatalf("the child's own words were not preserved: %v", err)
	}
	if strings.Contains(err.Error(), "invalid character") {
		t.Fatalf("a decode error is still being surfaced: %v", err)
	}
}

func TestAnInspectionThatCouldNotRunSaysSo2lb(t *testing.T) {
	err := readVerification(inspection{Report: []byte(`{"unavailable":"Cannot find path 'C:\\missing.exe'"}`)})
	if !errors.Is(err, ErrVerificationUnavailable) {
		t.Fatalf("an unavailable report was not reported as unavailable: %v", err)
	}
	if !strings.Contains(err.Error(), "Cannot find path") {
		t.Fatalf("the reason was lost: %v", err)
	}
}

func TestAnUnsignedFileIsRefusedWithTheRealReason2lb(t *testing.T) {
	err := readVerification(inspection{Report: []byte(`{"status":"NotSigned","status_message":"The file is not digitally signed.","signer":false,"timestamped":false}`)})
	if err == nil {
		t.Fatal("an unsigned file was accepted")
	}
	if errors.Is(err, ErrVerificationUnavailable) {
		t.Fatalf("a bad signature was reported as an unavailable verification: %v", err)
	}
	for _, want := range []string{"NotSigned", "not digitally signed"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal does not say %q: %v", want, err)
		}
	}
}

func TestTimestampedSignatureStillAcceptedAfterSignerExpiry2lb(t *testing.T) {
	// Kept from 2jw: the standard this item must not change.
	if err := acceptAuthenticode(authenticodeEvidence{Status: "Valid", Signer: true, Timestamped: true, SignerNotAfter: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatalf("timestamped expired-signer fixture refused: %v", err)
	}
	if err := acceptAuthenticode(authenticodeEvidence{Status: "Valid", Signer: true}); err == nil {
		t.Fatal("an untimestamped signature was accepted")
	}
}
