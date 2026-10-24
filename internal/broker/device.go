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
)

// THE DEVICE ROLE, beside the agent's Client (item 2wo). A phone's core is this and a
// binding: it pairs with a code, authenticates as "device", answers the agent's session
// handshake as the responder, and then sends and receives ciphertext with ACK and RESEND.
// Nothing outside this package builds a frame.

// maxDevicePending bounds the messages a device retains for RESEND; Send waits for room.
const maxDevicePending = 256

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
	link    Transport
	stop    context.CancelFunc
	session *Session
	counter uint64
	pending map[string][]byte
	counts  Counters
}

// NewDeviceClient builds one from a device identity and, when it is already paired, the
// pairing Pair returned last time; keeping both is the caller's business.
func NewDeviceClient(identity Identity, agent Pairing) *DeviceClient {
	return &DeviceClient{identity: identity, agent: agent, slots: make(chan struct{}, maxDevicePending), pending: map[string][]byte{}}
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

// Pair sends the agent's code as the device's PAIR_BEGIN and returns the fingerprint the
// person compares with the one the agent shows.
func (d *DeviceClient) Pair(ctx context.Context, dial Dialer, code string) (string, error) {
	t, err := dial(ctx)
	if err != nil {
		return "", err
	}
	defer t.Close(1000, "paired")
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
		return "", errors.New("pairing: the peer key id does not match the peer keys")
	}
	transcript := TranscriptHash(pairingID, signing, agreement, id.SigningPublic(), id.AgreementPublic())
	signature := ed25519.Sign(ed25519.NewKeyFromSeed(id.SigningSeed), transcript)
	if err := deviceSend(t, FramePairConfirm, pairConfirmPayload{PairingID: peer.PairingID, TranscriptHash: hexID(transcript), Signature: b64.EncodeToString(signature)}); err != nil {
		return "", err
	}
	var complete pairCompletePayload
	if err := deviceRead(ctx, t, FramePairComplete, &complete); err != nil {
		return "", err
	}
	d.mu.Lock()
	d.agent = Pairing{PairingID: pairingID, DeviceKeyID: keyID, DeviceSigning: signing, DeviceAgreement: agreement}
	d.mu.Unlock()
	return Fingerprint(signing, agreement, id.SigningPublic(), id.AgreementPublic()), nil
}

// Connect authenticates as the device and serves the link on its own goroutine until
// Close or the context ends. received is given each opened message; event one line per
// session change, ending with "closed: <why>" when the link ends on its own.
func (d *DeviceClient) Connect(ctx context.Context, dial Dialer, received func([]byte), event func(string)) error {
	if len(d.Pairing().PairingID) != 16 {
		return errors.New("broker: not paired")
	}
	ctx, stop := context.WithCancel(ctx)
	t, err := dial(ctx)
	if err == nil {
		err = d.authenticate(ctx, t)
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
	go func() {
		err := d.serve(ctx, t, received, event)
		_ = t.Close(1000, "done")
		d.mu.Lock()
		d.link, d.session = nil, nil
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

func (d *DeviceClient) serve(ctx context.Context, t Transport, received func([]byte), event func(string)) error {
	b64, p, me := base64.RawURLEncoding, d.Pairing(), d.identity.KeyID()
	var opening *Session
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
				kind, out.SessionID, out.Confirmation = FrameSessionReady, opening.SessionID(), b64.EncodeToString(sealed)
				d.mu.Lock()
				d.session, d.counter = opening, 1 // the sealed ready was counter 0
				d.mu.Unlock()
				opening = nil
				event("session " + out.SessionID[:8])
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
			if err := deviceSend(t, FrameAck, IDOnlyFrame{PairingID: in.PairingID, SessionID: in.SessionID, MessageID: in.MessageID}); err != nil {
				return err
			}
			d.mu.Lock()
			d.counts.Received++
			d.mu.Unlock()
			received(plaintext)
		case FrameAck, FrameExpired, FrameResend, FrameQueued:
			var in IDOnlyFrame
			if err := DecodeInto(frame.Payload, &in); err != nil {
				return err
			}
			d.mu.Lock()
			plaintext, held := d.pending[in.MessageID]
			if held && (frame.Type == FrameAck || frame.Type == FrameExpired) {
				delete(d.pending, in.MessageID)
				<-d.slots
			}
			d.mu.Unlock()
			if frame.Type == FrameResend && held {
				messageID, _ := hex.DecodeString(in.MessageID)
				if err := d.seal(t, messageID, plaintext, true); err != nil {
					return err
				}
			}
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

// Send seals one message to the agent and retains it until its ACK. With
// maxDevicePending unacknowledged it waits for room, or for the link to end.
func (d *DeviceClient) Send(ctx context.Context, plaintext []byte) error {
	select {
	case d.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
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
	d.pending[frame.MessageID] = plaintext
	if resend {
		d.counts.Resent++
	} else {
		d.counts.Sent++
	}
	d.mu.Unlock()
	return deviceSend(t, FrameCiphertext, frame)
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
