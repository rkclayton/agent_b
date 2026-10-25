package broker

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// Item 2wq: what a real phone sends, against the stand-in broker with the real broker's
// refusal and delivery rules.

func randomHex(t *testing.T) string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return hexID(raw)
}

func (b *standInBroker) pairingStarted() *standInPair {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pairing["agent"]
}

func (b *standInBroker) pairingHex() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pairingID
}

// A PEER and an ACK from the previous session arrive between SESSION_INIT and
// SESSION_RESPONSE, as O3 saw when the phone reconnected with ACKs in flight.
func TestAPeerAndAStaleAckDuringTheHandshakeDoNotDropIt2wq(t *testing.T) {
	b := newStandIn(t)
	var once sync.Once
	b.inject = func(role string, frame Frame) {
		if role == "device" && frame.Type == FrameSessionResp {
			once.Do(func() {
				b.sendTo("agent", FramePeer, peerPayload{PairingID: b.pairingHex(), Connected: true})
				b.sendTo("agent", FrameAck, IDOnlyFrame{PairingID: b.pairingHex(), SessionID: randomHex(t), MessageID: randomHex(t)})
			})
		}
	}
	replies := make(chan string, 1)
	agent, device := linkThrough(t, b, echo, func(plaintext []byte) { replies <- string(plaintext) })
	if err := device.Send(context.Background(), []byte("after the handshake")); err != nil {
		t.Fatal(err)
	}
	select {
	case reply := <-replies:
		if reply != "echo: after the handshake" {
			t.Fatalf("reply %q", reply)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no reply; agent %+v", agent.Status())
	}
	if status := agent.Status(); status.Reconnects != 0 {
		t.Fatalf("the handshake dropped: %+v", status)
	}
}

// The broker asks each end to RESEND a message it no longer holds. Each declines once with
// RESEND_DECLINED; the broker drops the intent and tells the other end DELIVERY_EXPIRED,
// so no RESEND comes again and nothing reconnects (item 2wr).
func TestAnUnknownResendIsDeclinedOnce2wr(t *testing.T) {
	b := newStandIn(t)
	b.expiry, b.deadline = 30*time.Second, 10*time.Second // the real broker: 5 min, 90 s
	agent, _ := linkThrough(t, b, echo, func([]byte) {})
	for _, role := range []string{"agent", "device"} {
		id := randomHex(t)
		b.mu.Lock()
		b.intents[id] = &standInIntent{sender: role, queued: true, at: time.Now()}
		b.mu.Unlock()
		b.sendTo(role, FrameResend, IDOnlyFrame{PairingID: b.pairingHex(), MessageID: id})
		for deadline := time.Now().Add(5 * time.Second); b.count(role+" 0x28", b.frames) == 0; time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: no RESEND_DECLINED; frames %v", role, b.frames)
			}
		}
		b.mu.Lock()
		_, held := b.intents[id]
		b.mu.Unlock()
		if held {
			t.Fatalf("%s: the stand-in kept the declined intent", role)
		}
	}
	time.Sleep(2 * time.Second)
	t.Logf("RESEND_DECLINED agent %d, device %d; DELIVERY_EXPIRED to device %d, to agent %d; ERROR from agent %d, from device %d; reconnects %d",
		b.count("agent 0x28", b.frames), b.count("device 0x28", b.frames), b.count("device 0x27", b.sent), b.count("agent 0x27", b.sent),
		b.count("agent 0x7f", b.frames), b.count("device 0x7f", b.frames), agent.Status().Reconnects)
	if b.count("agent 0x28", b.frames) != 1 || b.count("device 0x28", b.frames) != 1 || b.count("device 0x27", b.sent) != 1 || b.count("agent 0x27", b.sent) != 1 ||
		b.count("agent 0x7f", b.frames)+b.count("device 0x7f", b.frames) != 0 || agent.Status().Reconnects != 0 {
		t.Fatal("not one decline each, one expiry each, no ERROR and no reconnect")
	}
}

