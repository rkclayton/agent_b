package broker

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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

// Item 2o7's acceptance, END TO END: a DISPOSABLE Agent_b (never the operator's) is
// paired through the live broker with a synthetic device played here, and every route
// is exercised the way the phone uses it. Opt-in, because it reaches the public broker
// and starts a process:
//
//	$env:AGENTB_BROKER_LIVE = 'wss://broker.agentb.app/v1/connect'
//	$env:AGENTB_E2E_APP = '<a folder holding a built Agent_b.exe, web\ and prompts\>'
//	go test ./internal/broker -run PairedDesktop -v
func TestLivePairedDesktopAnswersItsPhone2o7(t *testing.T) {
	address := liveAddress(t)
	app := strings.TrimSpace(os.Getenv("AGENTB_E2E_APP"))
	if app == "" {
		t.Skip("SKIPPED: AGENTB_E2E_APP names no built Agent_b to run disposably")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	model := newStubModel(t)
	base := startDisposableAgent(t, ctx, app, address, model.server.URL)
	desktop := newDesktopClient(t, base)
	defer func() {
		if t.Failed() {
			if response, err := desktop.client.Get(base + "/api/broker/status"); err == nil {
				status, _ := io.ReadAll(response.Body)
				response.Body.Close()
				t.Logf("desktop broker status at failure: %s", status)
			}
		}
	}()

	// 1. PAIR: the desktop offers a code as its Settings page does; the synthetic
	// device joins with it, and both sides confirm the transcript.
	var offer PairingOffer
	desktop.post(t, "/api/broker", `{"action":"pair"}`, &offer)
	device := newLiveIdentity(t)
	join := dialLive(t, ctx, address, "device", device)
	join.send(t, FramePairBegin, struct {
		Role    string `json:"role"`
		Ed25519 string `json:"ed25519_public"`
		X25519  string `json:"x25519_public"`
		Code    string `json:"code"`
	}{Role: "device", Code: strings.ReplaceAll(offer.Code, "-", ""),
		Ed25519: base64.RawURLEncoding.EncodeToString(device.SigningPublic()),
		X25519:  base64.RawURLEncoding.EncodeToString(device.AgreementPublic())})
	var peer pairPeerPayload
	if err := DecodeInto(join.waitFor(t, FramePairPeer, 30*time.Second).Payload, &peer); err != nil {
		t.Fatal(err)
	}
	pairingID := mustDecodeHex(t, peer.PairingID)
	agentSigning, agentAgreement := mustDecode64(t, peer.Ed25519), mustDecode64(t, peer.X25519)
	agentKeyID := Identity{}.keyIDFor(agentSigning, agentAgreement)
	transcript := TranscriptHash(pairingID, agentSigning, agentAgreement, device.SigningPublic(), device.AgreementPublic())
	join.send(t, FramePairConfirm, pairConfirmPayload{PairingID: peer.PairingID, TranscriptHash: hex.EncodeToString(transcript),
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(device.SigningSeed), transcript))})
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if desktop.tryPost("/api/broker", `{"action":"confirm"}`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the desktop never reached the fingerprint step")
		}
	}
	join.waitFor(t, FramePairComplete, 30*time.Second)
	_ = join.transport.Close(1000, "paired")
	t.Logf("E2E 1 paired %s", peer.PairingID)

	// 2. The device connects and ANSWERS the desktop's session: the desktop dials on
	// its own, because a pairing now exists (2o7 (a)).
	phone := connectPhone(t, ctx, address, device, peer.PeerKeyID, pairingID, agentSigning, agentKeyID)
	snapshotBefore := phone.count("snapshot")
	t.Logf("E2E 2 session %s established by the desktop; %d snapshot units on connect", phone.sessionID, snapshotBefore)

	// 3. chat.create with no label makes a chat named as the desktop names one.
	created := phone.request(t, "chat.create", `{}`, 201)
	sessionID := created["body"].(map[string]any)["session"].(map[string]any)["id"].(string)
	var desktopChat struct {
		Session struct{ Label string } `json:"session"`
	}
	desktop.post(t, "/api/sessions", `{}`, &desktopChat)
	phoneLabel := created["body"].(map[string]any)["session"].(map[string]any)["label"].(string)
	if strings.TrimRight(phoneLabel, "0123456789 ") != strings.TrimRight(desktopChat.Session.Label, "0123456789 ") {
		t.Fatalf("the phone's chat is named %q, the desktop names one %q", phoneLabel, desktopChat.Session.Label)
	}
	// state lists the instance's chats, the new one among them.
	state := phone.request(t, "state", `{}`, 200)
	if !strings.Contains(fmt.Sprint(state["body"]), sessionID) {
		t.Fatalf("state does not list chat %s", sessionID)
	}
	t.Logf("E2E 3 chat.create %s named %q (desktop names one %q); state lists it", sessionID, phoneLabel, desktopChat.Session.Label)

	// 4. "hi" starts a run, and the device sees the answer arrive as patches in cursor order.
	phone.request(t, "message", fmt.Sprintf(`{"session_id":%q,"text":"hi"}`, sessionID), 202)
	phone.waitText(t, sessionID, "hello from the stub")
	if !phone.patchesChain(sessionID) {
		t.Fatal("the patches did not arrive in cursor order")
	}
	t.Log("E2E 4 message answered on the phone, patches in cursor order")

	// 5. A card raised by the run is approved from the phone and the run continues.
	phone.request(t, "message", fmt.Sprintf(`{"session_id":%q,"text":"approve me"}`, sessionID), 202)
	callID := phone.pendingApproval(t, sessionID)
	phone.request(t, "approve", fmt.Sprintf(`{"session_id":%q,"call_id":%q,"decision":"approve"}`, sessionID, callID), 200)
	phone.waitText(t, sessionID, "done after approval")
	t.Logf("E2E 5 card %s approved on the phone; the run continued", callID)

	// 6. stop stops a live run.
	phone.request(t, "message", fmt.Sprintf(`{"session_id":%q,"text":"long"}`, sessionID), 202)
	time.Sleep(2 * time.Second)
	phone.request(t, "stop", fmt.Sprintf(`{"session_id":%q}`, sessionID), 200)
	phone.waitStopped(t, sessionID)
	t.Log("E2E 6 stop stopped the run")

	// 7. A deliberately dropped patch is recovered by resync, which returns a fresh
	// state no older than the patch that was dropped.
	phone.dropNextPatch(sessionID)
	phone.request(t, "message", fmt.Sprintf(`{"session_id":%q,"text":"hi"}`, sessionID), 202)
	dropped := phone.waitDropped(t)
	fresh := phone.request(t, "resync", fmt.Sprintf(`{"session_id":%q}`, sessionID), 200)
	if !strings.Contains(fmt.Sprint(fresh["body"]), sessionID) {
		t.Fatalf("resync did not return chat %s", sessionID)
	}
	t.Logf("E2E 7 dropped patch at cursor %v; resync returned the chat's state", dropped)

	// 8. An unknown route and each refused route are answered 501, never proxied.
	for _, route := range []string{"no.such.route", "config", "update", "host-window", "plan.go", "/api/config"} {
		phone.request(t, route, `{}`, 501)
	}
	t.Log("E2E 8 unknown and refused routes answered 501")

	// 9. Revoke from the desktop: the session closes and stays closed.
	desktop.post(t, "/api/broker", `{"action":"revoke"}`, nil)
	status := func() string {
		response, err := desktop.client.Get(base + "/api/broker/status")
		if err != nil {
			return err.Error()
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return string(body)
	}
	if first := status(); !strings.Contains(first, `"state":"not paired"`) {
		t.Fatalf("after revoke the desktop reports %s", first)
	}
	time.Sleep(5 * time.Second)
	if later := status(); !strings.Contains(later, `"state":"not paired"`) || strings.Contains(later, `"session_id"`) {
		t.Fatalf("the session did not stay closed: %s", later)
	}
	t.Log("E2E 9 revoked from the desktop; the session closed and stayed closed")
}

