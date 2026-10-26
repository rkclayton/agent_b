package broker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// DefaultURL is THE broker address, and this is the one place it is written.
// the operator asked "cant you just have the broker url hard coded in the app? we dont
// need to manipulate it or even expose it" once the broker went public, because a fresh
// install could not pair a phone until someone found and typed an address. A broker.url
// in the configuration file still wins — for tests and for anyone running their own —
// but nothing in the page shows or edits it.
const DefaultURL = "wss://broker.agentb.app/v1/connect"

// DIAL OUT AND HOLD.
//
// Nothing listens. This client dials the broker, authenticates as the agent endpoint,
// establishes one peer session with the paired device and holds it, reconnecting with
// backoff as a NEW session when the socket drops. The delivery layer is the document's:
// ACK, RESEND after a reconnect, resync after a broker restart, and close 4001 meaning
// a newer connection for this key won.
//
// THE TRANSPORT IS AN INTERFACE so the scenarios can be driven by a scripted broker in
// tests. The live one is a WebSocket; nothing else in this file knows that.

// Transport is one connection to the broker.
type Transport interface {
	Send(frame []byte) error
	Receive(ctx context.Context) ([]byte, error)
	Close(code int, reason string) error
}

// Dialer opens one. A live dialer speaks WebSocket to wss://<broker>/v1/connect.
type Dialer func(ctx context.Context) (Transport, error)

// Pairing is what a completed pairing leaves behind: the ids and the peer's public
// keys. It is what the client needs to establish a session, and it is stored by the
// caller, never by this package.
type Pairing struct {
	PairingID       []byte
	DeviceKeyID     []byte
	DeviceSigning   []byte
	DeviceAgreement []byte
}

// Delivery is one application message in flight, retained by the sender until it is
// acknowledged so a RESEND can encrypt the same id again under a fresh session.
type Delivery struct {
	MessageID []byte
	Plaintext []byte
	Counter   uint64
	Epoch     uint32
	Queued    bool
	session   *Session // the one it was last sealed under
}