// O4 card 1: the phone's link drops after the device's "session" event and before the
// agent has SESSION_READY. The agent abandons the handshake at handshakeLimit, reconnects
// after the first backoff step, and the phone's redial reaches a session (item 2wr).
func TestADropBeforeSessionReadyIsAbandonedAndRedialed2wr(t *testing.T) {
	b := newStandIn(t)
	b.deadline = time.Minute // the real broker pings an idle link; this one must not cut it
	b.lose = map[string]bool{"device 0x1b": true}
	replies := make(chan string, 1)
	received := func(plaintext []byte) { replies <- string(plaintext) }
	agent, device := linkThrough(t, b, echo, received)
	for deadline := time.Now().Add(5 * time.Second); b.count("device 0x1b", b.frames) == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the stand-in never took the device's SESSION_READY")
		}
	}
	b.cut("device")
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		b.mu.Lock()
		closed := strings.HasPrefix(b.events[len(b.events)-1], "closed")
		b.mu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the device never saw its link end")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	redialed := time.Now()
	if err := device.Connect(ctx, Dial(b.address), received, b.deviceEvent); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(30 * time.Second); agent.Status().State != "phone connected"; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("still waiting 30 s after the redial: %+v", agent.Status())
		}
	}
	if err := device.Send(ctx, []byte("after the drop")); err != nil {
		t.Fatal(err)
	}
	select {
	case reply := <-replies:
		if reply != "echo: after the drop" {
			t.Fatalf("reply %q", reply)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no reply; agent %+v", agent.Status())
	}
	t.Logf("session again %s after the redial; reconnects %d", time.Since(redialed).Round(time.Millisecond), agent.Status().Reconnects)
}

// The broker's nonfatal queue_full, where the real broker gives it: the recipient has no
// session and 64 intents are held. The phone's link then drops and comes back; the
// refused message, which has no intent, is sent again under the new session, same
// message id, and arrives (item 2wr).
func TestQueueFullSendsTheSameMessageAgain2wr(t *testing.T) {
	b := newStandIn(t)
	replies := make(chan string, 1)
	received := func(plaintext []byte) {
		if string(plaintext) == "refused once" {
			replies <- string(plaintext)
		}
	}
	agent, device := linkThrough(t, b, echo, received)
	for deadline := time.Now().Add(5 * time.Second); agent.Status().State != "phone connected"; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no session: %+v", agent.Status())
		}
	}
	var mu sync.Mutex
	var sent []CiphertextFrame
	b.mu.Lock()
	b.inject = func(role string, frame Frame) {
		if role == "agent" && frame.Type == FrameCiphertext {
			var ciphertext CiphertextFrame
			_ = DecodeInto(frame.Payload, &ciphertext)
			mu.Lock()
			sent = append(sent, ciphertext)
			mu.Unlock()
		}
	}
	b.sessionUp = false // the phone's session is going; its link has not closed yet
	for range 64 {
		b.intents[randomHex(t)] = &standInIntent{sender: "device", queued: true, at: time.Now()}
	}
	b.mu.Unlock()
	if err := agent.Deliver([]byte("refused once")); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); b.count("agent 0x7f", b.sent) == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the stand-in never refused with queue_full")
		}
	}
	b.cut("device")
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		b.mu.Lock()
		closed := strings.HasPrefix(b.events[len(b.events)-1], "closed")
		b.mu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the device never saw its link end")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := device.Connect(ctx, Dial(b.address), received, b.deviceEvent); err != nil {
		t.Fatal(err)
	}
	select {
	case <-replies:
	case <-time.After(10 * time.Second):
		t.Fatalf("never delivered; agent %+v", agent.Status())
	}
	mu.Lock()
	defer mu.Unlock()
	t.Logf("CIPHERTEXT for it %d: refused %s session %s counter %d; again %s session %s counter %d; queue_full %d, ERROR from agent %d",
		len(sent), sent[0].MessageID[:8], sent[0].SessionID[:8], sent[0].Counter, sent[len(sent)-1].MessageID[:8], sent[len(sent)-1].SessionID[:8], sent[len(sent)-1].Counter,
		b.count("agent 0x7f", b.sent), b.count("agent 0x7f", b.frames))
	if len(sent) != 2 || sent[1].MessageID != sent[0].MessageID || sent[1].SessionID == sent[0].SessionID || b.count("agent 0x7f", b.frames) != 0 {
		t.Fatal("not the same message id once more under the new session")
	}
}