// connectedPhone is the synthetic device after its session is up.
type connectedPhone struct {
	endpoint  *liveEndpoint
	device    Identity
	pairing   []byte
	agentKey  []byte
	responder *Session
	sessionID string
	counter   uint64

	mu        sync.Mutex
	units     []map[string]any
	parts     [][]byte
	dropFor   string
	droppedAt any
}

func connectPhone(t *testing.T, ctx context.Context, address string, device Identity, pairingHex string, pairingID, agentSigning, agentKeyID []byte) *connectedPhone {
	t.Helper()
	endpoint := dialLive(t, ctx, address, "device", device)
	endpoint.authenticate(t)
	init := endpoint.waitFor(t, FrameSessionInit, 60*time.Second)
	var forwarded sessionFrame
	if err := DecodeInto(init.Payload, &forwarded); err != nil {
		t.Fatal(err)
	}
	ephemeral := make([]byte, 32)
	if _, err := rand.Read(ephemeral); err != nil {
		t.Fatal(err)
	}
	responder, err := NewResponder(device, SessionPrologue(pairingID, mustDecodeHex(t, forwarded.HandshakeID), agentKeyID, device.KeyID()), ephemeral)
	if err != nil {
		t.Fatal(err)
	}
	if err := responder.ReadInit(mustDecode64(t, forwarded.Noise), agentSigning, mustDecode64(t, forwarded.Signature)); err != nil {
		t.Fatalf("the device refused the desktop's init: %v", err)
	}
	messageTwo, err := responder.WriteResponse()
	if err != nil {
		t.Fatal(err)
	}
	endpoint.send(t, FrameSessionResp, sessionFrame{PairingID: forwarded.PairingID, HandshakeID: forwarded.HandshakeID,
		SenderKeyID: hex.EncodeToString(device.KeyID()), RecipientID: hex.EncodeToString(agentKeyID),
		Noise: base64.RawURLEncoding.EncodeToString(messageTwo), Signature: base64.RawURLEncoding.EncodeToString(responder.ConfirmSignature())})
	var finish sessionFrame
	if err := DecodeInto(endpoint.waitFor(t, FrameSessionFinish, 30*time.Second).Payload, &finish); err != nil {
		t.Fatal(err)
	}
	if err := responder.OpenFinish(mustDecode64(t, finish.Confirmation), agentSigning); err != nil {
		t.Fatal(err)
	}
	ready, err := responder.SealReady()
	if err != nil {
		t.Fatal(err)
	}
	endpoint.send(t, FrameSessionReady, sessionFrame{PairingID: forwarded.PairingID, HandshakeID: forwarded.HandshakeID,
		SenderKeyID: hex.EncodeToString(device.KeyID()), RecipientID: hex.EncodeToString(agentKeyID),
		SessionID: responder.SessionID(), Confirmation: base64.RawURLEncoding.EncodeToString(ready)})
	phone := &connectedPhone{endpoint: endpoint, device: device, pairing: pairingID, agentKey: agentKeyID, responder: responder, sessionID: responder.SessionID(), counter: 1}
	go phone.receive(t)
	return phone
}

