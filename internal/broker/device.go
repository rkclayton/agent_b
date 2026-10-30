package broker

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// THE DEVICE ROLE, beside the agent's Client (item 2wo). A phone's core is this and a
// binding: it pairs with a code, authenticates as "device", answers the agent's session
// handshake as the responder, and then sends and receives ciphertext with ACK and RESEND.
// Nothing outside this package builds a frame.

// maxDeviceInbox bounds the opened messages waiting for the received handler. The
// messages a device retains for RESEND are bounded at deliveryIntentLimit, the broker's
// intent bound, and Send waits for room (item 2wq).
const maxDeviceInbox = 256

// deliveryLifetime is the broker's: an intent or relayed message unacknowledged this long
// is dropped (agent_b_server internal/broker/server.go:47). A retained message older than
// this frees its slot even if the broker never says DELIVERY_EXPIRED (item 2wq).
const deliveryLifetime = 5 * time.Minute

// Counters are application frames: sent, received, re-encrypted on RESEND, and sent but
// not yet acknowledged.
type Counters struct{ Sent, Received, Resent, Unacknowledged int64 }

// DeviceClient is the phone's end of one pairing.
type DeviceClient struct {
	identity Identity
	agent    Pairing // the AGENT's keys, in the fields Pairing names for the far end
	slots    chan struct{}

	mu      sync.Mutex
	sendMu  sync.Mutex // keeps sealed frames on the wire in counter order
	now     func() time.Time
	link    Transport
	stop    context.CancelFunc
	session *Session
	counter uint64
	pending map[string]devicePending
	counts  Counters
	pairing *devicePairing // between BeginPair and ConfirmPair
}

type devicePending struct {
	plaintext []byte
	sent      time.Time
	session   string // the one it was last sealed under
}

type devicePairing struct {
	link   Transport
	peerID string
	agent  Pairing
}

// NewDeviceClient builds one from a device identity and, when it is already paired, the
// pairing Pair returned last time; keeping both is the caller's business.
func NewDeviceClient(identity Identity, agent Pairing) *DeviceClient {
	return &DeviceClient{identity: identity, agent: agent, slots: make(chan struct{}, deliveryIntentLimit), now: time.Now, pending: map[string]devicePending{}}
}

// Pairing is what Pair learned: the pairing id and the agent's key id and public keys.
func (d *DeviceClient) Pairing() Pairing {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.agent
}

func deviceRead(ctx context.Context, t Transport, want byte, into any) error {
	raw, err := t.Receive(ctx)
	if err != nil {
		return err
	}
	frame, err := Decode(raw)
	if err != nil {
		return err
	}
	if frame.Type == FrameError {
		var problem ErrorPayload
		_ = DecodeInto(frame.Payload, &problem)
		return &Problem{Code: problem.Code, Detail: problem.Detail, Fatal: problem.Fatal}
	}
	if frame.Type != want {
		return fmt.Errorf("broker: expected frame 0x%02x, got 0x%02x", want, frame.Type)
	}
	return DecodeInto(frame.Payload, into)
}

func deviceSend(t Transport, kind byte, payload any) error {
	frame, err := Encode(kind, payload)
	if err != nil {
		return err
	}
	return t.Send(frame)
}

// Pair is BeginPair and ConfirmPair with no comparison between them.
func (d *DeviceClient) Pair(ctx context.Context, dial Dialer, code string) (string, error) {
	fingerprint, err := d.BeginPair(ctx, dial, code, nil)
	if err == nil {
		err = d.ConfirmPair(ctx)
	}
	return fingerprint, err
}