// A refusal the broker makes fatal, every time the agent arrives: the reconnects back off
// 1 s doubling to 60 s instead of once a second.
func TestAFatalRefusalBacksOffToSixtySeconds2wq(t *testing.T) {
	b := newStandIn(t)
	b.inject = func(role string, frame Frame) {
		if role == "agent" && frame.Type == FramePeerQuery {
			b.sendTo("agent", FrameError, ErrorPayload{Code: "malformed", Detail: "unexpected_frame", Fatal: true})
		}
	}
	agentID, deviceID, pairingID := newIdentity(t), newIdentity(t), make([]byte, 16)
	_, _ = rand.Read(pairingID)
	agent := NewClient(agentID, Pairing{PairingID: pairingID, DeviceKeyID: deviceID.KeyID(), DeviceSigning: deviceID.SigningPublic(), DeviceAgreement: deviceID.AgreementPublic()}, Dial(b.address), echo)
	steps := make(chan time.Duration, 16)
	agent.wait = func(step time.Duration) <-chan time.Time {
		steps <- step
		fired := make(chan time.Time, 1)
		fired <- time.Now()
		return fired
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = agent.Run(ctx) }()
	var got []time.Duration
	for len(got) < 8 {
		select {
		case step := <-steps:
			got = append(got, step)
		case <-time.After(10 * time.Second):
			t.Fatalf("steps so far %v", got)
		}
	}
	cancel()
	t.Logf("backoff steps: %v", got)
	if fmt.Sprint(got) != "[1s 2s 4s 8s 16s 32s 1m0s 1m0s]" {
		t.Fatalf("backoff steps %v", got)
	}
}

// CHECK 4, the O3 shape: the PC sends 3,000 units while the phone's link is cut and
// restored. Every unit arrives (or expires), the broker never refuses one as queue_full,
// and no client sends an ERROR frame.
func TestThreeThousandUnitsSurviveACutLink2wq(t *testing.T) {
	b := newStandIn(t)
	b.expiry = 20 * time.Second
	var mu sync.Mutex
	got := map[string]bool{}
	received := func(plaintext []byte) { mu.Lock(); got[string(plaintext)] = true; mu.Unlock() }
	agent, device := linkThrough(t, b, echo, received)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cut := make(chan struct{})
	go func() {
		for index := 0; index < 3000 && ctx.Err() == nil; index++ {
			if index == 1000 {
				close(cut)
			}
			// The application's part, not the client's: a unit is offered again once
			// a session is up, as the desktop resends by cursor after a reconnect.
			for ctx.Err() == nil {
				for agent.Status().State != "phone connected" && ctx.Err() == nil {
					time.Sleep(5 * time.Millisecond)
				}
				if agent.Deliver([]byte(fmt.Sprintf("unit %d", index))) == nil {
					break
				}
			}
		}
	}()
	<-cut
	b.cut("device")
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		b.mu.Lock()
		closed := len(b.events) > 0 && strings.HasPrefix(b.events[len(b.events)-1], "closed")
		b.mu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the device never saw its link end")
		}
	}
	time.Sleep(time.Second)
	if err := device.Connect(ctx, Dial(b.address), received, b.deviceEvent); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	for deadline := time.Now().Add(40 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		mu.Lock()
		count := len(got)
		mu.Unlock()
		if count == 3000 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of 3000 units arrived; queue_full %d, expired %d, agent %+v", count, b.count("agent 0x7f", b.sent), b.count("agent 0x27", b.sent), agent.Status())
		}
	}
	t.Logf("3000 units in %s after the restore; QUEUED %d, RESEND %d, expired %d, queue_full %d, ERROR from agent %d, from device %d",
		time.Since(started).Round(time.Millisecond), b.count("agent 0x22", b.sent), b.count("agent 0x26", b.sent), b.count("agent 0x27", b.sent),
		b.count("agent 0x7f", b.sent), b.count("agent 0x7f", b.frames), b.count("device 0x7f", b.frames))
	if b.count("agent 0x7f", b.sent) != 0 || b.count("agent 0x7f", b.frames) != 0 || b.count("device 0x7f", b.frames) != 0 {
		t.Fatal("an ERROR frame crossed the wire")
	}
}