// receive opens every unit in arrival order, ACKs it, reassembles parts, and keeps it.
func (p *connectedPhone) receive(t *testing.T) {
	for {
		var frame Frame
		select {
		case frame = <-p.endpoint.frames:
		case err := <-p.endpoint.errs:
			t.Logf("the phone's connection ended: %v", err)
			return
		}
		if frame.Type == FrameRevoked {
			p.endpoint.frames <- frame
			return
		}
		// A real phone answers the broker's keepalive; one that does not is dropped.
		if frame.Type == FramePing {
			var ping struct {
				Token string `json:"token"`
			}
			if DecodeInto(frame.Payload, &ping) == nil {
				pong, _ := Encode(FramePong, ping)
				_ = p.endpoint.transport.Send(pong)
			}
			continue
		}
		if frame.Type == FrameError {
			var problem errorPayload
			_ = DecodeInto(frame.Payload, &problem)
			t.Logf("the phone got broker ERROR %s (%s)", problem.Code, problem.Detail)
		}
		if frame.Type != FrameCiphertext {
			continue
		}
		var carried ciphertextFrame
		if DecodeInto(frame.Payload, &carried) != nil {
			continue
		}
		sessionID, _ := hex.DecodeString(carried.SessionID)
		messageID, _ := hex.DecodeString(carried.MessageID)
		ciphertext, _ := base64.RawURLEncoding.DecodeString(carried.Ciphertext)
		aad := TransportAAD(p.pairing, sessionID, carried.Epoch, DirectionAgentToDevice, p.agentKey, p.device.KeyID(), messageID, carried.Counter)
		plaintext, err := p.responder.OpenMessage(aad, ciphertext)
		if err != nil {
			t.Errorf("the phone could not open a unit: %v", err)
			return
		}
		ack, _ := Encode(FrameAck, idOnlyFrame{PairingID: carried.PairingID, SessionID: carried.SessionID, MessageID: carried.MessageID})
		_ = p.endpoint.transport.Send(ack)
		var unit map[string]any
		if json.Unmarshal(plaintext, &unit) != nil {
			continue
		}
		p.mu.Lock()
		if unit["kind"] == "part" {
			p.parts = append(p.parts, plaintext)
			if index, count := unit["index"].(float64), unit["count"].(float64); index+1 == count {
				whole := []byte{}
				for _, raw := range p.parts {
					var part struct{ Chunk string }
					_ = json.Unmarshal(raw, &part)
					chunk, _ := base64.RawURLEncoding.DecodeString(part.Chunk)
					whole = append(whole, chunk...)
				}
				p.parts = nil
				unit = nil
				_ = json.Unmarshal(whole, &unit)
			} else {
				p.mu.Unlock()
				continue
			}
		}
		data, _ := unit["data"].(map[string]any)
		if unit["kind"] == "patch" && p.dropFor != "" && data["session_id"] == p.dropFor {
			p.droppedAt, p.dropFor = data["cursor"], ""
			p.mu.Unlock()
			continue
		}
		p.units = append(p.units, unit)
		p.mu.Unlock()
	}
}

