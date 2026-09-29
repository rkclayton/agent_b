package broker

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// Item 2kq (b): PAIRING FROM SETTINGS, and the operator's part in it is the fingerprint.

func TestThePairingCodeIsFortyCrockfordDigits2kq(t *testing.T) {
	code, raw, err := NewPairingCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 20 {
		t.Fatalf("the code carries %d bytes, want 20", len(raw))
	}
	groups := strings.Split(code, "-")
	if len(groups) != 8 {
		t.Fatalf("the code reads as %d groups, want 8: %q", len(groups), code)
	}
	for _, group := range groups {
		if len(group) != 5 {
			t.Fatalf("group %q is %d characters, want 5", group, len(group))
		}
	}
	decoded, err := DecodePairingCode(code)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(decoded) != hex.EncodeToString(raw) {
		t.Fatalf("the code did not round trip: %x then %x", raw, decoded)
	}
	// It reads the way the operator will type it: hyphens optional, case ignored.
	loose, err := DecodePairingCode(strings.ToLower(strings.ReplaceAll(code, "-", "")))
	if err != nil || hex.EncodeToString(loose) != hex.EncodeToString(raw) {
		t.Fatalf("a hyphen-free lower-case code did not decode: %v", err)
	}
	// And the refusals the document states: a wrong length, and a digit outside the
	// sixteen a v1 code may use.
	if _, err := DecodePairingCode(code + "0"); err == nil {
		t.Error("a 41-digit code was accepted")
	}
	if _, err := DecodePairingCode(strings.Repeat("Z", 40)); err == nil {
		t.Error("a code of index-31 digits was accepted")
	}
}

func TestTheFingerprintIsTenGroupsOfFour2kq(t *testing.T) {
	agent, device, _ := testPair(t)
	fingerprint := Fingerprint(agent.SigningPublic(), agent.AgreementPublic(), device.SigningPublic(), device.AgreementPublic())
	groups := strings.Split(fingerprint, " ")
	if len(groups) != 10 {
		t.Fatalf("the fingerprint reads as %d groups, want 10: %q", len(groups), fingerprint)
	}
	for _, group := range groups {
		if len(group) != 4 || strings.ToUpper(group) != group {
			t.Fatalf("group %q is not four upper-case hex characters", group)
		}
	}
	// The order matters: the agent endpoint's two keys come first, so swapping the two
	// endpoints is a different fingerprint and the operator would see it.
	swapped := Fingerprint(device.SigningPublic(), device.AgreementPublic(), agent.SigningPublic(), agent.AgreementPublic())
	if swapped == fingerprint {
		t.Fatal("the fingerprint does not depend on which endpoint is which")
	}
}

