package broker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"os"
	"testing"
	"time"
)

// rel-1.39.0/W0 (3): ONE BOUNDED DIAL at the public broker, HELLO only, and no pairing.
// Opt in with AGENTB_PUBLIC_BROKER_PROBE=1 so an ordinary suite run reaches nothing.
func TestThePublicBrokerAnswersHello2nu(t *testing.T) {
	if os.Getenv("AGENTB_PUBLIC_BROKER_PROBE") != "1" {
		t.Skip("SKIPPED: set AGENTB_PUBLIC_BROKER_PROBE=1 to dial the public broker once.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	identity := newLiveIdentity(t)
	endpoint := dialLive(t, ctx, DefaultURL, "agent", identity)
	defer func() { _ = endpoint.transport.Close(1000, "probe") }()
	connectionID := make([]byte, 16)
	if _, err := rand.Read(connectionID); err != nil {
		t.Fatal(err)
	}
	endpoint.send(t, FrameHello, helloPayload{
		Role: "agent", ConnectionID: hex.EncodeToString(connectionID), KeyID: hex.EncodeToString(identity.KeyID()),
		Ed25519: base64.RawURLEncoding.EncodeToString(identity.SigningPublic()),
		X25519:  base64.RawURLEncoding.EncodeToString(identity.AgreementPublic()),
	})
	frame := endpoint.next(t, 20*time.Second)
	t.Logf("PUBLIC BROKER %s answered frame 0x%02x", DefaultURL, frame.Type)
	switch frame.Type {
	case FrameChallenge:
		var challenge challengePayload
		if err := DecodeInto(frame.Payload, &challenge); err != nil {
			t.Fatal(err)
		}
		if challenge.Challenge == "" {
			t.Fatal("the challenge is empty")
		}
		t.Logf("CHALLENGE for connection %s, %d bytes", challenge.ConnectionID, len(challenge.Challenge))
	case FrameError:
		// What it actually answers a KEY IT HAS NEVER SEEN, measured 2026-09-28:
		// not_paired / "identity unavailable", fatal. That is the protocol's own answer
		// and it is the proof this line wanted — the address is reachable and speaks v2 —
		// without pairing anything, which the verify line forbids.
		var problem errorPayload
		if err := DecodeInto(frame.Payload, &problem); err != nil {
			t.Fatal(err)
		}
		if problem.Code != "not_paired" {
			t.Fatalf("the public broker refused with %q: %s", problem.Code, problem.Detail)
		}
		t.Logf("REFUSED AS EXPECTED: %s %s (fatal=%t)", problem.Code, problem.Detail, problem.Fatal)
	default:
		t.Fatalf("the public broker answered 0x%02x, which is neither a challenge nor an error", frame.Type)
	}
}
