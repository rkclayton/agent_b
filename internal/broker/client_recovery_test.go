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

// The broker asks the agent to RESEND a message it no longer holds, and asks again after
// every new session for as long as the intent lives.
func TestAnUnknownResendCostsOneLineNotALoop2wq(t *testing.T) {
	b := newStandIn(t)
	b.expiry, b.deadline = 30*time.Second, 10*time.Second // the real broker: 5 min, 90 s
	agent, _ := linkThrough(t, b, echo, func([]byte) {})
	id := randomHex(t)
	b.mu.Lock()
	b.intents[id] = &standInIntent{sender: "agent", queued: true, at: time.Now()}
	b.mu.Unlock()
	b.sendTo("agent", FrameResend, IDOnlyFrame{PairingID: b.pairingHex(), MessageID: id})
	time.Sleep(5 * time.Second)
	reconnects, errors := agent.Status().Reconnects, b.count("agent 0x7f", b.frames)
	b.mu.Lock()
	closes := strings.Join(b.closes, "; ")
	b.mu.Unlock()
	t.Logf("in 5 s: reconnects %d, ERROR frames from the agent %d, RESENDs sent %d; stand-in: %s", reconnects, errors, b.count("agent 0x26", b.sent), closes)
	if reconnects != 0 || errors != 0 {
		t.Fatalf("an unknown RESEND cost %d reconnects and %d ERROR frames", reconnects, errors)
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