// The whole flow against a scripted broker, including the operator's confirmation and
// the refusal when he says the screens do not match.
func TestPairingCompletesAndTheOperatorCanRefuse2kq(t *testing.T) {
	agent, device, _ := testPair(t)
	pairingID := make([]byte, 16)
	if _, err := rand.Read(pairingID); err != nil {
		t.Fatal(err)
	}
	code, raw, err := NewPairingCode()
	if err != nil {
		t.Fatal(err)
	}

	for _, confirmed := range []bool{true, false} {
		name := "the operator confirms"
		if !confirmed {
			name = "the operator refuses"
		}
		t.Run(name, func(t *testing.T) {
			transport := newScriptedBroker(t, agent, device, Pairing{PairingID: pairingID})
			frames := make(chan Frame, 8)
			receive := func() (Frame, error) { return <-frames, nil }
			go func() {
				// The broker's side: waiting, then the peer, then complete.
				begin := transport.next(t)
				if begin.Type != FramePairBegin {
					t.Errorf("first frame is 0x%02x, want PAIR_BEGIN", begin.Type)
					return
				}
				var payload pairBeginPayload
				if err := DecodeInto(begin.Payload, &payload); err != nil {
					t.Error(err)
					return
				}
				// The agent sends the HASH of the code and never the code.
				if payload.CodeHash != hex.EncodeToString(CodeHash(raw)) {
					t.Errorf("the agent sent code hash %s", payload.CodeHash)
				}
				if strings.Contains(string(begin.Payload), strings.ReplaceAll(code, "-", "")) {
					t.Error("the agent sent the pairing code itself")
				}
				frames <- Frame{Type: FramePairWaiting, Payload: mustEncodePayload(t, pairWaitingPayload{ExpiresAt: "2026-09-29T00:10:00Z"})}
				frames <- Frame{Type: FramePairPeer, Payload: mustEncodePayload(t, pairPeerPayload{
					PairingID: hex.EncodeToString(pairingID),
					PeerKeyID: hex.EncodeToString(device.KeyID()),
					Ed25519:   base64.RawURLEncoding.EncodeToString(device.SigningPublic()),
					X25519:    base64.RawURLEncoding.EncodeToString(device.AgreementPublic()),
				})}
				if !confirmed {
					return
				}
				confirm := transport.next(t)
				if confirm.Type != FramePairConfirm {
					t.Errorf("expected PAIR_CONFIRM, got 0x%02x", confirm.Type)
					return
				}
				var confirmation pairConfirmPayload
				if err := DecodeInto(confirm.Payload, &confirmation); err != nil {
					t.Error(err)
					return
				}
				transcript := TranscriptHash(pairingID, agent.SigningPublic(), agent.AgreementPublic(), device.SigningPublic(), device.AgreementPublic())
				if confirmation.TranscriptHash != hex.EncodeToString(transcript) {
					t.Error("the transcript hash does not match")
					return
				}
				signature, err := base64.RawURLEncoding.DecodeString(confirmation.Signature)
				if err != nil {
					t.Error(err)
					return
				}
				if !ed25519.Verify(agent.SigningPublic(), transcript, signature) {
					t.Error("the confirmation signature does not verify")
					return
				}
				frames <- Frame{Type: FramePairComplete, Payload: mustEncodePayload(t, pairCompletePayload{PairingID: hex.EncodeToString(pairingID)})}
			}()

			var shown PairingOffer
			pairing, err := BeginPairing(transport, agent, code, receive, func(offer PairingOffer) bool {
				shown = offer
				return confirmed
			})
			if !confirmed {
				if err == nil {
					t.Fatal("a pairing the operator refused completed anyway")
				}
				return
			}
			if err != nil {
				t.Fatalf("pairing: %v", err)
			}
			if hex.EncodeToString(pairing.PairingID) != hex.EncodeToString(pairingID) {
				t.Fatalf("pairing id = %x", pairing.PairingID)
			}
			// What the operator was shown: the code he typed and the fingerprint to
			// compare, ten groups of four.
			if shown.Code != code {
				t.Errorf("the offer showed %q", shown.Code)
			}
			if want := Fingerprint(agent.SigningPublic(), agent.AgreementPublic(), device.SigningPublic(), device.AgreementPublic()); shown.Fingerprint != want {
				t.Errorf("the fingerprint shown was %q", shown.Fingerprint)
			}
		})
	}
}

// A broker that names a key id which does not belong to the keys it sent is substituting
// an endpoint, and the pairing refuses rather than trusting the label.
func TestAPeerKeyIDThatDoesNotMatchItsKeysIsRefused2kq(t *testing.T) {
	agent, device, _ := testPair(t)
	pairingID := make([]byte, 16)
	if _, err := rand.Read(pairingID); err != nil {
		t.Fatal(err)
	}
	code, _, err := NewPairingCode()
	if err != nil {
		t.Fatal(err)
	}
	transport := newScriptedBroker(t, agent, device, Pairing{PairingID: pairingID})
	frames := make(chan Frame, 4)
	go func() {
		_ = transport.next(t)
		wrong := append([]byte(nil), agent.KeyID()...)
		frames <- Frame{Type: FramePairPeer, Payload: mustEncodePayload(t, pairPeerPayload{
			PairingID: hex.EncodeToString(pairingID),
			PeerKeyID: hex.EncodeToString(wrong),
			Ed25519:   base64.RawURLEncoding.EncodeToString(device.SigningPublic()),
			X25519:    base64.RawURLEncoding.EncodeToString(device.AgreementPublic()),
		})}
	}()
	if _, err := BeginPairing(transport, agent, code, func() (Frame, error) { return <-frames, nil }, func(PairingOffer) bool { return true }); err == nil {
		t.Fatal("a peer whose key id does not match its keys was accepted")
	}
}

func mustEncodePayload(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := Encode(0x00, value)
	if err != nil {
		t.Fatal(err)
	}
	return raw[6:]
}
