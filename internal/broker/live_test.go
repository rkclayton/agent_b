package broker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"
)

// Item 2kq's acceptance, against the LIVE broker: pair, authenticate, establish a peer
// session, round-trip a message, reconnect as a new session, revoke, and leave nothing
// paired.
//
// It is opt-in because it reaches a machine that is not this one:
//
//	$env:AGENTB_BROKER_LIVE = 'wss://<host>:<port>'; go test ./internal/broker -run Live
//
// Without it the case says what it would have done and why it did not, rather than
// passing silently and implying a proof nobody performed. The device side is a
// SYNTHETIC one played here with this package's own primitives — which is what the
// acceptance allows when the phone is not in the room — so what this proves is the WIRE
// against the real broker. The client's own behaviour is proved by the scripted cases.

func liveAddress(t *testing.T) string {
	t.Helper()
	address := strings.TrimSpace(os.Getenv("AGENTB_BROKER_LIVE"))
	if address == "" {
		t.Skip("SKIPPED live broker case: AGENTB_BROKER_LIVE is not set. It dials a broker on another machine and pairs a synthetic device with it; set the address to run it.")
	}
	return address
}

type liveEndpoint struct {
	role      string
	identity  Identity
	transport Transport
	frames    chan Frame
	errs      chan error
}

func dialLive(t *testing.T, ctx context.Context, address, role string, identity Identity) *liveEndpoint {
	t.Helper()
	transport, err := Dial(address)(ctx)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	endpoint := &liveEndpoint{role: role, identity: identity, transport: transport, frames: make(chan Frame, 32), errs: make(chan error, 1)}
	go func() {
		for {
			raw, err := transport.Receive(ctx)
			if err != nil {
				endpoint.errs <- err
				return
			}
			frame, err := Decode(raw)
			if err != nil {
				endpoint.errs <- err
				return
			}
			endpoint.frames <- frame
		}
	}()
	return endpoint
}

func (e *liveEndpoint) send(t *testing.T, frameType byte, payload any) {
	t.Helper()
	frame, err := Encode(frameType, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.transport.Send(frame); err != nil {
		t.Fatal(err)
	}
}

func (e *liveEndpoint) next(t *testing.T, timeout time.Duration) Frame {
	t.Helper()
	select {
	case frame := <-e.frames:
		if frame.Type == FrameError {
			var problem errorPayload
			_ = DecodeInto(frame.Payload, &problem)
			t.Logf("%s got broker ERROR: %s %s (fatal=%t)", e.role, problem.Code, problem.Detail, problem.Fatal)
		}
		return frame
	case err := <-e.errs:
		t.Fatalf("%s connection ended: %v", e.role, err)
		return Frame{}
	case <-time.After(timeout):
		t.Fatalf("%s: the broker sent nothing", e.role)
		return Frame{}
	}
}

// waitFor skips frames until the one the scenario is looking for, which is what an
// endpoint does: a PING or a QUEUED arriving mid-flow is not a failure.
func (e *liveEndpoint) waitFor(t *testing.T, frameType byte, timeout time.Duration) Frame {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		frame := e.next(t, time.Until(deadline))
		if frame.Type == frameType {
			return frame
		}
		t.Logf("%s skipped frame 0x%02x while waiting for 0x%02x", e.role, frame.Type, frameType)
	}
	t.Fatalf("%s never received frame 0x%02x", e.role, frameType)
	return Frame{}
}

// authenticate is the normal connection handshake every endpoint uses after pairing.
func (e *liveEndpoint) authenticate(t *testing.T) string {
	t.Helper()
	connectionID := make([]byte, 16)
	if _, err := rand.Read(connectionID); err != nil {
		t.Fatal(err)
	}
	e.send(t, FrameHello, helloPayload{
		Role: e.role, ConnectionID: hex.EncodeToString(connectionID), KeyID: hex.EncodeToString(e.identity.KeyID()),
		Ed25519: base64.RawURLEncoding.EncodeToString(e.identity.SigningPublic()),
		X25519:  base64.RawURLEncoding.EncodeToString(e.identity.AgreementPublic()),
	})
	frame := e.waitFor(t, FrameChallenge, 20*time.Second)
	var challenge challengePayload
	if err := DecodeInto(frame.Payload, &challenge); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(challenge.Challenge)
	if err != nil {
		t.Fatal(err)
	}
	e.send(t, FrameAuth, authPayload{
		ConnectionID: challenge.ConnectionID,
		Signature:    base64.RawURLEncoding.EncodeToString(SignAuth(e.identity, e.role, connectionID, raw)),
	})
	ready := e.waitFor(t, FrameReady, 20*time.Second)
	var readyFrame readyPayload
	if err := DecodeInto(ready.Payload, &readyFrame); err != nil {
		t.Fatal(err)
	}
	return readyFrame.Build
}