// BeginPair sends the agent's code as the device's PAIR_BEGIN and returns the fingerprint
// the person compares with the one the agent shows; nothing is paired until ConfirmPair.
// agentSigning, when set, is the key a scanned pairing link carried, and a PC whose key
// differs is refused (item 2wq).
func (d *DeviceClient) BeginPair(ctx context.Context, dial Dialer, code string, agentSigning []byte) (string, error) {
	d.endPairing()
	t, err := dial(ctx)
	if err != nil {
		return "", err
	}
	b64, id := base64.RawURLEncoding, d.identity
	if err := deviceSend(t, FramePairBegin, map[string]string{"role": "device", "ed25519_public": b64.EncodeToString(id.SigningPublic()), "x25519_public": b64.EncodeToString(id.AgreementPublic()), "code": code}); err != nil {
		return "", err
	}
	var peer PairPeerPayload
	if err := deviceRead(ctx, t, FramePairPeer, &peer); err != nil {
		return "", err
	}
	pairingID, _ := hex.DecodeString(peer.PairingID)
	signing, _ := b64.DecodeString(peer.Ed25519)
	agreement, _ := b64.DecodeString(peer.X25519)
	keyID := KeyIDFor(signing, agreement)
	if hexID(keyID) != peer.PeerKeyID || len(pairingID) != 16 {
		_ = t.Close(1000, "refused")
		return "", errors.New("pairing: the peer key id does not match the peer keys")
	}
	if agentSigning != nil && !bytes.Equal(agentSigning, signing) {
		_ = t.Close(1000, "refused")
		return "", errors.New("pairing: the PC's key is not the one scanned")
	}
	d.mu.Lock()
	d.pairing = &devicePairing{link: t, peerID: peer.PairingID, agent: Pairing{PairingID: pairingID, DeviceKeyID: keyID, DeviceSigning: signing, DeviceAgreement: agreement}}
	d.mu.Unlock()
	return Fingerprint(signing, agreement, id.SigningPublic(), id.AgreementPublic()), nil
}

// ConfirmPair sends PAIR_CONFIRM once the person has said the fingerprints match, and
// keeps the pairing when the broker completes it.
func (d *DeviceClient) ConfirmPair(ctx context.Context) error {
	d.mu.Lock()
	p := d.pairing
	d.mu.Unlock()
	if p == nil {
		return errors.New("pairing: nothing to confirm")
	}
	defer d.endPairing()
	id, a := d.identity, p.agent
	transcript := TranscriptHash(a.PairingID, a.DeviceSigning, a.DeviceAgreement, id.SigningPublic(), id.AgreementPublic())
	signature := ed25519.Sign(ed25519.NewKeyFromSeed(id.SigningSeed), transcript)
	if err := deviceSend(p.link, FramePairConfirm, pairConfirmPayload{PairingID: p.peerID, TranscriptHash: hexID(transcript), Signature: base64.RawURLEncoding.EncodeToString(signature)}); err != nil {
		return err
	}
	var complete pairCompletePayload
	if err := deviceRead(ctx, p.link, FramePairComplete, &complete); err != nil {
		return err
	}
	d.mu.Lock()
	d.agent = a
	d.mu.Unlock()
	return nil
}

// endPairing closes a pairing begun and not confirmed.
func (d *DeviceClient) endPairing() {
	d.mu.Lock()
	p := d.pairing
	d.pairing = nil
	d.mu.Unlock()
	if p != nil {
		_ = p.link.Close(1000, "paired")
	}
}

// Connect authenticates as the device and serves the link on its own goroutine until
// Close or the context ends. received is given each opened message, in order, on a
// goroutine of its own, so it may call Send (item 2wp); event gets one line per session
// change, "peer connected" or "peer gone" as the PC comes and goes (item 2wq), and
// "closed: <why>" when the link ends on its own.
func (d *DeviceClient) Connect(ctx context.Context, dial Dialer, received func([]byte), event func(string)) error {
	if len(d.Pairing().PairingID) != 16 {
		return errors.New("broker: not paired")
	}
	ctx, stop := context.WithCancel(ctx)
	t, err := dial(ctx)
	if err == nil {
		err = d.authenticate(ctx, t)
		if err == nil {
			err = deviceSend(t, FramePeerQuery, peerQueryPayload{PairingID: hexID(d.Pairing().PairingID)})
		}
		if err != nil {
			_ = t.Close(1000, "auth failed")
		}
	}
	if err != nil {
		stop()
		return err
	}
	d.mu.Lock()
	d.link, d.stop = t, stop
	d.mu.Unlock()
	event("ready")
	inbox := make(chan []byte, maxDeviceInbox)
	go func() {
		for plaintext := range inbox {
			received(plaintext)
		}
	}()
	go func() {
		err := d.serve(ctx, t, inbox, event)
		close(inbox)
		_ = t.Close(1000, "done")
		d.mu.Lock()
		if d.link == t { // a Connect that came after this link ended keeps its own
			d.link, d.session = nil, nil
		}
		d.mu.Unlock()
		if ctx.Err() == nil {
			event("closed: " + err.Error())
		}
	}()
	return nil
}