// Status is what Settings shows.
type Status struct {
	State         string `json:"state"`
	BrokerBuild   string `json:"broker_build,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	EndedReason   string `json:"ended_reason,omitempty"`
	NextAttemptAt string `json:"next_attempt_at,omitempty"`
	LastMessageAt string `json:"last_message_at,omitempty"`
	Reconnects    int    `json:"reconnects"`
	PairedDevice  string `json:"paired_device,omitempty"`
	LogPath       string `json:"log_path,omitempty"`
}

// Client holds the one session.
type Client struct {
	identity Identity
	pairing  Pairing
	dial     Dialer
	handle   func(messageID []byte, plaintext []byte) []byte
	now      func() time.Time
	wait     func(time.Duration) <-chan time.Time // the reconnect backoff's timer

	mu           sync.Mutex
	status       Status
	session      *Session
	transport    Transport
	sendCount    uint64
	epoch        uint32
	pending      map[string]*Delivery
	pendingOrder []string
	pendingBytes int
	room         *sync.Cond // signalled when pending shrinks or the session changes
	heldPeer     *peerPayload
	queueRefused bool // a queue_full came; the next session sends the refused again
	// handled remembers request ids already executed, so a RESEND after a reconnect
	// delivers the same answer instead of running the work twice. the
	// deduplication at the execution boundary.
	handled      map[string][]byte
	handledOrder []string
	// sendMu keeps sealed frames on the wire in counter order when the
	// answer to a request and the downstream stream send at the same time, and
	// connected is told each time a session is up, with a context that ends with it.
	sendMu    sync.Mutex
	pushes    [maxPendingPushes]string
	pushHead  int
	pushLen   int
	holding   func(context.Context)
	connected func(context.Context)
	refused   func(code, detail string)
	event     func(string)
}

const maxPendingPushes = 128

// deliveryIntentLimit is the broker's: it holds at most 64 delivery intents per pairing
// and refuses the next with queue_full (agent_b_server internal/broker/server.go:45,
// deliveryLimit). Deliver keeps no more than this many unacknowledged, so the broker is
// never asked to queue more (item 2wq).
const deliveryIntentLimit = 64

// handshakeLimit bounds SESSION_INIT to SESSION_READY. The live broker's encrypted round
// trip is about 53 ms, so a handshake is about two of them; ten times that is under the
// 5 s floor. A handshake not done by then is abandoned and the reconnect backoff runs, so a
// phone that dropped mid-handshake is never left at "holding" (item 2wr).
const handshakeLimit = 5 * time.Second

// queueFullWait is how long, after the session that follows a queue_full, the sender
// waits for the broker's RESENDs before it sends the refused messages again (item 2wr).
const queueFullWait = time.Second
const maxPendingBytes = 16 << 20
const maxPendingDeliveries = 2048

func (c *Client) OnHolding(holding func(context.Context)) { c.holding = holding }

// OnConnected is called, on its own goroutine, each time a session is established;
// its context is cancelled when that connection ends. Set it before Run.
func (c *Client) OnConnected(connected func(context.Context)) { c.connected = connected }

func (c *Client) OnSessionEvent(event func(string)) { c.event = event }

func (c *Client) recordEvent(message string) {
	if c.event != nil {
		c.event(message)
	}
}

func (c *Client) safeReason(err error) string {
	reason := err.Error()
	c.mu.Lock()
	secrets := []string{hexID(c.pairing.PairingID), hexID(c.pairing.DeviceKeyID), hexID(c.identity.KeyID())}
	if c.session != nil {
		secrets = append(secrets, c.session.SessionID())
	}
	c.mu.Unlock()
	for _, secret := range secrets {
		if len(secret) > 8 {
			reason = strings.ReplaceAll(reason, secret, secret[:8])
		}
	}
	return reason
}

// Deliver sends one downstream unit under a fresh message id on the live session. With
// deliveryIntentLimit messages unacknowledged it waits for an ACK or an expiry, and
// returns an error if the session ends meanwhile. A handler, which runs on the reader
// that receives those ACKs, uses DeliverNow.
func (c *Client) Deliver(plaintext []byte) error {
	transport, messageID, err := c.live()
	if err != nil {
		return err
	}
	c.mu.Lock()
	session := c.session
	for len(c.pending) >= deliveryIntentLimit && c.session == session && session != nil {
		c.room.Wait()
	}
	ended := c.session != session || session == nil
	c.mu.Unlock()
	if ended {
		return errors.New("broker: no session")
	}
	return c.Send(messageID, plaintext, transport)
}

// DeliverNow is Deliver without the wait, for a handler on the reader's goroutine.
func (c *Client) DeliverNow(plaintext []byte) error {
	transport, messageID, err := c.live()
	if err != nil {
		return err
	}
	return c.Send(messageID, plaintext, transport)
}

func (c *Client) Notify(kind, chatID, notice string) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	messageID := make([]byte, 16)
	if _, err := rand.Read(messageID); err != nil {
		return err
	}
	c.mu.Lock()
	transport := c.transport
	if transport == nil {
		c.mu.Unlock()
		return errors.New("broker: no connection")
	}
	if c.pushLen >= maxPendingPushes {
		c.mu.Unlock()
		return errors.New("broker: too many pushes awaiting answers")
	}
	c.pushes[(c.pushHead+c.pushLen)%maxPendingPushes] = kind
	c.pushLen++
	c.mu.Unlock()
	err := c.Push(transport, messageID, kind, chatID, notice)
	if err != nil {
		c.mu.Lock()
		c.pushLen--
		c.mu.Unlock()
		return err
	}
	return nil
}

func (c *Client) live() (Transport, []byte, error) {
	c.mu.Lock()
	transport, session := c.transport, c.session
	c.mu.Unlock()
	if transport == nil || session == nil {
		return nil, nil, errors.New("broker: no session")
	}
	messageID := make([]byte, 16)
	if _, err := rand.Read(messageID); err != nil {
		return nil, nil, err
	}
	return transport, messageID, nil
}

// NewClient builds one. handle is the application boundary: it is given a decrypted
// request and returns the response plaintext.
func NewClient(identity Identity, pairing Pairing, dial Dialer, handle func(messageID, plaintext []byte) []byte) *Client {
	client := &Client{
		identity: identity,
		pairing:  pairing,
		dial:     dial,
		handle:   handle,
		now:      time.Now,
		wait:     time.After,
		status:   Status{State: "broker unreachable"},
		pending:  map[string]*Delivery{},
		handled:  map[string][]byte{},
	}
	client.room = sync.NewCond(&client.mu)
	return client
}

// Status is a snapshot for the Settings page.
func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

func (c *Client) setState(state string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.State = state
	if err != nil {
		c.status.LastError = err.Error()
		return
	}
	if state == "holding" || state == "phone connected" {
		c.status.LastError = ""
		c.status.NextAttemptAt = ""
	}
}

func formatEndedReason(code, detail string) string {
	if detail == "" {
		return code
	}
	return code + ": " + detail
}

// Problem preserves an ERROR frame for the host that reports it.
type Problem struct {
	Code, Detail string
	Fatal        bool
}

func (e *Problem) Error() string { return formatEndedReason(e.Code, e.Detail) }

// OnRefused observes structured broker refusals without exposing any frame payload.
func (c *Client) OnRefused(fn func(code, detail string)) { c.refused = fn }

// Run dials and holds, reconnecting with backoff until the context ends. Each
// reconnection is a NEW session: fresh ephemerals, a fresh handshake id, a new session
// id. Nothing from the old session is reused.
func (c *Client) Run(ctx context.Context) error {
	backoff := time.Second
	for ctx.Err() == nil {
		err := c.once(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.mu.Lock()
		wasConnected := c.status.State == "holding" || c.status.State == "phone connected"
		c.mu.Unlock()
		var problem *Problem
		// A fatal refusal keeps backing off even after a connection: the broker refuses
		// the same thing again on the next one (item 2wq).
		fatal := errors.As(err, &problem) && problem.Fatal
		if wasConnected {
			if !fatal {
				backoff = time.Second
			}
			c.recordEvent("disconnected reason=" + c.safeReason(err))
		}
		if errors.As(err, &problem) && c.refused != nil {
			c.refused(problem.Code, problem.Detail)
		}
		if errors.As(err, &problem) && problem.Code == "connection_replaced" {
			// 4001 newest-wins. Another connection for this key took
			// over; this one stops rather than fighting it.
			c.mu.Lock()
			c.status.State = "broker unreachable"
			c.status.EndedReason = err.Error()
			c.mu.Unlock()
			return nil
		}
		if errors.As(err, &problem) && problem.Fatal && (problem.Code == "revoked" || problem.Code == "not_paired") {
			c.mu.Lock()
			c.status.State = "not paired"
			c.status.LastError = err.Error()
			c.status.EndedReason = err.Error()
			c.status.NextAttemptAt = ""
			c.mu.Unlock()
			return nil
		}
		c.mu.Lock()
		c.status.Reconnects++
		c.mu.Unlock()
		log.Printf("broker: the session dropped, reconnecting: %v", err)
		c.mu.Lock()
		c.status.State = "broker unreachable"
		c.status.LastError = err.Error()
		c.status.NextAttemptAt = c.now().Add(backoff).UTC().Format(time.RFC3339)
		if errors.As(err, &problem) {
			c.status.EndedReason = formatEndedReason(problem.Code, problem.Detail)
		}
		c.mu.Unlock()
		c.recordEvent("reconnect attempt backoff=" + backoff.String())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.wait(backoff):
		}
		backoff = nextReconnectBackoff(backoff, false)
	}
	return ctx.Err()
}

func nextReconnectBackoff(previous time.Duration, established bool) time.Duration {
	if established {
		return time.Second
	}
	return min(previous*2, 60*time.Second)
}

func (c *Client) once(ctx context.Context) error {
	transport, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer func() {
		c.dropSession()
		c.mu.Lock()
		c.transport = nil
		c.mu.Unlock()
		_ = transport.Close(1000, "done")
	}()
	c.mu.Lock()
	c.transport = transport
	c.pushHead, c.pushLen = 0, 0
	c.mu.Unlock()

	if err := c.authenticate(ctx, transport); err != nil {
		return err
	}
	c.recordEvent("connected")
	query, err := Encode(FramePeerQuery, peerQueryPayload{PairingID: hexID(c.pairing.PairingID)})
	if err != nil {
		return err
	}
	if err := transport.Send(query); err != nil {
		return err
	}
	c.recordEvent("PEER_QUERY sent")
	c.setState("holding", nil)
	connection, ended := context.WithCancel(ctx)
	defer ended()
	if c.holding != nil {
		go c.holding(connection)
	}
	return c.serve(ctx, transport)
}

type helloPayload struct {
	Role         string `json:"role"`
	ConnectionID string `json:"connection_id"`
	KeyID        string `json:"key_id"`
	Ed25519      string `json:"ed25519_public"`
	X25519       string `json:"x25519_public"`
}

type challengePayload struct {
	ConnectionID string `json:"connection_id"`
	Challenge    string `json:"challenge"`
}

type authPayload struct {
	ConnectionID string `json:"connection_id"`
	Signature    string `json:"signature"`
}

type readyPayload struct {
	ConnectionID string `json:"connection_id"`
	ServerTime   string `json:"server_time"`
	Build        string `json:"build,omitempty"`
}

type peerQueryPayload struct {
	PairingID string `json:"pairing_id"`
}

type peerPayload struct {
	PairingID string `json:"pairing_id"`
	Connected bool   `json:"connected"`
}

type ErrorPayload struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Fatal  bool   `json:"fatal"`
}

func (c *Client) authenticate(ctx context.Context, transport Transport) error {
	connectionID := make([]byte, 16)
	if _, err := rand.Read(connectionID); err != nil {
		return err
	}
	hello, err := Encode(FrameHello, helloPayload{
		Role:         "agent",
		ConnectionID: hexID(connectionID),
		KeyID:        hexID(c.identity.KeyID()),
		Ed25519:      base64.RawURLEncoding.EncodeToString(c.identity.SigningPublic()),
		X25519:       base64.RawURLEncoding.EncodeToString(c.identity.AgreementPublic()),
	})
	if err != nil {
		return err
	}
	if err := transport.Send(hello); err != nil {
		return err
	}
	frame, err := c.read(ctx, transport)
	if err != nil {
		return err
	}
	if err := c.expect(frame, FrameChallenge, "CHALLENGE"); err != nil {
		return err
	}
	var challenge challengePayload
	if err := DecodeInto(frame.Payload, &challenge); err != nil {
		return err
	}
	raw, err := base64.RawURLEncoding.DecodeString(challenge.Challenge)
	if err != nil {
		return fmt.Errorf("broker: the challenge is not base64url: %w", err)
	}
	auth, err := Encode(FrameAuth, authPayload{
		ConnectionID: challenge.ConnectionID,
		Signature:    base64.RawURLEncoding.EncodeToString(SignAuth(c.identity, "agent", connectionID, raw)),
	})
	if err != nil {
		return err
	}
	if err := transport.Send(auth); err != nil {
		return err
	}
	frame, err = c.read(ctx, transport)
	if err != nil {
		return err
	}
	if err := c.expect(frame, FrameReady, "READY"); err != nil {
		return err
	}
	var ready readyPayload
	if err := DecodeInto(frame.Payload, &ready); err != nil {
		return err
	}
	c.mu.Lock()
	c.status.BrokerBuild = ready.Build
	c.mu.Unlock()
	return nil
}

func (c *Client) expect(frame Frame, want byte, name string) error {
	if frame.Type == want {
		return nil
	}
	if frame.Type != FrameError {
		return fmt.Errorf("broker: expected %s, got frame 0x%02x", name, frame.Type)
	}
	var problem ErrorPayload
	if err := DecodeInto(frame.Payload, &problem); err != nil {
		return err
	}
	c.recordEvent("ERROR code=" + problem.Code + " detail=" + problem.Detail)
	return &Problem{Code: problem.Code, Detail: problem.Detail, Fatal: problem.Fatal}
}

type SessionFrame struct {
	PairingID    string `json:"pairing_id"`
	HandshakeID  string `json:"handshake_id"`
	SenderKeyID  string `json:"sender_key_id"`
	RecipientID  string `json:"recipient_key_id"`
	Noise        string `json:"noise,omitempty"`
	Signature    string `json:"signature,omitempty"`
	Confirmation string `json:"confirmation,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
}

