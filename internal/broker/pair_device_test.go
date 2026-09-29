package broker

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"
)

// Item 2ns (c): the SYNTHETIC DEVICE, driven by nothing but the link the QR carried.
//
// This is the half of rel-1.37.0's acceptance a phone would play. It is given the link a
// decoder read off the screenshot, and from that link alone — no code typed, no key
// copied — it reaches PAIR_COMPLETE with the running Agent_b through the live broker:
//
//	$env:AGENTB_BROKER_LIVE = 'wss://<host>:<port>'
//	$env:AGENTB_PAIR_LINK = 'https://agentb.app/pair#c=...&k=...'
//	go test ./internal/broker -run TestLiveSyntheticDevice
//
// The proof is not that pairing works — rel-1.36.0 proved that. It is that the QR on the
// screen is SUFFICIENT: the code in the fragment opens the pairing, and the key in the
// fragment is the key the broker then names as the peer. If the link were wrong in either
// field this case fails, and it fails on the wire rather than in an assertion about a
// string.
func TestLiveSyntheticDevicePairsFromTheLink2ns(t *testing.T) {
	link := strings.TrimSpace(os.Getenv("AGENTB_PAIR_LINK"))
	if link == "" {
		t.Skip("SKIPPED: AGENTB_PAIR_LINK is not set. The browser pass reads it off the QR and sets it here.")
	}
	address := liveAddress(t)
	code, agentKey, err := ReadPairingLink(link)
	if err != nil {
		t.Fatalf("the link the QR carried does not read: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	device := newLiveIdentity(t)
	pairDevice := dialLive(t, ctx, address, "device", device)
	defer func() { _ = pairDevice.transport.Close(1000, "done") }()

	// The code from the fragment, dashes stripped, is the whole of what the device sends.
	pairDevice.send(t, FramePairBegin, struct {
		Role    string `json:"role"`
		Ed25519 string `json:"ed25519_public"`
		X25519  string `json:"x25519_public"`
		Code    string `json:"code"`
	}{
		Role: "device", Code: strings.ReplaceAll(code, "-", ""),
		Ed25519: base64.RawURLEncoding.EncodeToString(device.SigningPublic()),
		X25519:  base64.RawURLEncoding.EncodeToString(device.AgreementPublic()),
	})

	peerFrame := pairDevice.waitFor(t, FramePairPeer, 30*time.Second)
	var peer pairPeerPayload
	if err := DecodeInto(peerFrame.Payload, &peer); err != nil {
		t.Fatal(err)
	}
	// THE KEY IN THE LINK IS THE PEER THE BROKER NAMED. This is the check that makes the
	// QR worth trusting: a broker that substituted its own agent would be caught here,
	// because the phone knows the identity key before it ever connects.
	if got := mustDecode64(t, peer.Ed25519); string(got) != string(agentKey) {
		t.Fatalf("the broker named peer %s, the link carried %s",
			base64.RawURLEncoding.EncodeToString(got), base64.RawURLEncoding.EncodeToString(agentKey))
	}
	pairingID, err := hex.DecodeString(peer.PairingID)
	if err != nil {
		t.Fatal(err)
	}
	transcript := TranscriptHash(pairingID, agentKey, mustDecode64(t, peer.X25519), device.SigningPublic(), device.AgreementPublic())
	pairDevice.send(t, FramePairConfirm, pairConfirmPayload{
		PairingID: peer.PairingID, TranscriptHash: hex.EncodeToString(transcript),
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(device.SigningSeed), transcript)),
	})
	// The operator's side of the fingerprint step happens in the browser, so the wait is
	// long: the agent sends its own PAIR_CONFIRM when Settings' "They match" is pressed.
	pairDevice.waitFor(t, FramePairComplete, 90*time.Second)
	t.Logf("PAIRED FROM THE LINK: pairing %s, fingerprint %s", peer.PairingID,
		Fingerprint(agentKey, mustDecode64(t, peer.X25519), device.SigningPublic(), device.AgreementPublic()))
}
