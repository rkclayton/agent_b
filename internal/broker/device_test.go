package broker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// Item 2wo: a stand-in broker that does what the real one does to a burst. Over 240
// frames in a connection's second it holds the frame and reads nothing more until the
// second turns; it forwards each frame from the sender's reader to the recipient, giving
// up a write after 5 s; and it closes a connection whose next read does not come in time.
//
// Item 2wq adds the real broker's refusal and delivery rules (agent_b_server
// internal/broker/server.go): an ERROR from a client is answered with fatal
// ERROR malformed/unexpected_frame and a close (:991, :1867); a CIPHERTEXT for a
// recipient that is absent or has no session yet becomes a delivery intent answered
// QUEUED, at most 64 of them, the next refused with nonfatal ERROR queue_full (:45,
// :1683); a new session is followed by RESEND of every intent and every relayed message
// not yet acknowledged; an intent expires with DELIVERY_EXPIRED (:47, shortened here);
// PEER goes only to an endpoint that sent PEER_QUERY, on the other's AUTH and close; and
// pairing is v0's PAIR_BEGIN, PAIR_WAITING, PAIR_PEER, PAIR_CONFIRM, PAIR_COMPLETE.
type standInBroker struct {
	deadline   time.Duration
	expiry     time.Duration
	address    string
	mu         sync.Mutex
	ends       map[string]*standInEnd // by HELLO role
	subscribed map[string]bool
	sessionUp  bool
	intents    map[string]*standInIntent // undelivered, by message id
	pairing    map[string]*standInPair   // by role, while pairing
	pairingID  string
	forwarded  map[string]time.Time // ciphertext sent to the agent, by message id
	frames     map[string]int       // "<role> 0x<type>" for every frame a client sent
	sent       map[string]int       // "<role> 0x<type>" for every frame the stand-in sent
	inject     func(role string, frame Frame)
	swallow    string // a role whose CIPHERTEXT the broker loses without a word
	worstAck   time.Duration
	acks       int
	holds      int
	closes     []string
	events     []string // the device's event lines
}

type standInEnd struct {
	ws      *websocket.Conn
	writeMu sync.Mutex
}

type standInIntent struct {
	sender string
	queued bool
	at     time.Time
}

type standInPair struct {
	end       *standInEnd
	signing   string
	agreement string
	confirmed bool
}

func newStandIn(t *testing.T) *standInBroker {
	b := &standInBroker{deadline: 3 * time.Second, expiry: 5 * time.Second, ends: map[string]*standInEnd{}, subscribed: map[string]bool{}, intents: map[string]*standInIntent{},
		pairing: map[string]*standInPair{}, forwarded: map[string]time.Time{}, frames: map[string]int{}, sent: map[string]int{}}
	server := httptest.NewServer(websocket.Handler(b.serve))
	t.Cleanup(server.Close)
	b.address = "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	go func() {
		for tick := time.NewTicker(50 * time.Millisecond); ctx.Err() == nil; <-tick.C {
			b.expire()
		}
	}()
	return b
}

func (b *standInBroker) close(role, reason string, ws *websocket.Conn) {
	b.mu.Lock()
	b.closes = append(b.closes, "closed role="+role+" reason="+reason)
	b.mu.Unlock()
	_ = ws.Close()
}

func (b *standInBroker) write(role string, end *standInEnd, raw []byte) {
	end.writeMu.Lock()
	defer end.writeMu.Unlock()
	_ = end.ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := websocket.Message.Send(end.ws, raw); err != nil {
		b.close(role, "write timeout", end.ws)
	}
}

func (b *standInBroker) send(role string, end *standInEnd, kind byte, payload any) {
	raw, _ := Encode(kind, payload)
	b.mu.Lock()
	b.sent[fmt.Sprintf("%s 0x%02x", role, kind)]++
	b.mu.Unlock()
	b.write(role, end, raw)
}

// sendTo sends to whichever connection now holds role, if any.
func (b *standInBroker) sendTo(role string, kind byte, payload any) {
	b.mu.Lock()
	end := b.ends[role]
	b.mu.Unlock()
	if end != nil {
		b.send(role, end, kind, payload)
	}
}

func (b *standInBroker) other(role string) (string, *standInEnd) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return otherRole(role), b.ends[otherRole(role)]
}

