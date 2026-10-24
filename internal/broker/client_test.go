package broker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// Item 2kq (a): the scenarios the document names, each against a SCRIPTED BROKER that
// plays both the broker and the phone. Nothing here reaches the network: what is being
// proved is this client's behaviour when the other end does what the document says.

// scriptedBroker is a transport that a test drives. It answers the connection
// handshake, plays the device through a real responder Session, and records what it
// received.
type scriptedBroker struct {
	t         *testing.T
	agent     Identity
	device    Identity
	pairing   Pairing
	responder *Session

	mu       sync.Mutex
	toClient chan []byte
	fromCli  chan Frame
	closed   chan struct{}
	closeErr error

	// what the test wants to observe
	acked    []string
	received []string
	sessions []string
}

func TestHandledRequestDeduplicationIsBounded2pd(t *testing.T) {
	client := NewClient(Identity{}, Pairing{}, nil, nil)
	client.mu.Lock()
	for index := 0; index < 1100; index++ {
		client.rememberHandled(fmt.Sprint(index), []byte("answer"))
	}
	client.mu.Unlock()
	if len(client.handled) != 1024 || len(client.handledOrder) != 1024 {
		t.Fatalf("handled=%d order=%d", len(client.handled), len(client.handledOrder))
	}
}

func newScriptedBroker(t *testing.T, agent, device Identity, pairing Pairing) *scriptedBroker {
	return &scriptedBroker{
		t: t, agent: agent, device: device, pairing: pairing,
		toClient: make(chan []byte, 32),
		fromCli:  make(chan Frame, 32),
		closed:   make(chan struct{}),
	}
}

func (b *scriptedBroker) Send(frame []byte) error {
	decoded, err := Decode(frame)
	if err != nil {
		return err
	}
	select {
	case b.fromCli <- decoded:
		return nil
	case <-b.closed:
		return errors.New("closed")
	}
}