// establish makes a new peer session with a fresh handshake id and ephemerals.
func (c *Client) establish(ctx context.Context, transport Transport) error {
	handshakeID := make([]byte, 16)
	if _, err := rand.Read(handshakeID); err != nil {
		return err
	}
	ephemeral := make([]byte, 32)
	if _, err := rand.Read(ephemeral); err != nil {
		return err
	}
	prologue := SessionPrologue(c.pairing.PairingID, handshakeID, c.identity.KeyID(), c.pairing.DeviceKeyID)
	session, err := NewInitiator(c.identity, c.pairing.DeviceAgreement, prologue, ephemeral)
	if err != nil {
		return err
	}
	messageOne, err := session.WriteInit()
	if err != nil {
		return err
	}
	init, err := Encode(FrameSessionInit, SessionFrame{
		PairingID:   hexID(c.pairing.PairingID),
		HandshakeID: hexID(handshakeID),
		SenderKeyID: hexID(c.identity.KeyID()),
		RecipientID: hexID(c.pairing.DeviceKeyID),
		Noise:       base64.RawURLEncoding.EncodeToString(messageOne),
		Signature:   base64.RawURLEncoding.EncodeToString(session.InitSignature(messageOne)),
	})
	if err != nil {
		return err
	}
	if err := transport.Send(init); err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, handshakeLimit)
	defer cancel()
	frame, err := c.readHandshake(bounded, transport, hexID(handshakeID))
	if err != nil {
		return c.abandoned(ctx, bounded, err)
	}
	if frame.Type != FrameSessionResp {
		return fmt.Errorf("broker: expected SESSION_RESPONSE, got frame 0x%02x", frame.Type)
	}
	var response SessionFrame
	if err := DecodeInto(frame.Payload, &response); err != nil {
		return err
	}
	messageTwo, err := base64.RawURLEncoding.DecodeString(response.Noise)
	if err != nil {
		return fmt.Errorf("broker: the response noise is not base64url: %w", err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(response.Signature)
	if err != nil {
		return fmt.Errorf("broker: the response signature is not base64url: %w", err)
	}
	if err := session.ReadResponse(messageTwo, c.pairing.DeviceSigning, signature); err != nil {
		return err
	}
	finish, err := session.SealFinish()
	if err != nil {
		return err
	}
	finishFrame, err := Encode(FrameSessionFinish, SessionFrame{
		PairingID:    hexID(c.pairing.PairingID),
		HandshakeID:  hexID(handshakeID),
		SenderKeyID:  hexID(c.identity.KeyID()),
		RecipientID:  hexID(c.pairing.DeviceKeyID),
		Confirmation: base64.RawURLEncoding.EncodeToString(finish),
	})
	if err != nil {
		return err
	}
	if err := transport.Send(finishFrame); err != nil {
		return err
	}
	frame, err = c.readHandshake(bounded, transport, hexID(handshakeID))
	if err != nil {
		return c.abandoned(ctx, bounded, err)
	}
	if frame.Type != FrameSessionReady {
		return fmt.Errorf("broker: expected SESSION_READY, got frame 0x%02x", frame.Type)
	}
	var readyFrame SessionFrame
	if err := DecodeInto(frame.Payload, &readyFrame); err != nil {
		return err
	}
	confirmation, err := base64.RawURLEncoding.DecodeString(readyFrame.Confirmation)
	if err != nil {
		return fmt.Errorf("broker: the ready confirmation is not base64url: %w", err)
	}
	if err := session.OpenReady(confirmation); err != nil {
		return err
	}
	if readyFrame.SessionID != session.SessionID() {
		return fmt.Errorf("broker: the device names session %s, this side derived %s", readyFrame.SessionID, session.SessionID())
	}
	c.mu.Lock()
	c.session = session
	c.sendCount = 1 // the sealed finish was counter 0
	c.epoch = 0
	c.status.SessionID = session.SessionID()
	c.mu.Unlock()
	return nil
}

type CiphertextFrame struct {
	PairingID      string `json:"pairing_id"`
	SessionID      string `json:"session_id"`
	MessageID      string `json:"message_id"`
	SenderKeyID    string `json:"sender_key_id"`
	RecipientKeyID string `json:"recipient_key_id"`
	Epoch          uint32 `json:"epoch"`
	Counter        uint64 `json:"counter"`
	PlaintextBytes int    `json:"plaintext_bytes"`
	Ciphertext     string `json:"ciphertext"`
}

type IDOnlyFrame struct {
	PairingID      string `json:"pairing_id,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	MessageID      string `json:"message_id"`
	RecipientKeyID string `json:"recipient_key_id,omitempty"`
	ExpiresAt      string `json:"expires_at,omitempty"`
}

func (c *Client) dropSession() {
	c.mu.Lock()
	c.session = nil
	c.status.SessionID = ""
	c.room.Broadcast()
	c.mu.Unlock()
}

func (c *Client) startConnected(ctx context.Context) context.CancelFunc {
	session, stop := context.WithCancel(ctx)
	go func() {
		c.connected(session)
		if session.Err() != nil {
			return
		}
		c.mu.Lock()
		transport := c.transport
		c.mu.Unlock()
		if transport != nil {
			_ = transport.Close(1011, "event listener dropped")
		}
	}()
	return stop
}

// serve handles one documented behaviour per frame and rejects unknown frames.
func (c *Client) serve(ctx context.Context, transport Transport) error {
	var stopSession context.CancelFunc
	defer func() {
		if stopSession != nil {
			stopSession()
		}
	}()
	for {
		frame, err := c.read(ctx, transport)
		if err != nil {
			return err
		}
		switch frame.Type {
		case FramePeer:
			var peer peerPayload
			if err := DecodeInto(frame.Payload, &peer); err != nil {
				return err
			}
			if peer.PairingID != hexID(c.pairing.PairingID) {
				return errors.New("broker: PEER names another pairing")
			}
			c.recordEvent(fmt.Sprintf("PEER received connected=%t", peer.Connected))
			if stopSession != nil {
				stopSession()
				stopSession = nil
			}
			c.dropSession()
			c.setState("holding", nil)
			if !peer.Connected {
				continue
			}
			if err := c.establish(ctx, transport); err != nil {
				if errors.Is(err, errOpeningUndeliverable) {
					continue
				}
				return err
			}
			// A PEER that came during the handshake came before the device's
			// SESSION_READY, and the broker relays one connection's frames in order:
			// connected=true is the connection the handshake just reached, and
			// connected=false describes an older connection, never this one (item 2wv).
			if held := c.heldPeer; held != nil {
				c.heldPeer = nil
				if !held.Connected {
					c.recordEvent("PEER connected=false ignored: it came before this handshake's SESSION_READY")
				}
			}
			c.setState("phone connected", nil)
			c.recordEvent("session ready")
			c.sendRefused(transport)
			if c.connected != nil {
				stopSession = c.startConnected(ctx)
			}
		case FrameCiphertext:
			if err := c.deliver(frame, transport); err != nil {
				return err
			}
		case FrameAck:
			var ack IDOnlyFrame
			if err := DecodeInto(frame.Payload, &ack); err != nil {
				return err
			}
			c.mu.Lock()
			c.removePendingLocked(ack.MessageID)
			c.mu.Unlock()
		case FrameQueued:
			// The device is offline; the broker holds an intent. The plaintext stays
			// retained here until an ACK or a RESEND.
			var queued IDOnlyFrame
			if err := DecodeInto(frame.Payload, &queued); err != nil {
				return err
			}
			c.mu.Lock()
			if delivery := c.pending[queued.MessageID]; delivery != nil {
				delivery.Queued = true
			}
			c.mu.Unlock()
		case FrameResend:
			var resend IDOnlyFrame
			if err := DecodeInto(frame.Payload, &resend); err != nil {
				return err
			}
			if err := c.resend(resend.MessageID, transport); err != nil {
				return err
			}
		case FrameExpired:
			var expired IDOnlyFrame
			if err := DecodeInto(frame.Payload, &expired); err != nil {
				return err
			}
			c.mu.Lock()
			c.removePendingLocked(expired.MessageID)
			c.mu.Unlock()
		case FramePing:
			var ping struct {
				Token string `json:"token"`
			}
			if err := DecodeInto(frame.Payload, &ping); err != nil {
				return err
			}
			pong, err := Encode(FramePong, ping)
			if err != nil {
				return err
			}
			if err := transport.Send(pong); err != nil {
				return err
			}
		case FramePushAccepted:
			if _, err := c.pushAnswer(frame); err != nil {
				return err
			}
		case FrameError:
			if handled, err := c.pushAnswer(frame); handled || err != nil {
				if err != nil {
					return err
				}
				continue
			}
			var problem ErrorPayload
			if err := DecodeInto(frame.Payload, &problem); err != nil {
				return err
			}
			if problem.Fatal {
				c.recordEvent("ERROR code=" + problem.Code + " detail=" + problem.Detail)
				return &Problem{Code: problem.Code, Detail: problem.Detail, Fatal: true}
			}
			c.recordEvent("ERROR code=" + problem.Code + " detail=" + problem.Detail)
			log.Printf("broker: %s (%s)", problem.Code, problem.Detail)
			if problem.Code == "queue_full" {
				c.mu.Lock()
				c.queueRefused = true
				c.mu.Unlock()
			}
		case FrameRevoked:
			var revoked revokePayload
			if err := DecodeInto(frame.Payload, &revoked); err != nil {
				return err
			}
			return &Problem{Code: "revoked", Detail: "pairing revoked", Fatal: true}
		default:
			return fmt.Errorf("broker: unexpected frame 0x%02x", frame.Type)
		}
	}
}

// deliver opens one application message, executes it at most once, and answers.
func (c *Client) deliver(frame Frame, transport Transport) error {
	var payload CiphertextFrame
	if err := DecodeInto(frame.Payload, &payload); err != nil {
		return err
	}
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	if session == nil || payload.SessionID != session.SessionID() {
		return fmt.Errorf("broker: wrong_session %s", payload.SessionID)
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(payload.Ciphertext)
	if err != nil {
		return fmt.Errorf("broker: ciphertext is not base64url: %w", err)
	}
	messageID, err := hex.DecodeString(payload.MessageID)
	if err != nil {
		return fmt.Errorf("broker: message id is not hex: %w", err)
	}
	sessionID, err := hex.DecodeString(payload.SessionID)
	if err != nil {
		return fmt.Errorf("broker: session id is not hex: %w", err)
	}
	aad := TransportAAD(c.pairing.PairingID, sessionID, payload.Epoch, DirectionDeviceToAgent,
		c.pairing.DeviceKeyID, c.identity.KeyID(), messageID, payload.Counter)
	plaintext, err := session.OpenMessage(aad, ciphertext)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.status.LastMessageAt = c.now().UTC().Format(time.RFC3339)
	c.mu.Unlock()
	ack, err := Encode(FrameAck, IDOnlyFrame{PairingID: payload.PairingID, SessionID: payload.SessionID, MessageID: payload.MessageID})
	if err != nil {
		return err
	}
	if err := transport.Send(ack); err != nil {
		return err
	}

	// DEDUPLICATED AT THE EXECUTION BOUNDARY. A retried request never
	// executes twice; the answer it produced the first time is sent again.
	c.mu.Lock()
	answer, already := c.handled[payload.MessageID]
	c.mu.Unlock()
	if !already {
		answer = c.handle(messageID, plaintext)
		c.mu.Lock()
		c.rememberHandled(payload.MessageID, answer)
		c.mu.Unlock()
	}
	if len(answer) == 0 {
		return nil
	}
	return c.Send(messageID, answer, transport)
}

func (c *Client) rememberHandled(id string, answer []byte) {
	c.handled[id] = answer
	c.handledOrder = append(c.handledOrder, id)
	if len(c.handledOrder) > 1024 {
		delete(c.handled, c.handledOrder[0])
		c.handledOrder = c.handledOrder[1:]
	}
}

// Send seals one application message to the device and retains it until it is
// acknowledged.
func (c *Client) Send(messageID, plaintext []byte, transport Transport) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	c.mu.Lock()
	session := c.session
	counter := c.sendCount
	epoch := c.epoch
	c.sendCount++
	if session != nil {
		c.retainPendingLocked(&Delivery{MessageID: messageID, Plaintext: plaintext, Counter: counter, Epoch: epoch, session: session})
	}
	c.mu.Unlock()
	if session == nil {
		return errors.New("broker: no session")
	}
	sessionID, err := hex.DecodeString(session.SessionID())
	if err != nil {
		return err
	}
	aad := TransportAAD(c.pairing.PairingID, sessionID, epoch, DirectionAgentToDevice,
		c.identity.KeyID(), c.pairing.DeviceKeyID, messageID, counter)
	ciphertext, err := session.SealMessage(aad, plaintext)
	if err != nil {
		return err
	}
	out, err := Encode(FrameCiphertext, CiphertextFrame{
		PairingID:      hexID(c.pairing.PairingID),
		SessionID:      session.SessionID(),
		MessageID:      hexID(messageID),
		SenderKeyID:    hexID(c.identity.KeyID()),
		RecipientKeyID: hexID(c.pairing.DeviceKeyID),
		Epoch:          epoch,
		Counter:        counter,
		PlaintextBytes: len(plaintext),
		Ciphertext:     base64.RawURLEncoding.EncodeToString(ciphertext),
	})
	if err != nil {
		return err
	}
	return transport.Send(out)
}

// resend encrypts a retained message again under the CURRENT session, which is what
// RESEND means after a reconnect: the same message id, new keys and a new counter.
func (c *Client) resend(messageID string, transport Transport) error {
	c.mu.Lock()
	delivery, ok := c.pending[messageID]
	c.mu.Unlock()
	if !ok {
		// Declined once; the broker drops the intent and tells the recipient (item 2wr).
		c.recordEvent("RESEND_DECLINED for a message no longer held: " + messageID[:min(8, len(messageID))])
		declined, err := Encode(FrameResendDeclined, IDOnlyFrame{PairingID: hexID(c.pairing.PairingID), MessageID: messageID})
		if err != nil {
			return err
		}
		return transport.Send(declined)
	}
	raw, err := hex.DecodeString(messageID)
	if err != nil {
		return err
	}
	return c.Send(raw, delivery.Plaintext, transport)
}

// Resync is what an endpoint does after a broker restart: the volatile delivery intents
// are gone, so everything still retained is offered again under the live session.
func (c *Client) Resync(transport Transport) error {
	c.mu.Lock()
	ids := make([]string, 0, len(c.pending))
	for id := range c.pending {
		ids = append(ids, id)
	}
	c.mu.Unlock()
	for _, id := range ids {
		if err := c.resend(id, transport); err != nil {
			return err
		}
	}
	return nil
}

// Pending reports how many messages are retained awaiting acknowledgement.
func (c *Client) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}

func (c *Client) PendingBytes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pendingBytes
}

func (c *Client) removePendingLocked(id string) {
	if delivery := c.pending[id]; delivery != nil {
		c.pendingBytes -= len(delivery.Plaintext)
		delete(c.pending, id)
		c.room.Broadcast()
	}
	for index, pendingID := range c.pendingOrder {
		if pendingID == id {
			c.pendingOrder = append(c.pendingOrder[:index], c.pendingOrder[index+1:]...)
			return
		}
	}
}

func (c *Client) retainPendingLocked(delivery *Delivery) {
	id := hexID(delivery.MessageID)
	if current := c.pending[id]; current != nil {
		current.Counter, current.Epoch, current.session = delivery.Counter, delivery.Epoch, delivery.session
		return
	}
	for c.pendingBytes+len(delivery.Plaintext) > maxPendingBytes || len(c.pending) >= maxPendingDeliveries {
		evicted := false
		for _, oldest := range c.pendingOrder {
			if candidate := c.pending[oldest]; candidate != nil && !candidate.Queued {
				c.removePendingLocked(oldest)
				evicted = true
				break
			}
		}
		if !evicted {
			return
		}
	}
	c.pending[id] = delivery
	c.pendingOrder = append(c.pendingOrder, id)
	c.pendingBytes += len(delivery.Plaintext)
}

var errOpeningUndeliverable = errors.New("broker: session opening recipient absent")

// abandoned names a handshake that ran out of handshakeLimit; other errors pass through.
func (c *Client) abandoned(ctx, bounded context.Context, err error) error {
	if deadline, _ := bounded.Deadline(); ctx.Err() != nil || time.Now().Before(deadline) {
		return err
	}
	c.recordEvent("handshake abandoned: no SESSION_READY within " + handshakeLimit.String())
	return fmt.Errorf("broker: no SESSION_READY within %s: %w", handshakeLimit, err)
}

// sendRefused follows a queue_full. The broker refuses only for a recipient with no
// session, so the refused frame's session is ending and it holds no intent for it: once
// this session's RESENDs have had queueFullWait to arrive, whatever is still retained
// under an earlier session is sent again, same message id, under this one (item 2wr).
func (c *Client) sendRefused(transport Transport) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.queueRefused {
		return
	}
	c.queueRefused = false
	session := c.session
	go func() {
		<-c.wait(queueFullWait)
		c.mu.Lock()
		var refused []*Delivery
		for _, id := range c.pendingOrder {
			if delivery := c.pending[id]; delivery != nil && delivery.session != session && c.session == session {
				refused = append(refused, delivery)
			}
		}
		c.mu.Unlock()
		for _, delivery := range refused {
			if c.Send(delivery.MessageID, delivery.Plaintext, transport) != nil {
				return
			}
		}
	}()
}

func (c *Client) readHandshake(ctx context.Context, transport Transport, handshakeID string) (Frame, error) {
	for {
		frame, err := c.read(ctx, transport)
		if err == nil && (frame.Type == FramePushAccepted || frame.Type == FrameError) {
			if handled, answerErr := c.pushAnswer(frame); handled || answerErr != nil {
				if answerErr != nil {
					return Frame{}, answerErr
				}
				continue
			}
		}
		if err == nil && frame.Type == FrameError {
			var problem ErrorPayload
			if err := DecodeInto(frame.Payload, &problem); err != nil {
				return Frame{}, err
			}
			c.recordEvent("ERROR code=" + problem.Code + " detail=" + problem.Detail)
			if !problem.Fatal {
				if problem.Code == "queue_full" {
					c.mu.Lock()
					c.queueRefused = true
					c.mu.Unlock()
				}
				continue
			}
			return Frame{}, &Problem{Code: problem.Code, Detail: problem.Detail, Fatal: problem.Fatal}
		}
		if err == nil && frame.Type == FrameQueued {
			var queued IDOnlyFrame
			if err := DecodeInto(frame.Payload, &queued); err != nil {
				return Frame{}, err
			}
			if queued.MessageID == handshakeID && queued.RecipientKeyID == hexID(c.pairing.DeviceKeyID) {
				return Frame{}, errOpeningUndeliverable
			}
			c.mu.Lock()
			if delivery := c.pending[queued.MessageID]; delivery != nil {
				delivery.Queued = true
			}
			c.mu.Unlock()
			continue
		}
		// Item 2wq: frames of the session being replaced, and presence, arrive between
		// SESSION_INIT and SESSION_RESPONSE. A PEER is held for after the handshake; an
		// ACK or an expiry settles its delivery; a CIPHERTEXT of the old session is left
		// unacknowledged, so the broker resends it under the new one; a RESEND waits for
		// the RESEND the broker sends once the new session is ready.
		if err == nil && frame.Type == FramePeer {
			var peer peerPayload
			if err := DecodeInto(frame.Payload, &peer); err != nil {
				return Frame{}, err
			}
			c.heldPeer = &peer
			continue
		}
		if err == nil && (frame.Type == FrameAck || frame.Type == FrameExpired) {
			var settled IDOnlyFrame
			if err := DecodeInto(frame.Payload, &settled); err != nil {
				return Frame{}, err
			}
			c.mu.Lock()
			c.removePendingLocked(settled.MessageID)
			c.mu.Unlock()
			continue
		}
		if err == nil && (frame.Type == FrameCiphertext || frame.Type == FrameResend) {
			continue
		}
		if err != nil || frame.Type != FramePing {
			return frame, err
		}
		if frame.Type == FramePing {
			var ping struct {
				Token string `json:"token"`
			}
			if err := DecodeInto(frame.Payload, &ping); err != nil {
				return Frame{}, err
			}
			pong, err := Encode(FramePong, ping)
			if err != nil {
				return Frame{}, err
			}
			if err := transport.Send(pong); err != nil {
				return Frame{}, err
			}
		}
	}
}

func (c *Client) pushAnswer(frame Frame) (bool, error) {
	answer := ""
	switch frame.Type {
	case FramePushAccepted:
		answer = "accepted"
	case FrameError:
		var problem ErrorPayload
		if err := DecodeInto(frame.Payload, &problem); err != nil {
			return true, err
		}
		if problem.Fatal {
			return false, nil
		}
		if problem.Code == "malformed" && problem.Detail == "recipient_no_token" {
			answer = "no_token"
		} else {
			answer = "refused"
		}
	default:
		return false, nil
	}
	c.mu.Lock()
	if c.pushLen == 0 {
		c.mu.Unlock()
		return false, nil
	}
	kind := c.pushes[c.pushHead]
	c.pushes[c.pushHead] = ""
	c.pushHead = (c.pushHead + 1) % maxPendingPushes
	c.pushLen--
	c.mu.Unlock()
	c.recordEvent("PUSH kind=" + kind + " answer=" + answer)
	return true, nil
}

func (c *Client) read(ctx context.Context, transport Transport) (Frame, error) {
	message, err := transport.Receive(ctx)
	if err != nil {
		return Frame{}, err
	}
	return Decode(message)
}