func newLiveIdentity(t *testing.T) Identity {
	t.Helper()
	seed := make([]byte, 32)
	agreement := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(agreement); err != nil {
		t.Fatal(err)
	}
	return Identity{SigningSeed: seed, Agreement: agreement}
}

// The whole acceptance, in order, against the live broker.
func TestLiveBrokerPairsRoundTripsAndRevokes2kq(t *testing.T) {
	address := liveAddress(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	agent := newLiveIdentity(t)
	device := newLiveIdentity(t)

	// 1. PAIR. The agent goes first with the hash of a code; the synthetic device
	// answers with the code itself. Neither side is authenticated yet: the document
	// says pairing IS the first frame on an unpaired connection.
	pairAgent := dialLive(t, ctx, address, "agent", agent)
	pairDevice := dialLive(t, ctx, address, "device", device)
	code, raw, err := NewPairingCode()
	if err != nil {
		t.Fatal(err)
	}
	pairAgent.send(t, FramePairBegin, pairBeginPayload{
		Role: "agent", CodeHash: hex.EncodeToString(CodeHash(raw)),
		Ed25519: base64.RawURLEncoding.EncodeToString(agent.SigningPublic()),
		X25519:  base64.RawURLEncoding.EncodeToString(agent.AgreementPublic()),
	})
	pairAgent.waitFor(t, FramePairWaiting, 20*time.Second)
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

	peerFrame := pairAgent.waitFor(t, FramePairPeer, 20*time.Second)
	pairDevice.waitFor(t, FramePairPeer, 20*time.Second)
	var peer pairPeerPayload
	if err := DecodeInto(peerFrame.Payload, &peer); err != nil {
		t.Fatal(err)
	}
	pairingID, err := hex.DecodeString(peer.PairingID)
	if err != nil {
		t.Fatal(err)
	}
	// The key id the broker labels the peer with is DERIVED here, not believed.
	if got := hex.EncodeToString(Identity{}.keyIDFor(mustDecode64(t, peer.Ed25519), mustDecode64(t, peer.X25519))); got != peer.PeerKeyID {
		t.Fatalf("the broker named key id %s for keys that derive %s", peer.PeerKeyID, got)
	}
	transcript := TranscriptHash(pairingID, agent.SigningPublic(), agent.AgreementPublic(), device.SigningPublic(), device.AgreementPublic())
	for _, side := range []struct {
		endpoint *liveEndpoint
		identity Identity
	}{{pairAgent, agent}, {pairDevice, device}} {
		side.endpoint.send(t, FramePairConfirm, pairConfirmPayload{
			PairingID: peer.PairingID, TranscriptHash: hex.EncodeToString(transcript),
			Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(side.identity.SigningSeed), transcript)),
		})
	}
	pairAgent.waitFor(t, FramePairComplete, 20*time.Second)
	pairDevice.waitFor(t, FramePairComplete, 20*time.Second)
	fingerprint := Fingerprint(agent.SigningPublic(), agent.AgreementPublic(), device.SigningPublic(), device.AgreementPublic())
	t.Logf("LIVE 1 paired: pairing %s, fingerprint %s", peer.PairingID, fingerprint)
	_ = pairAgent.transport.Close(1000, "paired")
	_ = pairDevice.transport.Close(1000, "paired")

	// 2. AUTHENTICATE on new connections, which is what the document says follows
	// PAIR_COMPLETE.
	liveAgent := dialLive(t, ctx, address, "agent", agent)
	liveDevice := dialLive(t, ctx, address, "device", device)
	build := liveAgent.authenticate(t)
	liveDevice.authenticate(t)
	t.Logf("LIVE 2 authenticated: broker build %q", build)

	// 3. ESTABLISH the peer session THROUGH the broker, both roles real.
	handshakeID := make([]byte, 16)
	if _, err := rand.Read(handshakeID); err != nil {
		t.Fatal(err)
	}
	ephemeral := make([]byte, 32)
	if _, err := rand.Read(ephemeral); err != nil {
		t.Fatal(err)
	}
	prologue := SessionPrologue(pairingID, handshakeID, agent.KeyID(), device.KeyID())
	initiator, err := NewInitiator(agent, device.AgreementPublic(), prologue, ephemeral)
	if err != nil {
		t.Fatal(err)
	}
	messageOne, err := initiator.WriteInit()
	if err != nil {
		t.Fatal(err)
	}
	liveAgent.send(t, FrameSessionInit, sessionFrame{
		PairingID: peer.PairingID, HandshakeID: hex.EncodeToString(handshakeID),
		SenderKeyID: hex.EncodeToString(agent.KeyID()), RecipientID: hex.EncodeToString(device.KeyID()),
		Noise:     base64.RawURLEncoding.EncodeToString(messageOne),
		Signature: base64.RawURLEncoding.EncodeToString(initiator.InitSignature(messageOne)),
	})
	initFrame := liveDevice.waitFor(t, FrameSessionInit, 20*time.Second)
	var forwarded sessionFrame
	if err := DecodeInto(initFrame.Payload, &forwarded); err != nil {
		t.Fatal(err)
	}
	deviceEphemeral := make([]byte, 32)
	if _, err := rand.Read(deviceEphemeral); err != nil {
		t.Fatal(err)
	}
	responder, err := NewResponder(device, prologue, deviceEphemeral)
	if err != nil {
		t.Fatal(err)
	}
	if err := responder.ReadInit(mustDecode64(t, forwarded.Noise), agent.SigningPublic(), mustDecode64(t, forwarded.Signature)); err != nil {
		t.Fatalf("the device refused the agent's init over the live broker: %v", err)
	}
	messageTwo, err := responder.WriteResponse()
	if err != nil {
		t.Fatal(err)
	}
	liveDevice.send(t, FrameSessionResp, sessionFrame{
		PairingID: peer.PairingID, HandshakeID: forwarded.HandshakeID,
		SenderKeyID: hex.EncodeToString(device.KeyID()), RecipientID: hex.EncodeToString(agent.KeyID()),
		Noise:     base64.RawURLEncoding.EncodeToString(messageTwo),
		Signature: base64.RawURLEncoding.EncodeToString(responder.ConfirmSignature()),
	})
	responseFrame := liveAgent.waitFor(t, FrameSessionResp, 20*time.Second)
	var response sessionFrame
	if err := DecodeInto(responseFrame.Payload, &response); err != nil {
		t.Fatal(err)
	}
	if err := initiator.ReadResponse(mustDecode64(t, response.Noise), device.SigningPublic(), mustDecode64(t, response.Signature)); err != nil {
		t.Fatalf("the agent refused the device's response: %v", err)
	}
	finish, err := initiator.SealFinish()
	if err != nil {
		t.Fatal(err)
	}
	liveAgent.send(t, FrameSessionFinish, sessionFrame{
		PairingID: peer.PairingID, HandshakeID: hex.EncodeToString(handshakeID),
		SenderKeyID: hex.EncodeToString(agent.KeyID()), RecipientID: hex.EncodeToString(device.KeyID()),
		Confirmation: base64.RawURLEncoding.EncodeToString(finish),
	})
	finishFrame := liveDevice.waitFor(t, FrameSessionFinish, 20*time.Second)
	var sealedFinish sessionFrame
	if err := DecodeInto(finishFrame.Payload, &sealedFinish); err != nil {
		t.Fatal(err)
	}
	if err := responder.OpenFinish(mustDecode64(t, sealedFinish.Confirmation), agent.SigningPublic()); err != nil {
		t.Fatalf("the device refused the agent's finish: %v", err)
	}
	ready, err := responder.SealReady()
	if err != nil {
		t.Fatal(err)
	}
	liveDevice.send(t, FrameSessionReady, sessionFrame{
		PairingID: peer.PairingID, HandshakeID: forwarded.HandshakeID,
		SenderKeyID: hex.EncodeToString(device.KeyID()), RecipientID: hex.EncodeToString(agent.KeyID()),
		SessionID: responder.SessionID(), Confirmation: base64.RawURLEncoding.EncodeToString(ready),
	})
	readyFrame := liveAgent.waitFor(t, FrameSessionReady, 20*time.Second)
	var sessionReady sessionFrame
	if err := DecodeInto(readyFrame.Payload, &sessionReady); err != nil {
		t.Fatal(err)
	}
	if err := initiator.OpenReady(mustDecode64(t, sessionReady.Confirmation)); err != nil {
		t.Fatalf("the agent refused the device's ready: %v", err)
	}
	if sessionReady.SessionID != initiator.SessionID() {
		t.Fatalf("the two ends derived different session ids: %s and %s", sessionReady.SessionID, initiator.SessionID())
	}
	t.Logf("LIVE 3 session established through the broker: %s", initiator.SessionID())

	// 4. ROUND TRIP one message end to end, and the broker never sees the plaintext.
	messageID := make([]byte, 16)
	if _, err := rand.Read(messageID); err != nil {
		t.Fatal(err)
	}
	sessionID, err := hex.DecodeString(initiator.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	plaintext := "live round trip"
	aad := TransportAAD(pairingID, sessionID, 0, DirectionAgentToDevice, agent.KeyID(), device.KeyID(), messageID, 1)
	ciphertext, err := initiator.SealMessage(aad, []byte(plaintext))
	if err != nil {
		t.Fatal(err)
	}
	liveAgent.send(t, FrameCiphertext, ciphertextFrame{
		PairingID: peer.PairingID, SessionID: initiator.SessionID(), MessageID: hex.EncodeToString(messageID),
		SenderKeyID: hex.EncodeToString(agent.KeyID()), RecipientKeyID: hex.EncodeToString(device.KeyID()),
		Epoch: 0, Counter: 1, PlaintextBytes: len(plaintext),
		Ciphertext: base64.RawURLEncoding.EncodeToString(ciphertext),
	})
	delivered := liveDevice.waitFor(t, FrameCiphertext, 20*time.Second)
	if strings.Contains(string(delivered.Payload), plaintext) {
		t.Fatal("the plaintext crossed the broker in the clear")
	}
	var carried ciphertextFrame
	if err := DecodeInto(delivered.Payload, &carried); err != nil {
		t.Fatal(err)
	}
	openedAAD := TransportAAD(pairingID, sessionID, carried.Epoch, DirectionAgentToDevice, agent.KeyID(), device.KeyID(), messageID, carried.Counter)
	opened, err := responder.OpenMessage(openedAAD, mustDecode64(t, carried.Ciphertext))
	if err != nil {
		t.Fatalf("the device could not open the delivered message: %v", err)
	}
	if string(opened) != plaintext {
		t.Fatalf("the device read %q", opened)
	}
	liveDevice.send(t, FrameAck, idOnlyFrame{PairingID: peer.PairingID, SessionID: carried.SessionID, MessageID: carried.MessageID})
	t.Logf("LIVE 4 round trip: %d bytes delivered and opened, plaintext absent from the wire", len(opened))

	// 5. REVOKE from the agent, and nothing is left paired: a new connection with the
	// same key is refused.
	if err := Revoke(liveAgent.transport, agent, pairingID); err != nil {
		t.Fatal(err)
	}
	liveAgent.waitFor(t, FrameRevoked, 20*time.Second)
	t.Log("LIVE 5 revoked")
	_ = liveAgent.transport.Close(1000, "done")
	_ = liveDevice.transport.Close(1000, "done")

	after := dialLive(t, ctx, address, "agent", agent)
	connectionID := make([]byte, 16)
	if _, err := rand.Read(connectionID); err != nil {
		t.Fatal(err)
	}
	after.send(t, FrameHello, helloPayload{
		Role: "agent", ConnectionID: hex.EncodeToString(connectionID), KeyID: hex.EncodeToString(agent.KeyID()),
		Ed25519: base64.RawURLEncoding.EncodeToString(agent.SigningPublic()),
		X25519:  base64.RawURLEncoding.EncodeToString(agent.AgreementPublic()),
	})
	refusal := after.waitFor(t, FrameError, 20*time.Second)
	var problem errorPayload
	if err := DecodeInto(refusal.Payload, &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != "revoked" && problem.Code != "not_paired" {
		t.Fatalf("a revoked key was answered %q", problem.Code)
	}
	t.Logf("LIVE 6 nothing left paired: a reconnect with the revoked key is refused %q", problem.Code)
	_ = after.transport.Close(1000, "done")
}

func mustDecode64(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("not base64url: %q", value)
	}
	return raw
}