// (c) The device client pairs in two steps: BeginPair returns the fingerprint the person
// compares, and only ConfirmPair commits; a scanned key that is not the PC's is refused.
func TestTheDeviceClientPairsInTwoSteps2wq(t *testing.T) {
	pairWith := func(t *testing.T, scanned func(agent Identity) []byte) (*DeviceClient, Identity, Identity, chan PairingOffer, chan Pairing, string, error) {
		b := newStandIn(t)
		agentID, deviceID := newIdentity(t), newIdentity(t)
		code, _, err := NewPairingCode()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		offers, paired := make(chan PairingOffer, 1), make(chan Pairing, 1)
		go func() {
			link, err := Dial(b.address)(ctx)
			if err != nil {
				return
			}
			receive := func() (Frame, error) {
				raw, err := link.Receive(ctx)
				if err != nil {
					return Frame{}, err
				}
				return Decode(raw)
			}
			if pairing, err := BeginPairing(link, agentID, code, receive, func(offer PairingOffer) bool { offers <- offer; return true }); err == nil {
				paired <- pairing
			}
		}()
		for deadline := time.Now().Add(5 * time.Second); b.pairingStarted() == nil; time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("the agent never began pairing")
			}
		}
		device := NewDeviceClient(deviceID, Pairing{})
		fingerprint, err := device.BeginPair(ctx, Dial(b.address), code, scanned(agentID))
		return device, agentID, deviceID, offers, paired, fingerprint, err
	}
	t.Run("confirmed", func(t *testing.T) {
		device, agentID, deviceID, offers, paired, fingerprint, err := pairWith(t, func(agent Identity) []byte { return agent.SigningPublic() })
		if err != nil {
			t.Fatal(err)
		}
		offer := <-offers
		if fingerprint != offer.Fingerprint || len(device.Pairing().PairingID) != 0 {
			t.Fatalf("fingerprint %q vs the PC's %q; paired before confirming: %x", fingerprint, offer.Fingerprint, device.Pairing().PairingID)
		}
		if err := device.ConfirmPair(context.Background()); err != nil {
			t.Fatal(err)
		}
		agentSide := <-paired
		if hexID(device.Pairing().DeviceKeyID) != hexID(agentID.KeyID()) || hexID(agentSide.DeviceKeyID) != hexID(deviceID.KeyID()) || hexID(device.Pairing().PairingID) != hexID(agentSide.PairingID) {
			t.Fatalf("device %+v, agent %+v", device.Pairing(), agentSide)
		}
		t.Logf("fingerprint shown between the steps: %s", fingerprint)
	})
	t.Run("a scanned key that is not the PC's", func(t *testing.T) {
		_, _, _, _, _, _, err := pairWith(t, func(Identity) []byte { return newIdentity(t).SigningPublic() })
		if err == nil || !strings.Contains(err.Error(), "scanned") {
			t.Fatalf("err %v", err)
		}
		t.Logf("refused: %v", err)
	})
}

// (c) Presence: the device asks after READY and is told when the PC comes and goes.
func TestTheDeviceClientFollowsThePC2wq(t *testing.T) {
	b, _, _ := linkThroughStandIn(t, echo, func([]byte) {})
	waitEvent := func(want string) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			b.mu.Lock()
			events := strings.Join(b.events, "; ")
			b.mu.Unlock()
			if strings.Contains(events, want) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("no %q in: %s", want, events)
			}
		}
	}
	waitEvent("peer connected")
	b.mu.Lock()
	b.events = nil
	b.mu.Unlock()
	b.cut("agent")
	waitEvent("peer gone")
	waitEvent("peer connected")
}

// (c) A message the broker never answers frees its slot at the broker's own expiry; one
// the broker reports expired frees it at once.
func TestTheDeviceClientExpiresWhatTheBrokerNeverAnswers2wq(t *testing.T) {
	b, _, device := linkThroughStandIn(t, echo, func([]byte) {})
	var ids []string
	b.mu.Lock()
	b.swallow = "device"
	b.inject = func(role string, frame Frame) {
		if role == "device" && frame.Type == FrameCiphertext {
			var ciphertext CiphertextFrame
			_ = DecodeInto(frame.Payload, &ciphertext)
			b.mu.Lock()
			ids = append(ids, ciphertext.MessageID)
			b.mu.Unlock()
		}
	}
	b.mu.Unlock()
	for range 3 {
		if err := device.Send(context.Background(), []byte("never answered")); err != nil {
			t.Fatal(err)
		}
	}
	waitCounters(t, device, Counters{Sent: 3, Unacknowledged: 3}, 5*time.Second)
	var first string
	for deadline := time.Now().Add(5 * time.Second); first == ""; time.Sleep(10 * time.Millisecond) {
		b.mu.Lock()
		if len(ids) == 3 {
			first = ids[0]
		}
		b.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("the broker never saw the three messages")
		}
	}
	b.sendTo("device", FrameExpired, IDOnlyFrame{PairingID: b.pairingHex(), MessageID: first})
	waitCounters(t, device, Counters{Sent: 3, Unacknowledged: 2}, 5*time.Second)
	device.mu.Lock()
	device.now = func() time.Time { return time.Now().Add(deliveryLifetime + time.Second) }
	device.mu.Unlock()
	b.sendTo("device", FramePing, map[string]string{"token": "expiry"})
	waitCounters(t, device, Counters{Sent: 3, Unacknowledged: 0}, 5*time.Second)
}