func otherRole(role string) string {
	return map[string]string{"agent": "device", "device": "agent"}[role]
}

// cut closes role's connection from the broker's side, as a dropped network does.
func (b *standInBroker) cut(role string) {
	b.mu.Lock()
	end := b.ends[role]
	b.mu.Unlock()
	if end != nil {
		_ = end.ws.Close()
	}
}

func (b *standInBroker) count(key string, in map[string]int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return in[key]
}

func (b *standInBroker) deviceEvent(line string) {
	b.mu.Lock()
	b.events = append(b.events, line)
	b.mu.Unlock()
}

func (b *standInBroker) expire() {
	b.mu.Lock()
	senders := map[string]string{}
	for id, intent := range b.intents {
		if time.Since(intent.at) >= b.expiry {
			senders[id] = intent.sender
			delete(b.intents, id)
		}
	}
	pairingID := b.pairingID
	b.mu.Unlock()
	for id, sender := range senders {
		b.sendTo(sender, FrameExpired, IDOnlyFrame{PairingID: pairingID, MessageID: id})
	}
}

// presence tells role's peer, when it subscribed, that role came or went.
func (b *standInBroker) presence(role string, connected bool) {
	b.mu.Lock()
	peer := otherRole(role)
	subscribed, pairingID := b.subscribed[peer], b.pairingID
	b.mu.Unlock()
	if subscribed {
		b.sendTo(peer, FramePeer, peerPayload{PairingID: pairingID, Connected: connected})
	}
}

func (b *standInBroker) pair(end *standInEnd, frame Frame) {
	if frame.Type == FramePairBegin {
		var begin struct {
			Role      string `json:"role"`
			Signing   string `json:"ed25519_public"`
			Agreement string `json:"x25519_public"`
			CodeHash  string `json:"code_hash"`
			Code      string `json:"code"`
		}
		_ = DecodeInto(frame.Payload, &begin)
		b.mu.Lock()
		b.pairing[begin.Role] = &standInPair{end: end, signing: begin.Signing, agreement: begin.Agreement}
		agent, device := b.pairing["agent"], b.pairing["device"]
		b.mu.Unlock()
		if begin.Role == "agent" {
			b.send("agent", end, FramePairWaiting, pairWaitingPayload{ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339)})
			return
		}
		if _, err := DecodePairingCode(begin.Code); err != nil || agent == nil {
			b.send("device", end, FrameError, ErrorPayload{Code: "malformed", Detail: "no_pending_for_code"})
			return
		}
		id := make([]byte, 16)
		_, _ = rand.Read(id)
		b.mu.Lock()
		b.pairingID = hexID(id)
		b.mu.Unlock()
		for _, pair := range [][2]*standInPair{{agent, device}, {device, agent}} {
			signing, _ := base64.RawURLEncoding.DecodeString(pair[1].signing)
			agreement, _ := base64.RawURLEncoding.DecodeString(pair[1].agreement)
			b.send("pairing", pair[0].end, FramePairPeer, PairPeerPayload{PairingID: hexID(id), PeerKeyID: hexID(KeyIDFor(signing, agreement)), Ed25519: pair[1].signing, X25519: pair[1].agreement})
		}
		return
	}
	b.mu.Lock()
	var confirmed []*standInPair
	for _, pair := range b.pairing {
		if pair.end == end {
			pair.confirmed = true
		}
		if pair.confirmed {
			confirmed = append(confirmed, pair)
		}
	}
	pairingID := b.pairingID
	b.mu.Unlock()
	if len(confirmed) == 2 {
		for _, pair := range confirmed {
			b.send("pairing", pair.end, FramePairComplete, pairCompletePayload{PairingID: pairingID})
		}
	}
}