func (d *DeviceClient) authenticate(ctx context.Context, t Transport) error {
	b64, id := base64.RawURLEncoding, d.identity
	connectionID := make([]byte, 16)
	if _, err := rand.Read(connectionID); err != nil {
		return err
	}
	var challenge challengePayload
	var ready readyPayload
	err := deviceSend(t, FrameHello, helloPayload{Role: "device", ConnectionID: hexID(connectionID), KeyID: hexID(id.KeyID()), Ed25519: b64.EncodeToString(id.SigningPublic()), X25519: b64.EncodeToString(id.AgreementPublic())})
	if err == nil {
		err = deviceRead(ctx, t, FrameChallenge, &challenge)
	}
	if err == nil {
		raw, _ := b64.DecodeString(challenge.Challenge)
		err = deviceSend(t, FrameAuth, authPayload{ConnectionID: challenge.ConnectionID, Signature: b64.EncodeToString(SignAuth(id, "device", connectionID, raw))})
	}
	if err == nil {
		err = deviceRead(ctx, t, FrameReady, &ready)
	}
	return err
}

func (d *DeviceClient) serve(ctx context.Context, t Transport, inbox chan<- []byte, event func(string)) error {
	b64, p, me := base64.RawURLEncoding, d.Pairing(), d.identity.KeyID()
	var opening *Session
	var openedAt time.Time
	for {
		raw, err := t.Receive(ctx)
		if err != nil {
			return err
		}
		frame, err := Decode(raw)
		if err != nil {
			return err
		}
		switch frame.Type {
		case FrameSessionInit, FrameSessionFinish:
			var in SessionFrame
			if err := DecodeInto(frame.Payload, &in); err != nil {
				return err
			}
			out, kind := SessionFrame{PairingID: in.PairingID, HandshakeID: in.HandshakeID, SenderKeyID: hexID(me), RecipientID: in.SenderKeyID}, byte(FrameSessionResp)
			if frame.Type == FrameSessionInit {
				handshake, _ := hex.DecodeString(in.HandshakeID)
				ephemeral := make([]byte, 32)
				if _, err := rand.Read(ephemeral); err != nil {
					return err
				}
				m1, _ := b64.DecodeString(in.Noise)
				signature, _ := b64.DecodeString(in.Signature)
				var m2 []byte
				openedAt = d.now()
				if opening, err = NewResponder(d.identity, SessionPrologue(p.PairingID, handshake, p.DeviceKeyID, me), ephemeral); err == nil {
					if err = opening.ReadInit(m1, p.DeviceSigning, signature); err == nil {
						m2, err = opening.WriteResponse()
					}
				}
				if err != nil {
					return err
				}
				out.Noise, out.Signature = b64.EncodeToString(m2), b64.EncodeToString(opening.ConfirmSignature())
			} else {
				if opening == nil {
					return errors.New("broker: SESSION_FINISH without SESSION_INIT")
				}
				finish, _ := b64.DecodeString(in.Confirmation)
				if err := opening.OpenFinish(finish, p.DeviceSigning); err != nil {
					return err
				}
				sealed, err := opening.SealReady()
				if err != nil {
					return err
				}
				// SESSION_READY is queued before the session is published, so no Send can
				// put a CIPHERTEXT ahead of it (item 2wp).
				out.SessionID, out.Confirmation = opening.SessionID(), b64.EncodeToString(sealed)
				if err := deviceSend(t, FrameSessionReady, out); err != nil {
					return err
				}
				d.mu.Lock()
				d.session, d.counter = opening, 1 // the sealed ready was counter 0
				d.mu.Unlock()
				go d.resendStale(ctx, t, opening)
				opening = nil
				event("session " + out.SessionID[:8])
				continue
			}
			if err := deviceSend(t, kind, out); err != nil {
				return err
			}
		case FrameCiphertext:
			var in CiphertextFrame
			if err := DecodeInto(frame.Payload, &in); err != nil {
				return err
			}
			d.mu.Lock()
			s := d.session
			d.mu.Unlock()
			if s == nil || in.SessionID != s.SessionID() {
				return fmt.Errorf("broker: wrong_session %s", in.SessionID)
			}
			sessionID, _ := hex.DecodeString(in.SessionID)
			messageID, _ := hex.DecodeString(in.MessageID)
			sealed, _ := b64.DecodeString(in.Ciphertext)
			plaintext, err := s.OpenMessage(TransportAAD(p.PairingID, sessionID, in.Epoch, DirectionAgentToDevice, p.DeviceKeyID, me, messageID, in.Counter), sealed)
			if err != nil {
				return err
			}
			// The reader never waits on the handler: a message that finds the queue
			// full is not acknowledged, so the broker keeps it in flight, and the link
			// ends saying so.
			select {
			case inbox <- plaintext:
			default:
				return fmt.Errorf("broker: %d received messages not yet handled", maxDeviceInbox)
			}
			if err := deviceSend(t, FrameAck, IDOnlyFrame{PairingID: in.PairingID, SessionID: in.SessionID, MessageID: in.MessageID}); err != nil {
				return err
			}
			d.mu.Lock()
			d.counts.Received++
			d.mu.Unlock()
		case FrameAck, FrameExpired, FrameResend, FrameQueued:
			var in IDOnlyFrame
			if err := DecodeInto(frame.Payload, &in); err != nil {
				return err
			}
			d.mu.Lock()
			retained, held := d.pending[in.MessageID]
			if held && (frame.Type == FrameAck || frame.Type == FrameExpired) {
				delete(d.pending, in.MessageID)
				<-d.slots
			}
			d.mu.Unlock()
			if frame.Type == FrameResend && held {
				messageID, _ := hex.DecodeString(in.MessageID)
				if err := d.seal(t, messageID, retained.plaintext, true); err != nil {
					return err
				}
			} else if frame.Type == FrameResend {
				// Declined once, as the document says (item 2wr).
				if err := deviceSend(t, FrameResendDeclined, IDOnlyFrame{PairingID: hexID(p.PairingID), MessageID: in.MessageID}); err != nil {
					return err
				}
			}
		case FramePeer:
			var peer peerPayload
			if err := DecodeInto(frame.Payload, &peer); err != nil {
				return err
			}
			event(map[bool]string{true: "peer connected", false: "peer gone"}[peer.Connected])
		case FramePing:
			var ping struct {
				Token string `json:"token"`
			}
			if err := DecodeInto(frame.Payload, &ping); err != nil {
				return err
			}
			if err := deviceSend(t, FramePong, ping); err != nil {
				return err
			}
			d.mu.Lock()
			d.expireLocked()
			d.mu.Unlock()
			// The device never waits on a handshake, but an opening the agent left is
			// dropped at the agent's bound (item 2wr).
			if opening != nil && d.now().Sub(openedAt) >= handshakeLimit {
				opening = nil
				event("handshake abandoned")
			}
		case FrameError:
			var in ErrorPayload
			_ = DecodeInto(frame.Payload, &in)
			event("error " + in.Code + " " + in.Detail)
			if in.Fatal {
				return &Problem{Code: in.Code, Detail: in.Detail, Fatal: true}
			}
		default:
			return fmt.Errorf("broker: unexpected frame 0x%02x", frame.Type)
		}
	}
}