// request sends one request unit and returns the response naming its id.
func (p *connectedPhone) request(t *testing.T, route, body string, wantStatus int) map[string]any {
	t.Helper()
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	unit := fmt.Sprintf(`{"v":1,"kind":"request","id":%q,"route":%q,"body":%s}`, hex.EncodeToString(id), route, body)
	messageID := make([]byte, 16)
	if _, err := rand.Read(messageID); err != nil {
		t.Fatal(err)
	}
	sessionID := mustDecodeHex(t, p.sessionID)
	aad := TransportAAD(p.pairing, sessionID, 0, DirectionDeviceToAgent, p.device.KeyID(), p.agentKey, messageID, p.counter)
	ciphertext, err := p.responder.SealMessage(aad, []byte(unit))
	if err != nil {
		t.Fatal(err)
	}
	p.endpoint.send(t, FrameCiphertext, ciphertextFrame{PairingID: hex.EncodeToString(p.pairing), SessionID: p.sessionID, MessageID: hex.EncodeToString(messageID),
		SenderKeyID: hex.EncodeToString(p.device.KeyID()), RecipientKeyID: hex.EncodeToString(p.agentKey), Epoch: 0, Counter: p.counter,
		PlaintextBytes: len(unit), Ciphertext: base64.RawURLEncoding.EncodeToString(ciphertext)})
	p.counter++
	response := p.wait(t, "the response to "+route, func(unit map[string]any) bool {
		return unit["kind"] == "response" && unit["id"] == hex.EncodeToString(id)
	})
	if status, _ := response["status"].(float64); int(status) != wantStatus {
		t.Fatalf("route %s answered %v, want %d: %v", route, response["status"], wantStatus, response["body"])
	}
	return response
}