func (b *standInBroker) serve(ws *websocket.Conn) {
	end, role := &standInEnd{ws: ws}, "unauthenticated"
	defer func() {
		b.mu.Lock()
		current := b.ends[role] == end
		if current {
			delete(b.ends, role)
			b.sessionUp = false
		}
		b.mu.Unlock()
		if current {
			b.presence(role, false)
		}
	}()
	window, count := time.Now(), 0
	for {
		_ = ws.SetReadDeadline(time.Now().Add(b.deadline))
		var raw []byte
		if err := websocket.Message.Receive(ws, &raw); err != nil {
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() {
				b.close(role, "read deadline", ws)
			}
			return
		}
		if count++; time.Since(window) >= time.Second {
			window, count = time.Now(), 1
		} else if count > 240 {
			b.mu.Lock()
			b.holds++
			b.mu.Unlock()
			time.Sleep(time.Until(window.Add(time.Second)))
			window, count = time.Now(), 1
		}
		frame, err := Decode(raw)
		if err != nil {
			b.close(role, "malformed", ws)
			return
		}
		b.mu.Lock()
		b.frames[fmt.Sprintf("%s 0x%02x", role, frame.Type)]++
		inject := b.inject
		b.mu.Unlock()
		if inject != nil {
			inject(role, frame)
		}
		switch frame.Type {
		case FrameHello:
			var hello helloPayload
			_ = DecodeInto(frame.Payload, &hello)
			role = hello.Role
			b.send(role, end, FrameChallenge, challengePayload{ConnectionID: hello.ConnectionID, Challenge: "AAAA"})
		case FrameAuth:
			b.send(role, end, FrameReady, readyPayload{ConnectionID: "stand-in", ServerTime: time.Now().UTC().Format(time.RFC3339)})
			b.mu.Lock()
			if previous := b.ends[role]; previous != nil {
				_ = previous.ws.Close()
			}
			b.ends[role], b.sessionUp = end, false
			b.mu.Unlock()
			b.presence(role, true)
		case FramePeerQuery:
			var query peerQueryPayload
			_ = DecodeInto(frame.Payload, &query)
			b.mu.Lock()
			b.pairingID, b.subscribed[role] = query.PairingID, true
			present := b.ends[otherRole(role)] != nil
			b.mu.Unlock()
			b.send(role, end, FramePeer, peerPayload{PairingID: query.PairingID, Connected: present})
		case FramePairBegin, FramePairConfirm:
			b.pair(end, frame)
		case FrameError:
			b.send(role, end, FrameError, ErrorPayload{Code: "malformed", Detail: "unexpected_frame", Fatal: true})
			b.close(role, "unexpected_frame", ws)
			return
		case FrameCiphertext:
			var ciphertext CiphertextFrame
			_ = DecodeInto(frame.Payload, &ciphertext)
			name, to := b.other(role)
			b.mu.Lock()
			if b.swallow == role {
				b.mu.Unlock()
				continue
			}
			if to == nil || !b.sessionUp {
				queued := 0
				for _, intent := range b.intents {
					if intent.queued {
						queued++
					}
				}
				full := queued >= 64
				if !full {
					b.intents[ciphertext.MessageID] = &standInIntent{sender: role, queued: true, at: time.Now()}
				}
				b.mu.Unlock()
				if full {
					b.send(role, end, FrameError, ErrorPayload{Code: "queue_full", Detail: "delivery intent queue full"})
				} else {
					b.send(role, end, FrameQueued, IDOnlyFrame{PairingID: ciphertext.PairingID, MessageID: ciphertext.MessageID, ExpiresAt: time.Now().Add(b.expiry).UTC().Format(time.RFC3339)})
				}
				continue
			}
			b.intents[ciphertext.MessageID] = &standInIntent{sender: role, at: time.Now()}
			if role == "device" {
				b.forwarded[ciphertext.MessageID] = time.Now()
			}
			b.mu.Unlock()
			b.write(name, to, raw)
		case FrameAck:
			var ack IDOnlyFrame
			_ = DecodeInto(frame.Payload, &ack)
			b.mu.Lock()
			delete(b.intents, ack.MessageID)
			if sent, ok := b.forwarded[ack.MessageID]; ok && role == "agent" {
				b.acks, b.worstAck = b.acks+1, max(b.worstAck, time.Since(sent))
				delete(b.forwarded, ack.MessageID)
			}
			b.mu.Unlock()
			if name, to := b.other(role); to != nil {
				b.write(name, to, raw)
			}
		default:
			name, to := b.other(role)
			if to == nil {
				continue
			}
			b.write(name, to, raw)
			if frame.Type != FrameSessionReady {
				continue
			}
			b.mu.Lock()
			b.sessionUp = true
			resend := map[string]string{}
			for id, intent := range b.intents {
				resend[id], intent.queued = intent.sender, false
			}
			pairingID := b.pairingID
			b.mu.Unlock()
			for id, sender := range resend {
				b.sendTo(sender, FrameResend, IDOnlyFrame{PairingID: pairingID, MessageID: id})
			}
		}
	}
}