func (b *scriptedBroker) Receive(ctx context.Context) ([]byte, error) {
	select {
	case frame := <-b.toClient:
		return frame, nil
	case <-b.closed:
		if b.closeErr != nil {
			return nil, b.closeErr
		}
		return nil, errors.New("closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *scriptedBroker) Close(int, string) error { return nil }

func (b *scriptedBroker) push(frameType byte, payload any) {
	raw, err := Encode(frameType, payload)
	if err != nil {
		b.t.Fatal(err)
	}
	b.toClient <- raw
}

func (b *scriptedBroker) next(t *testing.T) Frame {
	t.Helper()
	select {
	case frame := <-b.fromCli:
		return frame
	case <-time.After(5 * time.Second):
		t.Fatal("the client sent nothing")
		return Frame{}
	}
}

func (b *scriptedBroker) authenticate(t *testing.T) {
	t.Helper()
	hello := b.next(t)
	if hello.Type != FrameHello {
		t.Fatalf("first frame is 0x%02x, want HELLO", hello.Type)
	}
	var greeting helloPayload
	if err := DecodeInto(hello.Payload, &greeting); err != nil {
		t.Fatal(err)
	}
	if greeting.Role != "agent" {
		t.Fatalf("the client claims role %q", greeting.Role)
	}
	challenge := make([]byte, 32)
	if _, err := rand.Read(challenge); err != nil {
		t.Fatal(err)
	}
	b.push(FrameChallenge, challengePayload{ConnectionID: greeting.ConnectionID, Challenge: base64.RawURLEncoding.EncodeToString(challenge)})
	auth := b.next(t)
	if auth.Type != FrameAuth {
		t.Fatalf("second frame is 0x%02x, want AUTH", auth.Type)
	}
	var signed authPayload
	if err := DecodeInto(auth.Payload, &signed); err != nil {
		t.Fatal(err)
	}
	connectionID, err := hex.DecodeString(greeting.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(signed.Signature)
	if err != nil {
		t.Fatal(err)
	}
	// The broker's own check: the signature is over the v2 digest.
	if !verifyAuth(b.agent, "agent", connectionID, challenge, signature) {
		t.Fatal("the client's AUTH signature does not verify")
	}
	b.push(FrameReady, readyPayload{ConnectionID: greeting.ConnectionID, ServerTime: time.Now().UTC().Format(time.RFC3339), Build: "scripted"})
	query := b.next(t)
	if query.Type != FramePeerQuery {
		t.Fatalf("frame after READY is 0x%02x, want PEER_QUERY", query.Type)
	}
	var presence peerQueryPayload
	if err := DecodeInto(query.Payload, &presence); err != nil {
		t.Fatal(err)
	}
	if presence.PairingID != hexID(b.pairing.PairingID) {
		t.Fatalf("PEER_QUERY pairing = %q", presence.PairingID)
	}
}

func (b *scriptedBroker) peerHandshake(t *testing.T) {
	t.Helper()
	b.push(FramePeer, peerPayload{PairingID: hexID(b.pairing.PairingID), Connected: true})

	// The peer session, played by a real responder.
	init := b.next(t)
	if init.Type != FrameSessionInit {
		t.Fatalf("expected SESSION_INIT, got 0x%02x", init.Type)
	}
	var initFrame SessionFrame
	if err := DecodeInto(init.Payload, &initFrame); err != nil {
		t.Fatal(err)
	}
	handshakeID, err := hex.DecodeString(initFrame.HandshakeID)
	if err != nil {
		t.Fatal(err)
	}
	prologue := SessionPrologue(b.pairing.PairingID, handshakeID, b.agent.KeyID(), b.device.KeyID())
	ephemeral := make([]byte, 32)
	if _, err := rand.Read(ephemeral); err != nil {
		t.Fatal(err)
	}
	responder, err := NewResponder(b.device, prologue, ephemeral)
	if err != nil {
		t.Fatal(err)
	}
	messageOne, err := base64.RawURLEncoding.DecodeString(initFrame.Noise)
	if err != nil {
		t.Fatal(err)
	}
	initSignature, err := base64.RawURLEncoding.DecodeString(initFrame.Signature)
	if err != nil {
		t.Fatal(err)
	}
	if err := responder.ReadInit(messageOne, b.agent.SigningPublic(), initSignature); err != nil {
		t.Fatalf("the device refused the client's init: %v", err)
	}
	messageTwo, err := responder.WriteResponse()
	if err != nil {
		t.Fatal(err)
	}
	b.push(FrameSessionResp, SessionFrame{
		PairingID: initFrame.PairingID, HandshakeID: initFrame.HandshakeID,
		SenderKeyID: hexID(b.device.KeyID()), RecipientID: hexID(b.agent.KeyID()),
		Noise:     base64.RawURLEncoding.EncodeToString(messageTwo),
		Signature: base64.RawURLEncoding.EncodeToString(responder.ConfirmSignature()),
	})
	finish := b.next(t)
	if finish.Type != FrameSessionFinish {
		t.Fatalf("expected SESSION_FINISH, got 0x%02x", finish.Type)
	}
	var finishFrame SessionFrame
	if err := DecodeInto(finish.Payload, &finishFrame); err != nil {
		t.Fatal(err)
	}
	sealed, err := base64.RawURLEncoding.DecodeString(finishFrame.Confirmation)
	if err != nil {
		t.Fatal(err)
	}
	if err := responder.OpenFinish(sealed, b.agent.SigningPublic()); err != nil {
		t.Fatalf("the device refused the client's finish: %v", err)
	}
	ready, err := responder.SealReady()
	if err != nil {
		t.Fatal(err)
	}
	b.push(FrameSessionReady, SessionFrame{
		PairingID: initFrame.PairingID, HandshakeID: initFrame.HandshakeID,
		SenderKeyID: hexID(b.device.KeyID()), RecipientID: hexID(b.agent.KeyID()),
		SessionID: responder.SessionID(), Confirmation: base64.RawURLEncoding.EncodeToString(ready),
	})
	b.mu.Lock()
	b.responder = responder
	b.sessions = append(b.sessions, responder.SessionID())
	b.mu.Unlock()
}

func (b *scriptedBroker) handshake(t *testing.T) {
	t.Helper()
	b.authenticate(t)
	b.peerHandshake(t)
}

// sendToAgent plays one application request from the phone.
func (b *scriptedBroker) sendToAgent(t *testing.T, messageID []byte, counter uint64, plaintext string) {
	t.Helper()
	b.mu.Lock()
	responder := b.responder
	b.mu.Unlock()
	sessionID, err := hex.DecodeString(responder.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	aad := TransportAAD(b.pairing.PairingID, sessionID, 0, DirectionDeviceToAgent, b.device.KeyID(), b.agent.KeyID(), messageID, counter)
	ciphertext, err := responder.SealMessage(aad, []byte(plaintext))
	if err != nil {
		t.Fatal(err)
	}
	b.push(FrameCiphertext, CiphertextFrame{
		PairingID: hexID(b.pairing.PairingID), SessionID: responder.SessionID(), MessageID: hexID(messageID),
		SenderKeyID: hexID(b.device.KeyID()), RecipientKeyID: hexID(b.agent.KeyID()),
		Epoch: 0, Counter: counter, PlaintextBytes: len(plaintext),
		Ciphertext: base64.RawURLEncoding.EncodeToString(ciphertext),
	})
}

func (b *scriptedBroker) openFromAgent(t *testing.T, frame Frame) string {
	t.Helper()
	var payload CiphertextFrame
	if err := DecodeInto(frame.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := hex.DecodeString(payload.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	messageID, err := hex.DecodeString(payload.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	aad := TransportAAD(b.pairing.PairingID, sessionID, payload.Epoch, DirectionAgentToDevice, b.agent.KeyID(), b.device.KeyID(), messageID, payload.Counter)
	plaintext, err := b.responder.OpenMessage(aad, raw)
	if err != nil {
		t.Fatalf("the device could not open the message: %v", err)
	}
	return string(plaintext)
}

func verifyAuth(identity Identity, role string, connectionID, challenge, signature []byte) bool {
	return ed25519.Verify(identity.SigningPublic(), AuthDigest(role, connectionID, challenge), signature)
}

func waitConnected(t *testing.T, client *Client) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if client.Status().State == "phone connected" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the client never reported connected: %+v", client.Status())
}

func TestStatusNamesBrokerUnreachableAndTheNextAttempt2oe(t *testing.T) {
	agent, _, pairing := testPair(t)
	client := NewClient(agent, pairing, func(context.Context) (Transport, error) {
		return nil, errors.New("recorded broker unavailable")
	}, func([]byte, []byte) []byte { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = client.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status := client.Status()
		if status.State == "broker unreachable" && status.NextAttemptAt != "" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("status never carried a retry time: %+v", client.Status())
}

func TestEndedSessionReasonKeepsTheFrameWords2oe(t *testing.T) {
	if got := formatEndedReason("revoked", "pairing removed by peer"); got != "revoked: pairing removed by peer" {
		t.Fatalf("reason=%q", got)
	}
}

func testPair(t *testing.T) (Identity, Identity, Pairing) {
	t.Helper()
	newIdentity := func() Identity {
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
	agent, device := newIdentity(), newIdentity()
	pairingID := make([]byte, 16)
	if _, err := rand.Read(pairingID); err != nil {
		t.Fatal(err)
	}
	return agent, device, Pairing{
		PairingID:       pairingID,
		DeviceKeyID:     device.KeyID(),
		DeviceSigning:   device.SigningPublic(),
		DeviceAgreement: device.AgreementPublic(),
	}
}

// One request in, one response out, over a real session.
func TestARequestRoundTripsOverTheSession2kq(t *testing.T) {
	agent, device, pairing := testPair(t)
	broker := newScriptedBroker(t, agent, device, pairing)
	executions := 0
	client := NewClient(agent, pairing, func(context.Context) (Transport, error) { return broker, nil },
		func(messageID, plaintext []byte) []byte {
			executions++
			return []byte("answer to " + string(plaintext))
		})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { _ = client.Run(ctx) }()

	broker.handshake(t)
	waitConnected(t, client)
	messageID := make([]byte, 16)
	if _, err := rand.Read(messageID); err != nil {
		t.Fatal(err)
	}
	broker.sendToAgent(t, messageID, 0, "hello from the phone")

	ack := broker.next(t)
	if ack.Type != FrameAck {
		t.Fatalf("the client did not acknowledge first: 0x%02x", ack.Type)
	}
	answer := broker.next(t)
	if answer.Type != FrameCiphertext {
		t.Fatalf("the client did not answer: 0x%02x", answer.Type)
	}
	if plaintext := broker.openFromAgent(t, answer); plaintext != "answer to hello from the phone" {
		t.Fatalf("answer = %q", plaintext)
	}
	if executions != 1 {
		t.Fatalf("the request executed %d times", executions)
	}
}

func TestSessionEventsNameConnectPeerDropAndReconnect2p0(t *testing.T) {
	agent, device, pairing := testPair(t)
	first, second := newScriptedBroker(t, agent, device, pairing), newScriptedBroker(t, agent, device, pairing)
	dials, events := 0, make(chan string, 16)
	client := NewClient(agent, pairing, func(context.Context) (Transport, error) {
		dials++
		if dials == 1 {
			return first, nil
		}
		return second, nil
	}, nil)
	client.OnSessionEvent(func(event string) { events <- event })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = client.Run(ctx) }()
	first.authenticate(t)
	first.push(FramePeer, peerPayload{PairingID: hexID(pairing.PairingID), Connected: false})
	first.peerHandshake(t)
	waitConnected(t, client)
	first.closeErr = errors.New("scripted close " + hexID(pairing.PairingID))
	close(first.closed)
	second.handshake(t)
	want := []string{"connected", "PEER_QUERY sent", "PEER received connected=false", "PEER received connected=true", "session ready", "disconnected reason=scripted close " + hexID(pairing.PairingID)[:8], "reconnect attempt backoff=1s", "connected"}
	for index, expected := range want {
		select {
		case got := <-events:
			if got != expected {
				t.Fatalf("event %d = %q, want %q", index, got, expected)
			}
		case <-ctx.Done():
			t.Fatalf("missing event %d (%q)", index, expected)
		}
	}
}

func TestUndeliverableOpeningWaitsForTheNextPeerWithoutReconnecting2t1(t *testing.T) {
	agent, device, pairing := testPair(t)
	broker := newScriptedBroker(t, agent, device, pairing)
	client := NewClient(agent, pairing, func(context.Context) (Transport, error) { return broker, nil },
		func([]byte, []byte) []byte { return nil })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { _ = client.Run(ctx) }()
	broker.authenticate(t)
	broker.push(FramePeer, peerPayload{PairingID: hexID(pairing.PairingID), Connected: true})
	init := broker.next(t)
	if init.Type != FrameSessionInit {
		t.Fatalf("expected SESSION_INIT, got 0x%02x", init.Type)
	}
	var opening SessionFrame
	if err := DecodeInto(init.Payload, &opening); err != nil {
		t.Fatal(err)
	}
	broker.push(FrameQueued, IDOnlyFrame{MessageID: opening.HandshakeID, RecipientKeyID: hexID(device.KeyID())})
	deadline := time.Now().Add(time.Second)
	for client.Status().State != "holding" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := client.Status().State; got != "holding" {
		t.Fatalf("state after undeliverable opening = %q", got)
	}
	broker.peerHandshake(t)
	waitConnected(t, client)
	if status := client.Status(); status.Reconnects != 0 {
		t.Fatalf("the opening reconnected %d times", status.Reconnects)
	}
}

func TestPCFirstWaitsAndThenConnectsOnPeerTrueWithoutRedial2t1(t *testing.T) {
	agent, device, pairing := testPair(t)
	standIn := newScriptedBroker(t, agent, device, pairing)
	dials := 0
	client := NewClient(agent, pairing, func(context.Context) (Transport, error) {
		dials++
		return standIn, nil
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = client.Run(ctx) }()
	standIn.authenticate(t)
	standIn.push(FramePeer, peerPayload{PairingID: hexID(pairing.PairingID), Connected: false})
	time.Sleep(20 * time.Millisecond)
	if got := client.Status().State; got != "holding" {
		t.Fatalf("offline peer state = %q", got)
	}
	select {
	case frame := <-standIn.fromCli:
		t.Fatalf("offline peer caused frame 0x%02x", frame.Type)
	default:
	}
	started := time.Now()
	standIn.peerHandshake(t)
	waitConnected(t, client)
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("session took %s after PEER true", elapsed)
	}
	if dials != 1 {
		t.Fatalf("dialed %d times", dials)
	}
}

func TestPeerReplacementMakesFreshSessionAndResendsPendingOnce2t1(t *testing.T) {
	agent, device, pairing := testPair(t)
	standIn := newScriptedBroker(t, agent, device, pairing)
	client := NewClient(agent, pairing, func(context.Context) (Transport, error) { return standIn, nil }, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = client.Run(ctx) }()
	standIn.handshake(t)
	waitConnected(t, client)
	firstSession := client.Status().SessionID
	messageID := make([]byte, 16)
	if _, err := rand.Read(messageID); err != nil {
		t.Fatal(err)
	}
	if err := client.Send(messageID, []byte("before replacement"), standIn); err != nil {
		t.Fatal(err)
	}
	if frame := standIn.next(t); frame.Type != FrameCiphertext {
		t.Fatalf("initial send = 0x%02x", frame.Type)
	}
	started := time.Now()
	standIn.peerHandshake(t)
	deadline := time.Now().Add(2 * time.Second)
	for (client.Status().SessionID == "" || client.Status().SessionID == firstSession) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("replacement session took %s", elapsed)
	}
	if second := client.Status().SessionID; second == "" || second == firstSession {
		t.Fatalf("replacement session id = %q after %q", second, firstSession)
	}
	standIn.push(FrameResend, IDOnlyFrame{PairingID: hexID(pairing.PairingID), MessageID: hexID(messageID)})
	if frame := standIn.next(t); frame.Type != FrameCiphertext {
		t.Fatalf("resend = 0x%02x", frame.Type)
	} else if plaintext := standIn.openFromAgent(t, frame); plaintext != "before replacement" {
		t.Fatalf("resent plaintext = %q", plaintext)
	}
	select {
	case frame := <-standIn.fromCli:
		t.Fatalf("pending message was sent more than once: 0x%02x", frame.Type)
	case <-time.After(50 * time.Millisecond):
	}
}

// A retried request id never executes twice: the same answer comes back.
func TestARetriedRequestNeverExecutesTwice2kq(t *testing.T) {
	agent, device, pairing := testPair(t)
	broker := newScriptedBroker(t, agent, device, pairing)
	executions := 0
	client := NewClient(agent, pairing, func(context.Context) (Transport, error) { return broker, nil },
		func(messageID, plaintext []byte) []byte {
			executions++
			return []byte("executed")
		})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { _ = client.Run(ctx) }()
	broker.handshake(t)
	waitConnected(t, client)

	messageID := make([]byte, 16)
	if _, err := rand.Read(messageID); err != nil {
		t.Fatal(err)
	}
	broker.sendToAgent(t, messageID, 0, "do the work")
	if frame := broker.next(t); frame.Type != FrameAck {
		t.Fatalf("expected ACK, got 0x%02x", frame.Type)
	}
	if frame := broker.next(t); frame.Type != FrameCiphertext {
		t.Fatalf("expected the answer, got 0x%02x", frame.Type)
	}
	// The same id again, as a duplicate delivery after a disconnect would arrive.
	broker.sendToAgent(t, messageID, 1, "do the work")
	if frame := broker.next(t); frame.Type != FrameAck {
		t.Fatalf("expected ACK, got 0x%02x", frame.Type)
	}
	if frame := broker.next(t); frame.Type != FrameCiphertext {
		t.Fatalf("expected the same answer again, got 0x%02x", frame.Type)
	}
	if executions != 1 {
		t.Fatalf("a retried request executed %d times", executions)
	}
}

// RESEND after a reconnect: the same message id, encrypted again under the new session.
func TestResendEncryptsTheSameMessageUnderTheNewSession2kq(t *testing.T) {
	agent, device, pairing := testPair(t)
	broker := newScriptedBroker(t, agent, device, pairing)
	client := NewClient(agent, pairing, func(context.Context) (Transport, error) { return broker, nil }, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { _ = client.Run(ctx) }()
	broker.handshake(t)
	waitConnected(t, client)

	messageID := make([]byte, 16)
	if _, err := rand.Read(messageID); err != nil {
		t.Fatal(err)
	}
	if err := client.Send(messageID, []byte("a notice for the phone"), broker); err != nil {
		t.Fatal(err)
	}
	first := broker.next(t)
	if first.Type != FrameCiphertext {
		t.Fatalf("expected the message, got 0x%02x", first.Type)
	}
	if client.Pending() != 1 {
		t.Fatal("the sender did not retain the message until it was acknowledged")
	}
	// The broker asks for it again, which is what a disconnect before ACK produces.
	broker.push(FrameResend, IDOnlyFrame{PairingID: hexID(pairing.PairingID), MessageID: hexID(messageID)})
	second := broker.next(t)
	if second.Type != FrameCiphertext {
		t.Fatalf("expected the resend, got 0x%02x", second.Type)
	}
	var one, two CiphertextFrame
	if err := DecodeInto(first.Payload, &one); err != nil {
		t.Fatal(err)
	}
	if err := DecodeInto(second.Payload, &two); err != nil {
		t.Fatal(err)
	}
	if one.MessageID != two.MessageID {
		t.Fatalf("the resend changed the message id: %s then %s", one.MessageID, two.MessageID)
	}
	if one.Counter == two.Counter {
		t.Fatal("the resend reused the counter, which would repeat a nonce")
	}
	if one.Ciphertext == two.Ciphertext {
		t.Fatal("the resend produced identical ciphertext")
	}
	// And an ACK clears it.
	broker.push(FrameAck, IDOnlyFrame{PairingID: hexID(pairing.PairingID), MessageID: hexID(messageID)})
	deadline := time.Now().Add(3 * time.Second)
	for client.Pending() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if client.Pending() != 0 {
		t.Fatal("an acknowledged message is still retained")
	}
}

// A broker restart: resync offers everything still retained under the live session.
func TestResyncAfterABrokerRestartOffersWhatIsRetained2kq(t *testing.T) {
	agent, device, pairing := testPair(t)
	broker := newScriptedBroker(t, agent, device, pairing)
	client := NewClient(agent, pairing, func(context.Context) (Transport, error) { return broker, nil }, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { _ = client.Run(ctx) }()
	broker.handshake(t)
	waitConnected(t, client)

	for index := 0; index < 3; index++ {
		messageID := make([]byte, 16)
		if _, err := rand.Read(messageID); err != nil {
			t.Fatal(err)
		}
		if err := client.Send(messageID, []byte("queued work"), broker); err != nil {
			t.Fatal(err)
		}
		if frame := broker.next(t); frame.Type != FrameCiphertext {
			t.Fatalf("expected the message, got 0x%02x", frame.Type)
		}
		// The broker reports it queued: the device is offline.
		broker.push(FrameQueued, IDOnlyFrame{PairingID: hexID(pairing.PairingID), MessageID: hexID(messageID), ExpiresAt: time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)})
	}
	if client.Pending() != 3 {
		t.Fatalf("retained %d messages, want 3", client.Pending())
	}
	if err := client.Resync(broker); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		if frame := broker.next(t); frame.Type != FrameCiphertext {
			t.Fatalf("resync sent 0x%02x", frame.Type)
		}
	}
}

// Close 4001: a newer connection for this key won, and this one stops rather than
// reconnecting into a fight.
func TestCloseReplacedStopsRatherThanReconnecting2kq(t *testing.T) {
	agent, device, pairing := testPair(t)
	broker := newScriptedBroker(t, agent, device, pairing)
	dials := 0
	client := NewClient(agent, pairing, func(context.Context) (Transport, error) {
		dials++
		return broker, nil
	}, nil)
	refused := make(chan string, 1)
	client.OnRefused(func(code, detail string) { refused <- code + ": " + detail })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	broker.handshake(t)
	waitConnected(t, client)

	broker.push(FrameError, ErrorPayload{Code: "connection_replaced", Detail: "a newer connection for this key", Fatal: true})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want a clean stop", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the client did not stop after 4001")
	}
	if dials != 1 {
		t.Fatalf("the client dialled %d times after being replaced", dials)
	}
	status := client.Status()
	if status.State != "broker unreachable" {
		t.Fatalf("status = %q", status.State)
	}
	if status.EndedReason != "connection_replaced: a newer connection for this key" {
		t.Fatalf("ended reason = %q", status.EndedReason)
	}
	select {
	case got := <-refused:
		if got != "connection_replaced: a newer connection for this key" {
			t.Fatalf("refusal callback = %q", got)
		}
	default:
		t.Fatal("structured session refusal was not reported")
	}
}

func TestFatalRevokedStopsAndSaysNotPaired2rb(t *testing.T) {
	agent, device, pairing := testPair(t)
	standin := newScriptedBroker(t, agent, device, pairing)
	dials, events := 0, make(chan string, 2)
	client := NewClient(agent, pairing, func(context.Context) (Transport, error) { dials++; return standin, nil }, nil)
	client.OnSessionEvent(func(event string) { events <- event })
	done := make(chan error, 1)
	go func() { done <- client.Run(context.Background()) }()
	if frame := standin.next(t); frame.Type != FrameHello {
		t.Fatalf("first frame = 0x%02x", frame.Type)
	}
	standin.push(FrameError, ErrorPayload{Code: "revoked", Detail: "identity unavailable", Fatal: true})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the revoked client kept reconnecting")
	}
	if dials != 1 || client.Status().State != "not paired" {
		t.Fatalf("dials=%d status=%+v", dials, client.Status())
	}
	if event := <-events; event != "ERROR code=revoked detail=identity unavailable" {
		t.Fatalf("event = %q", event)
	}
}

func TestNonfatalHandshakeErrorKeepsBackoff2rb(t *testing.T) {
	agent, device, pairing := testPair(t)
	standin := newScriptedBroker(t, agent, device, pairing)
	client := NewClient(agent, pairing, func(context.Context) (Transport, error) { return standin, nil }, nil)
	events, ctx := make(chan string, 2), context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	client.OnSessionEvent(func(event string) { events <- event })
	go func() { _ = client.Run(ctx) }()
	_ = standin.next(t)
	standin.push(FrameError, ErrorPayload{Code: "busy", Detail: "try later", Fatal: false})
	for _, want := range []string{"ERROR code=busy detail=try later", "reconnect attempt backoff=1s"} {
		if got := <-events; got != want {
			t.Fatalf("event = %q, want %q", got, want)
		}
	}
	if status := client.Status(); status.Reconnects != 1 || status.NextAttemptAt == "" {
		t.Fatalf("status=%+v", status)
	}
}

func TestReconnectBackoffStartsOverAfterEstablishedSession2un(t *testing.T) {
	backoff := time.Second
	for range 10 {
		backoff = nextReconnectBackoff(backoff, true)
		if backoff != time.Second {
			t.Fatalf("established-session backoff=%s", backoff)
		}
	}
	backoff = time.Second
	for _, want := range []time.Duration{2, 4, 8, 16, 32, 32, 32, 32, 32, 32} {
		backoff = nextReconnectBackoff(backoff, false)
		if backoff != want*time.Second {
			t.Fatalf("consecutive-refusal backoff=%s want=%s", backoff, want*time.Second)
		}
	}
}

func TestRetainedDeliveriesStayWithinSixteenMiB2un(t *testing.T) {
	client := NewClient(Identity{}, Pairing{}, nil, nil)
	if err := client.Deliver(make([]byte, 1<<20)); err == nil || client.PendingBytes() != 0 {
		t.Fatalf("absent phone retained chat contents: err=%v bytes=%d", err, client.PendingBytes())
	}
	unit := make([]byte, 1<<20)
	client.mu.Lock()
	for index := 0; index < 200; index++ {
		id := []byte(fmt.Sprintf("%016d", index))
		client.retainPendingLocked(&Delivery{MessageID: id, Plaintext: append([]byte(nil), unit...)})
	}
	client.mu.Unlock()
	if got := client.PendingBytes(); got > 16<<20 {
		t.Fatalf("retained bytes=%d", got)
	}
	if got := client.Pending(); got != 16 {
		t.Fatalf("retained units=%d want=16 newest units", got)
	}
}

func TestMissingResendAnswersExpiredAndKeepsSession2un(t *testing.T) {
	transport := newScriptedBroker(t, Identity{}, Identity{}, Pairing{})
	client := NewClient(Identity{}, Pairing{}, nil, nil)
	if err := client.resend("00112233445566778899aabbccddeeff", transport); err != nil {
		t.Fatal(err)
	}
	frame := transport.next(t)
	if frame.Type != FrameError {
		t.Fatalf("missing resend answer=0x%02x", frame.Type)
	}
	var problem ErrorPayload
	if err := DecodeInto(frame.Payload, &problem); err != nil || problem.Code != "delivery_expired" || problem.Fatal {
		t.Fatalf("problem=%+v err=%v", problem, err)
	}
}