func (p *connectedPhone) wait(t *testing.T, what string, match func(map[string]any) bool) map[string]any {
	t.Helper()
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		p.mu.Lock()
		for _, unit := range p.units {
			if match(unit) {
				p.mu.Unlock()
				return unit
			}
		}
		p.mu.Unlock()
	}
	t.Fatalf("the phone never received %s", what)
	return nil
}

func (p *connectedPhone) count(kind string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, unit := range p.units {
		if unit["kind"] == kind {
			n++
		}
	}
	return n
}

func (p *connectedPhone) waitText(t *testing.T, sessionID, text string) {
	t.Helper()
	p.wait(t, fmt.Sprintf("%q in chat %s", text, sessionID), func(unit map[string]any) bool {
		data, _ := unit["data"].(map[string]any)
		encoded, _ := json.Marshal(unit)
		return unit["kind"] == "patch" && data["session_id"] == sessionID && strings.Contains(string(encoded), text)
	})
}

func (p *connectedPhone) waitStopped(t *testing.T, sessionID string) {
	t.Helper()
	// The run is over when resync no longer shows it active.
	active := regexp.MustCompile(`"status":"(running|queued|stopping)"`)
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		state := p.request(t, "resync", fmt.Sprintf(`{"session_id":%q}`, sessionID), 200)
		encoded, _ := json.Marshal(state["body"])
		if !active.Match(encoded) {
			return
		}
	}
	t.Fatalf("the run in chat %s never stopped", sessionID)
}

// patchesChain is the phone's gap rule: each patch's previous_cursor is the cursor
// before it for the same chat.
func (p *connectedPhone) patchesChain(sessionID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	var last any
	for _, unit := range p.units {
		data, _ := unit["data"].(map[string]any)
		if unit["kind"] != "patch" || data["session_id"] != sessionID {
			continue
		}
		if last != nil && fmt.Sprint(data["previous_cursor"]) != fmt.Sprint(last) {
			return false
		}
		last = data["cursor"]
	}
	return last != nil
}

func (p *connectedPhone) pendingApproval(t *testing.T, sessionID string) string {
	t.Helper()
	last := ""
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		state := p.request(t, "resync", fmt.Sprintf(`{"session_id":%q}`, sessionID), 200)
		// The card's call id is the model's own tool-call id, which the stub fixes.
		encoded, _ := json.Marshal(state["body"])
		if bytes.Contains(encoded, []byte(`"pending_approval"`)) && bytes.Contains(encoded, []byte(`"phone-card"`)) {
			return "phone-card"
		}
		last = string(encoded)
	}
	t.Fatalf("no approval card was raised; the last state was %.600s", last)
	return ""
}

func (p *connectedPhone) dropNextPatch(sessionID string) {
	p.mu.Lock()
	p.dropFor = sessionID
	p.mu.Unlock()
}

func (p *connectedPhone) waitDropped(t *testing.T) any {
	t.Helper()
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		p.mu.Lock()
		dropped := p.droppedAt
		p.mu.Unlock()
		if dropped != nil {
			return dropped
		}
	}
	t.Fatal("no patch arrived to drop")
	return nil
}

// stubModel answers the disposable Agent_b: "hi" is answered, "approve me" asks to write a
// file (the mutating approval mode puts it to a card), "long" runs a slow shell command, and a
// tool result is answered with the text that shows the run continued.
type stubModel struct{ server *httptest.Server }