func newIdentity(t *testing.T) Identity {
	seed, agreement := make([]byte, 32), make([]byte, 32)
	_, _ = rand.Read(seed)
	_, _ = rand.Read(agreement)
	return Identity{SigningSeed: seed, Agreement: agreement}
}

func echo(_, plaintext []byte) []byte { return append([]byte("echo: "), plaintext...) }

// linkThroughStandIn starts a stand-in broker, the PC's agent Client answering through
// handle and a paired DeviceClient, and returns once their session is up; onSession, when
// set, runs inside the device's "session" event.
func linkThroughStandIn(t *testing.T, handle func(_, plaintext []byte) []byte, received func([]byte), onSession ...func(*DeviceClient)) (*standInBroker, *Client, *DeviceClient) {
	t.Helper()
	b := newStandIn(t)
	agent, device := linkThrough(t, b, handle, received, onSession...)
	return b, agent, device
}

func linkThrough(t *testing.T, b *standInBroker, handle func(_, plaintext []byte) []byte, received func([]byte), onSession ...func(*DeviceClient)) (*Client, *DeviceClient) {
	t.Helper()
	address := b.address
	agentID, deviceID, pairingID := newIdentity(t), newIdentity(t), make([]byte, 16)
	_, _ = rand.Read(pairingID)
	agent := NewClient(agentID, Pairing{PairingID: pairingID, DeviceKeyID: deviceID.KeyID(), DeviceSigning: deviceID.SigningPublic(), DeviceAgreement: deviceID.AgreementPublic()},
		Dial(address), handle)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = agent.Run(ctx) }()
	for deadline := time.Now().Add(5 * time.Second); agent.Status().State != "holding"; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the agent client did not reach holding: %+v", agent.Status())
		}
	}
	device := NewDeviceClient(deviceID, Pairing{PairingID: pairingID, DeviceKeyID: agentID.KeyID(), DeviceSigning: agentID.SigningPublic(), DeviceAgreement: agentID.AgreementPublic()})
	ready := make(chan struct{}, 1)
	if err := device.Connect(ctx, Dial(address), received, func(line string) {
		b.deviceEvent(line)
		if strings.HasPrefix(line, "session ") {
			for _, hook := range onSession {
				hook(device)
			}
			ready <- struct{}{}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(device.Close)
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatalf("no session: %+v", agent.Status())
	}
	return agent, device
}

func waitCounters(t *testing.T, device *DeviceClient, want Counters, within time.Duration) {
	t.Helper()
	for deadline := time.Now().Add(within); device.Counters() != want; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("counters %+v, want %+v", device.Counters(), want)
		}
	}
}

