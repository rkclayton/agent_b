//go:build windows

package updater

import (
	"testing"
	"time"
)

func TestTimestampedSignatureRemainsAcceptedAfterSignerExpiry2jw(t *testing.T) {
	// Windows reports a correctly timestamped signature as Valid even after the
	// signer certificate's NotAfter. The updater intentionally judges that
	// Authenticode result and timestamp evidence, not today's certificate date.
	if err := acceptAuthenticode(authenticodeEvidence{Status: "Valid", Signer: true, Timestamped: true, SignerNotAfter: time.Now().Add(-time.Hour), TimestampTime: time.Now().Add(-48 * time.Hour)}); err != nil {
		t.Fatalf("timestamped expired-signer fixture refused: %v", err)
	}
	if err := acceptAuthenticode(authenticodeEvidence{Status: "Valid", Signer: true}); err == nil {
		t.Fatal("an untimestamped signature was accepted")
	}
}