// RegisterPush registers this device's own push token with the broker (item 2ww), on
// the live link after READY: "apns" takes the 64-hex token and sends no provider, as
// the iPhone's registration does; "fcm" takes 1-512 of [A-Za-z0-9_:-], as the broker
// checks it. It is sent once. A refusal arrives as an "error" event; sending again is
// the caller's choice.
func (d *DeviceClient) RegisterPush(provider, token string) error {
	payload := pushRegisterPayload{PairingID: hexID(d.Pairing().PairingID), DeviceToken: token}
	switch provider {
	case "apns":
		if _, err := hex.DecodeString(token); err != nil || len(token) != 64 {
			return errors.New("push register: an APNs token is 64 hex characters")
		}
		payload.DeviceToken = strings.ToLower(token)
	case "fcm":
		if !validFCMPushIdentifier(token) {
			return errors.New("push register: an FCM token is 1-512 of A-Z a-z 0-9 _ : -")
		}
		payload.Provider = provider
	default:
		return fmt.Errorf("push register: provider %q is not apns or fcm", provider)
	}
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	d.mu.Lock()
	t := d.link
	d.mu.Unlock()
	if t == nil {
		return errors.New("broker: no link")
	}
	return deviceSend(t, FramePushRegister, payload)
}

// RegisterPushFID registers this device's Firebase Installation ID. The broker
// accepts the same bounded character set as an FCM registration token, but the
// two values occupy different wire fields.
func (d *DeviceClient) RegisterPushFID(fid string) error {
	if !validFCMPushIdentifier(fid) {
		return errors.New("push register: a Firebase Installation ID is 1-512 of A-Z a-z 0-9 _ : -")
	}
	payload := pushRegisterPayload{PairingID: hexID(d.Pairing().PairingID), Provider: "fcm", FID: fid}
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	d.mu.Lock()
	t := d.link
	d.mu.Unlock()
	if t == nil {
		return errors.New("broker: no link")
	}
	return deviceSend(t, FramePushRegister, payload)
}