// CHECK 3: the O1 shape, one message from the device answered by the PC's own client.
func TestDeviceClientRoundTripsThroughTheAgentClient2wo(t *testing.T) {
	replies := make(chan string, 1)
	_, _, device := linkThroughStandIn(t, echo, func(plaintext []byte) { replies <- string(plaintext) })
	if err := device.Send(context.Background(), []byte("hello from the phone")); err != nil {
		t.Fatal(err)
	}
	select {
	case reply := <-replies:
		if reply != "echo: hello from the phone" {
			t.Fatalf("reply %q", reply)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no reply")
	}
	waitCounters(t, device, Counters{Sent: 1, Received: 1}, 5*time.Second)
	t.Logf("counters sent/received/resent/unacknowledged: %+v", device.Counters())
}

// CHECK 2, (c): the O1 burst. 256 messages from the device, each answered by the PC, must
// all come back with no connection closed, and the PC's ACK for each must reach the broker
// within 100 ms of the broker forwarding the message. At 4 KiB (O1's size) an unpaced link
// only slows; at 64 KiB the buffers fill and it wedges until a write times out.
func TestABurstNeitherStallsNorClosesTheLink2wo(t *testing.T) {
	for _, size := range []int{4 << 10, 64 << 10} {
		t.Run(strconv.Itoa(size), func(t *testing.T) { burst(t, size) })
	}
}

func burst(t *testing.T, size int) {
	var mu sync.Mutex
	answered := 0
	b, _, device := linkThroughStandIn(t, echo, func([]byte) { mu.Lock(); answered++; mu.Unlock() })
	payload := make([]byte, size)
	started := time.Now()
	for index := 0; index < 256; index++ {
		if err := device.Send(context.Background(), payload); err != nil {
			b.mu.Lock()
			defer b.mu.Unlock()
			t.Fatalf("send %d: %v; stand-in: %s", index, err, strings.Join(b.closes, "; "))
		}
	}
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		mu.Lock()
		done := answered
		mu.Unlock()
		b.mu.Lock()
		closes, acks, worst, holds := strings.Join(b.closes, "; "), b.acks, b.worstAck, b.holds
		b.mu.Unlock()
		if closes != "" || time.Now().After(deadline) {
			t.Fatalf("answered %d of 256, acks %d, holds %d; stand-in: %s", done, acks, holds, closes)
		}
		if done == 256 && acks == 256 {
			t.Logf("256 answered in %s, broker holds %d, worst ACK %s", time.Since(started).Round(time.Millisecond), holds, worst.Round(time.Millisecond))
			// Under -race the PC's reader opens and answers slower than the 200 frames a
			// second the device is paced to, so the ACK then measures that backlog, not a
			// write it waited behind: CI measured 123 ms and 1.5 s at 64 KiB (item 2wp).
			if worst > 100*time.Millisecond && !raceDetector {
				t.Fatalf("worst ACK %s, over 100 ms", worst)
			}
			break
		}
	}
	waitCounters(t, device, Counters{Sent: 256, Received: 256}, 5*time.Second)
}

// Item 2wp: a handler that replies from inside received while all 256 pending slots are
// taken. The agent holds its reader on the first message until the device's pending set
// is full, then answers it; the reply's Send needs the ACKs that follow on the device's
// reader, so it completes only if received does not run there.
func TestASendFromInsideReceivedCompletes2wp(t *testing.T) {
	release, replied := make(chan struct{}), make(chan error, 1)
	var device *DeviceClient
	handle := func(_, plaintext []byte) []byte {
		if string(plaintext) != "first" {
			return nil
		}
		<-release
		return []byte("answer me")
	}
	_, _, device = linkThroughStandIn(t, handle, func(plaintext []byte) {
		if string(plaintext) == "answer me" {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			replied <- device.Send(ctx, []byte("the reply"))
		}
	})
	for index := 0; index <= deliveryIntentLimit; index++ {
		if err := device.Send(context.Background(), []byte(map[bool]string{true: "first", false: "fill"}[index == 0])); err != nil {
			t.Fatal(err)
		}
	}
	if got := device.Counters().Unacknowledged; got != deliveryIntentLimit {
		t.Fatalf("unacknowledged %d, want %d", got, deliveryIntentLimit)
	}
	close(release)
	select {
	case err := <-replied:
		if err != nil {
			t.Fatalf("Send from inside received: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Send from inside received did not return")
	}
	waitCounters(t, device, Counters{Sent: deliveryIntentLimit + 2, Received: 1}, 10*time.Second)
}

// Item 2wp: a Send the moment the session exists must reach the agent after SESSION_READY.
// The race detector's scheduling found it: the burst began on the "session" event while
// SESSION_READY was still unsent, and the agent dropped the session on the CIPHERTEXT.
func TestASendOnTheSessionEventFollowsSessionReady2wp(t *testing.T) {
	replies := make(chan string, 1)
	b, agent, _ := linkThroughStandIn(t, echo, func(plaintext []byte) { replies <- string(plaintext) }, func(device *DeviceClient) {
		if err := device.Send(context.Background(), []byte("at once")); err != nil {
			t.Error(err)
		}
	})
	select {
	case reply := <-replies:
		if reply != "echo: at once" {
			t.Fatalf("reply %q", reply)
		}
	case <-time.After(5 * time.Second):
		b.mu.Lock()
		defer b.mu.Unlock()
		t.Fatalf("no reply; agent %+v; stand-in: %s", agent.Status(), strings.Join(b.closes, "; "))
	}
}