func newStubModel(t *testing.T) *stubModel {
	t.Helper()
	stream := func(w http.ResponseWriter, delta map[string]any, finish string) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 4}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"agentb-fake"}]}`)
			return
		case "/props":
			fmt.Fprint(w, `{"n_ctx":32768}`)
			return
		}
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		last := body.Messages[len(body.Messages)-1]
		text := fmt.Sprint(last.Content)
		switch {
		case last.Role == "tool":
			stream(w, map[string]any{"content": "done after approval"}, "stop")
		case strings.Contains(text, "approve me"):
			stream(w, map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "phone-card", "type": "function", "function": map[string]any{"name": "write_file", "arguments": `{"path":"phone-note.txt","content":"written after the phone approved"}`}}}}, "tool_calls")
		case strings.Contains(text, "long"):
			stream(w, map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "phone-long", "type": "function", "function": map[string]any{"name": "shell", "arguments": `{"command":"Start-Sleep -Seconds 60","timeout_s":120}`}}}}, "tool_calls")
		default:
			stream(w, map[string]any{"content": "hello from the stub"}, "stop")
		}
	}))
	t.Cleanup(server.Close)
	return &stubModel{server: server}
}

// startDisposableAgent runs the built Agent_b on a free port with its own data root.
func startDisposableAgent(t *testing.T, ctx context.Context, app, broker, model string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	data := t.TempDir()
	workspace := filepath.Join(data, "workspace")
	config := map[string]any{
		"config_version": 10, "listen": fmt.Sprintf("127.0.0.1:%d", port), "workspace": workspace, "log_dir": filepath.Join(data, "logs"),
		"connections": []any{map[string]any{"id": "stub", "label": "Stub", "base_url": model, "model": "agentb-fake", "credential": "", "request_timeout_s": 10, "probe_mode": "off",
			"context":      map[string]any{"n_ctx": 32768, "reserve_output": 4096},
			"capabilities": map[string]any{"connection": "agentb-fake", "n_ctx": 32768, "streaming": true, "tool_calls": true, "overflow_behavior": "error", "probed_at": time.Now().UTC().Format(time.RFC3339)}}},
		"agents":   []any{map[string]any{"name": "Phone", "b": "stub", "toolset": []string{"read_file", "list_dir", "write_file", "shell"}}},
		"approval": map[string]any{"mode": "mutating"}, "broker": map[string]any{"url": broker},
		"memory": map[string]any{"enabled": false, "dir": filepath.Join(data, "memory"), "max_tokens": 1500},
		"shell":  map[string]any{"command": []string{"powershell", "-NoProfile", "-NonInteractive", "-Command"}, "timeout_s": 120, "max_timeout_s": 600, "file_routing_guard": true, "service_account": map[string]any{"enabled": false}},
	}
	encoded, _ := json.MarshalIndent(config, "", "  ")
	configPath := filepath.Join(data, "harness.json")
	if err := os.WriteFile(configPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command := exec.CommandContext(ctx, filepath.Join(app, "Agent_b.exe"), "-config", configPath, "-app-root", app, "-data-root", data)
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
		if t.Failed() {
			t.Logf("disposable Agent_b output:\n%s", output.String())
		}
	})
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
		if response, err := http.Get(base + "/chat"); err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return base
			}
		}
	}
	t.Fatalf("the disposable Agent_b never answered:\n%s", output.String())
	return ""
}

// desktopClient is the operator's own page: a browser session and its mutation token.
type desktopClient struct {
	base   string
	client *http.Client
	token  string
}

func newDesktopClient(t *testing.T, base string) *desktopClient {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	response, err := client.Get(base + "/chat")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(response.Body)
	response.Body.Close()
	match := regexp.MustCompile(`<meta name="agentb-mutation-token" content="([^"]+)">`).FindSubmatch(page)
	if match == nil {
		t.Fatal("no mutation token in the page")
	}
	return &desktopClient{base: base, client: client, token: string(match[1])}
}

func (d *desktopClient) do(path, body string) (*http.Response, []byte, error) {
	request, _ := http.NewRequest(http.MethodPost, d.base+path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-AgentB-Mutation-Token", d.token)
	response, err := d.client.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)
	return response, payload, nil
}

func (d *desktopClient) post(t *testing.T, path, body string, into any) {
	t.Helper()
	response, payload, err := d.do(path, body)
	if err != nil || response.StatusCode >= 300 {
		t.Fatalf("POST %s: %v %s", path, err, payload)
	}
	if into != nil {
		if err := json.Unmarshal(payload, into); err != nil {
			t.Fatalf("POST %s: %v %s", path, err, payload)
		}
	}
}

func (d *desktopClient) tryPost(path, body string) bool {
	response, _, err := d.do(path, body)
	return err == nil && response.StatusCode < 300
}

func mustDecodeHex(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("not hex: %q", value)
	}
	return raw
}
