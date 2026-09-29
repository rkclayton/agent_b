package broker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
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

// handshake plays the broker's side of HELLO/CHALLENGE/AUTH/READY and then the device's
// side of the peer session, with a real responder so every value is authentic.
func (b *scriptedBroker) handshake(t *testing.T) {
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

	// The peer session, played by a real responder.
	init := b.next(t)
	if init.Type != FrameSessionInit {
		t.Fatalf("expected SESSION_INIT, got 0x%02x", init.Type)
	}
	var initFrame sessionFrame
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
	b.push(FrameSessionResp, sessionFrame{
		PairingID: initFrame.PairingID, HandshakeID: initFrame.HandshakeID,
		SenderKeyID: hexID(b.device.KeyID()), RecipientID: hexID(b.agent.KeyID()),
		Noise:     base64.RawURLEncoding.EncodeToString(messageTwo),
		Signature: base64.RawURLEncoding.EncodeToString(responder.ConfirmSignature()),
	})
	finish := b.next(t)
	if finish.Type != FrameSessionFinish {
		t.Fatalf("expected SESSION_FINISH, got 0x%02x", finish.Type)
	}
	var finishFrame sessionFrame
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
	b.push(FrameSessionReady, sessionFrame{
		PairingID: initFrame.PairingID, HandshakeID: initFrame.HandshakeID,
		SenderKeyID: hexID(b.device.KeyID()), RecipientID: hexID(b.agent.KeyID()),
		SessionID: responder.SessionID(), Confirmation: base64.RawURLEncoding.EncodeToString(ready),
	})
	b.mu.Lock()
	b.responder = responder
	b.sessions = append(b.sessions, responder.SessionID())
	b.mu.Unlock()
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
	b.push(FrameCiphertext, ciphertextFrame{
		PairingID: hexID(b.pairing.PairingID), SessionID: responder.SessionID(), MessageID: hexID(messageID),
		SenderKeyID: hexID(b.device.KeyID()), RecipientKeyID: hexID(b.agent.KeyID()),
		Epoch: 0, Counter: counter, PlaintextBytes: len(plaintext),
		Ciphertext: base64.RawURLEncoding.EncodeToString(ciphertext),
	})
}

func verifyAuth(identity Identity, role string, connectionID, challenge, signature []byte) bool {
	return ed25519.Verify(identity.SigningPublic(), AuthDigest(role, connectionID, challenge), signature)
}

// waitConnected is the one synchronisation these scenarios need: the client establishes
// its session on its own goroutine, and a test that sends before it is ready is racing
// the code rather than testing it.
func waitConnected(t *testing.T, client *Client) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if client.Status().State == "connected" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the client never reported connected: %+v", client.Status())
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
	var payload ciphertextFrame
	if err := DecodeInto(answer.Payload, &payload); err != nil {
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
	responseID, err := hex.DecodeString(payload.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	aad := TransportAAD(pairing.PairingID, sessionID, payload.Epoch, DirectionAgentToDevice, agent.KeyID(), device.KeyID(), responseID, payload.Counter)
	plaintext, err := broker.responder.OpenMessage(aad, raw)
	if err != nil {
		t.Fatalf("the device could not open the answer: %v", err)
	}
	if string(plaintext) != "answer to hello from the phone" {
		t.Fatalf("answer = %q", plaintext)
	}
	if executions != 1 {
		t.Fatalf("the request executed %d times", executions)
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
	broker.push(FrameResend, idOnlyFrame{PairingID: hexID(pairing.PairingID), MessageID: hexID(messageID)})
	second := broker.next(t)
	if second.Type != FrameCiphertext {
		t.Fatalf("expected the resend, got 0x%02x", second.Type)
	}
	var one, two ciphertextFrame
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
	broker.push(FrameAck, idOnlyFrame{PairingID: hexID(pairing.PairingID), MessageID: hexID(messageID)})
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
		broker.push(FrameQueued, idOnlyFrame{PairingID: hexID(pairing.PairingID), MessageID: hexID(messageID), ExpiresAt: time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)})
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	broker.handshake(t)
	waitConnected(t, client)

	broker.push(FrameError, errorPayload{Code: "connection_replaced", Detail: "a newer connection for this key", Fatal: true})
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
	if state := client.Status().State; state != "replaced" {
		t.Fatalf("status = %q", state)
	}
}