func validFCMPushIdentifier(value string) bool {
	if len(value) < 1 || len(value) > 512 {
		return false
	}
	for _, r := range value {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == ':' || r == '-') {
			return false
		}
	}
	return true
}

// Send seals one message to the agent and retains it until its ACK. With
// deliveryIntentLimit unacknowledged it waits for room, or for the context to end.
func (d *DeviceClient) Send(ctx context.Context, plaintext []byte) error {
	select {
	case d.slots <- struct{}{}:
	default:
		d.mu.Lock()
		d.expireLocked()
		d.mu.Unlock()
		select {
		case d.slots <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	d.mu.Lock()
	t := d.link
	d.mu.Unlock()
	messageID := make([]byte, 16)
	_, err := rand.Read(messageID)
	if err == nil && t == nil {
		err = errors.New("broker: no link")
	}
	if err == nil {
		err = d.seal(t, messageID, append([]byte(nil), plaintext...), false)
	}
	if err != nil {
		d.mu.Lock()
		if _, held := d.pending[hexID(messageID)]; !held {
			<-d.slots
		}
		d.mu.Unlock()
	}
	return err
}

func (d *DeviceClient) seal(t Transport, messageID, plaintext []byte, resend bool) error {
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	d.mu.Lock()
	s, counter, p, me := d.session, d.counter, d.agent, d.identity.KeyID()
	if s != nil {
		d.counter++
	}
	d.mu.Unlock()
	if s == nil {
		return errors.New("broker: no session")
	}
	sessionID, _ := hex.DecodeString(s.SessionID())
	sealed, err := s.SealMessage(TransportAAD(p.PairingID, sessionID, 0, DirectionDeviceToAgent, me, p.DeviceKeyID, messageID, counter), plaintext)
	if err != nil {
		return err
	}
	frame := CiphertextFrame{PairingID: hexID(p.PairingID), SessionID: s.SessionID(), MessageID: hexID(messageID), SenderKeyID: hexID(me), RecipientKeyID: hexID(p.DeviceKeyID), Counter: counter, PlaintextBytes: len(plaintext), Ciphertext: base64.RawURLEncoding.EncodeToString(sealed)}
	d.mu.Lock()
	if retained, held := d.pending[frame.MessageID]; held {
		retained.plaintext, retained.session = plaintext, frame.SessionID
		d.pending[frame.MessageID] = retained
	} else {
		d.pending[frame.MessageID] = devicePending{plaintext: plaintext, sent: d.now(), session: frame.SessionID}
	}
	if resend {
		d.counts.Resent++
	} else {
		d.counts.Sent++
	}
	d.mu.Unlock()
	return deviceSend(t, FrameCiphertext, frame)
}

// resendStale follows the agent's rule (item 2wr) on the device (item 2wv): once a new
// session's RESENDs have had queueFullWait to arrive, whatever is still retained under
// an earlier session is sealed again, same message id, oldest first, under this one.
func (d *DeviceClient) resendStale(ctx context.Context, t Transport, s *Session) {
	select {
	case <-time.After(queueFullWait):
	case <-ctx.Done():
		return
	}
	type stale struct {
		id        string
		plaintext []byte
		sent      time.Time
	}
	var resend []stale
	d.mu.Lock()
	if d.session == s {
		for id, retained := range d.pending {
			if retained.session != s.SessionID() {
				resend = append(resend, stale{id, retained.plaintext, retained.sent})
			}
		}
	}
	d.mu.Unlock()
	slices.SortFunc(resend, func(a, b stale) int { return a.sent.Compare(b.sent) })
	for _, message := range resend {
		messageID, _ := hex.DecodeString(message.id)
		if d.seal(t, messageID, message.plaintext, true) != nil {
			return
		}
	}
}

// expireLocked frees what the broker has dropped by its own lifetime without saying so.
func (d *DeviceClient) expireLocked() {
	for id, retained := range d.pending {
		if d.now().Sub(retained.sent) >= deliveryLifetime {
			delete(d.pending, id)
			<-d.slots
		}
	}
}

// Counters is a snapshot; Unacknowledged is what is retained for RESEND.
func (d *DeviceClient) Counters() Counters {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.counts
	out.Unacknowledged = int64(len(d.pending))
	return out
}

// Close ends the link. The pairing stays.
func (d *DeviceClient) Close() {
	d.mu.Lock()
	t, stop := d.link, d.stop
	d.mu.Unlock()
	if stop != nil {
		stop()
	}
	if t != nil {
		_ = t.Close(1000, "closed")
	}
}
