// Package broker is this AgentB's client for the broker that lives in its own
// repository: it dials out, holds one end-to-end session with the operator's phone,
// and carries application messages over it. Item 2kq.
//
// NOTHING HERE TRUSTS THE BROKER. The broker routes ciphertext and sees metadata; every
// value below is derived from the two endpoints' own keys. docs/external/ holds the
// normative wire documents, and docs/external/broker-protocol-v2-vectors.json is
// re-derived by this package's own gate, in both roles — that gate was written and
// committed before any of this existed.
package broker

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/flynn/noise"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// Wire constants, spelled exactly as broker-protocol-v1.md spells them. They are
// separate constants rather than inline literals because every one of them is a value
// two implementations have to agree on byte for byte.
const (
	// WireVersion is the protocol version byte: 0x02.
	WireVersion = 0x02

	prologueLabel = "agentb-session-v2"
	initLabel     = "agentb-session-init-v2"
	confirmLabel  = "agentb-session-confirm-v2"
	finishLabel   = "agentb-session-finish-v2"
	readyLabel    = "agentb-session-ready-v2"
	sessionLabel  = "agentb-session-id-v2"

	pushSaltLabel = "agentb-push-v1"
	pushInfoLabel = "agentb-push-box-v1"

	// The two roles, as the AAD's direction byte.
	DirectionAgentToDevice = 0x01
	DirectionDeviceToAgent = 0x02
)

// PushKinds is the fixed vocabulary the push payload may carry. It is fixed so that a
// device can try all of it as AAD without the broker learning which one arrived.
var PushKinds = []string{"approval_required", "run_stopped", "item_stuck"}

// Identity is one endpoint's two private keys: an Ed25519 signing key (held as its
// seed) and an X25519 agreement key. Only the public halves ever cross the wire.
type Identity struct {
	SigningSeed []byte
	Agreement   []byte
}

// SigningPublic is the Ed25519 public key.
func (i Identity) SigningPublic() ed25519.PublicKey {
	return ed25519.NewKeyFromSeed(i.SigningSeed).Public().(ed25519.PublicKey)
}

// AgreementPublic is the X25519 public key.
func (i Identity) AgreementPublic() []byte {
	public, err := curve25519.X25519(i.Agreement, curve25519.Basepoint)
	if err != nil {
		return nil
	}
	return public
}

// KeyID is protocol v0's rule, unchanged by v2: the first 16 bytes of
// SHA-256(ed25519_public || x25519_public).
func (i Identity) KeyID() []byte {
	digest := sha256.New()
	digest.Write(i.SigningPublic())
	digest.Write(i.AgreementPublic())
	return digest.Sum(nil)[:16]
}

// Fingerprint is the human verification value: the first 20 bytes of SHA-256 over
// "agentb-pair-v1" and the four public keys, agent endpoint first, shown as ten groups
// of four upper-case hex characters. The operator compares it with his phone.
func Fingerprint(agentSigning ed25519.PublicKey, agentAgreement []byte, deviceSigning ed25519.PublicKey, deviceAgreement []byte) string {
	digest := sha256.New()
	digest.Write([]byte("agentb-pair-v1"))
	digest.Write(agentSigning)
	digest.Write(agentAgreement)
	digest.Write(deviceSigning)
	digest.Write(deviceAgreement)
	raw := strings.ToUpper(hex.EncodeToString(digest.Sum(nil)[:20]))
	groups := make([]string, 0, 10)
	for index := 0; index+4 <= len(raw); index += 4 {
		groups = append(groups, raw[index:index+4])
	}
	return strings.Join(groups, " ")
}

// SessionPrologue is the exact Noise prologue the document spells out.
func SessionPrologue(pairingID, handshakeID, agentKeyID, deviceKeyID []byte) []byte {
	out := make([]byte, 0, len(prologueLabel)+1+16*4+2)
	out = append(out, prologueLabel...)
	out = append(out, WireVersion)
	out = append(out, pairingID...)
	out = append(out, handshakeID...)
	out = append(out, agentKeyID...)
	out = append(out, deviceKeyID...)
	// The two trailing bytes are the roles: initiator 0x01, responder 0x02.
	return append(out, 0x01, 0x02)
}

// TransportAAD is the ciphertext AAD: version, pairing, session, epoch, direction, the
// two key ids, the message id and the counter. Every field is authenticated, so a frame
// replayed into another session, role or epoch fails to open rather than being noticed
// later.
func TransportAAD(pairingID, sessionID []byte, epoch uint32, direction byte, senderKeyID, recipientKeyID, messageID []byte, counter uint64) []byte {
	out := make([]byte, 0, 1+16+16+4+1+16+16+16+8)
	out = append(out, WireVersion)
	out = append(out, pairingID...)
	out = append(out, sessionID...)
	out = binary.BigEndian.AppendUint32(out, epoch)
	out = append(out, direction)
	out = append(out, senderKeyID...)
	out = append(out, recipientKeyID...)
	out = append(out, messageID...)
	return binary.BigEndian.AppendUint64(out, counter)
}

// fixedRandom feeds the Noise handshake a known ephemeral. It exists for the vector
// gate and for the scripted tests; a live session draws from crypto/rand.
type fixedRandom struct{ data []byte }

func (r *fixedRandom) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

var noiseSuite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)

// Session is one end of an established peer session. Both roles use it: which one is
// which decides only the direction byte and which CipherState seals.
type Session struct {
	identity  Identity
	prologue  []byte
	state     *noise.HandshakeState
	initiator bool

	send    *noise.CipherState
	receive *noise.CipherState
	hash    []byte
	ready   bool

	// The responder's confirmation signature, kept so the gate can compare it.
	confirmation []byte
}

// NewInitiator is this AgentB: the Noise IK initiator, which knows the device's static
// agreement key from the pairing.
func NewInitiator(identity Identity, devicePublic, prologue, ephemeral []byte) (*Session, error) {
	state, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   noiseSuite,
		Random:        &fixedRandom{data: ephemeral},
		Pattern:       noise.HandshakeIK,
		Initiator:     true,
		Prologue:      prologue,
		StaticKeypair: noise.DHKey{Private: identity.Agreement, Public: identity.AgreementPublic()},
		PeerStatic:    devicePublic,
	})
	if err != nil {
		return nil, fmt.Errorf("broker session: %w", err)
	}
	return &Session{identity: identity, prologue: prologue, state: state, initiator: true}, nil
}

// NewResponder is the device side. It exists here so the vector gate proves both roles
// and so the scripted-broker tests can play a phone.
func NewResponder(identity Identity, prologue, ephemeral []byte) (*Session, error) {
	state, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   noiseSuite,
		Random:        &fixedRandom{data: ephemeral},
		Pattern:       noise.HandshakeIK,
		Initiator:     false,
		Prologue:      prologue,
		StaticKeypair: noise.DHKey{Private: identity.Agreement, Public: identity.AgreementPublic()},
	})
	if err != nil {
		return nil, fmt.Errorf("broker session: %w", err)
	}
	return &Session{identity: identity, prologue: prologue, state: state}, nil
}

// WriteInit produces Noise message 1.
func (s *Session) WriteInit() ([]byte, error) {
	message, _, _, err := s.state.WriteMessage(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("broker session init: %w", err)
	}
	return message, nil
}

// InitSignature signs message 1 under the prologue, as the document requires before the
// responder will process it at all.
func (s *Session) InitSignature(messageOne []byte) []byte {
	return ed25519.Sign(ed25519.NewKeyFromSeed(s.identity.SigningSeed), s.initDigest(messageOne))
}

func (s *Session) initDigest(messageOne []byte) []byte {
	digest := sha256.New()
	digest.Write([]byte(initLabel))
	digest.Write(s.prologue)
	digest.Write(binary.BigEndian.AppendUint32(nil, uint32(len(messageOne))))
	digest.Write(messageOne)
	return digest.Sum(nil)
}

// ReadInit is the responder verifying that signature BEFORE processing message 1, and
// then processing it.
func (s *Session) ReadInit(messageOne []byte, agentSigning ed25519.PublicKey, signature []byte) error {
	if !ed25519.Verify(agentSigning, s.initDigest(messageOne), signature) {
		return errors.New("broker session: the initiator signature does not verify")
	}
	if _, _, _, err := s.state.ReadMessage(nil, messageOne); err != nil {
		return fmt.Errorf("broker session init: %w", err)
	}
	return nil
}

// WriteResponse produces Noise message 2 and splits the transport keys.
func (s *Session) WriteResponse() ([]byte, error) {
	message, first, second, err := s.state.WriteMessage(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("broker session response: %w", err)
	}
	s.hash = append([]byte(nil), s.state.ChannelBinding()...)
	// Split gives the initiator's sending state first. The responder sends with the
	// second and receives with the first.
	s.receive, s.send = first, second
	return message, nil
}

// ReadResponse is the initiator reading message 2, splitting, and verifying the
// responder's signature over the confirmation.
func (s *Session) ReadResponse(messageTwo []byte, deviceSigning ed25519.PublicKey, signature []byte) error {
	_, first, second, err := s.state.ReadMessage(nil, messageTwo)
	if err != nil {
		return fmt.Errorf("broker session response: %w", err)
	}
	s.hash = append([]byte(nil), s.state.ChannelBinding()...)
	s.send, s.receive = first, second
	if !ed25519.Verify(deviceSigning, s.confirmDigest(), signature) {
		return errors.New("broker session: the responder confirmation does not verify")
	}
	s.confirmation = append([]byte(nil), signature...)
	return nil
}

func (s *Session) confirmDigest() []byte {
	digest := sha256.New()
	digest.Write([]byte(confirmLabel))
	digest.Write(s.prologue)
	digest.Write(s.hash)
	return digest.Sum(nil)
}

// ConfirmSignature is the responder's signature over the confirmation digest.
func (s *Session) ConfirmSignature() []byte {
	return ed25519.Sign(ed25519.NewKeyFromSeed(s.identity.SigningSeed), s.confirmDigest())
}

// HandshakeHash is the Noise channel binding both sides obtain.
func (s *Session) HandshakeHash() []byte { return s.hash }

// SessionID is the first 16 bytes of SHA-256(label || h), lower-case hex.
func (s *Session) SessionID() string {
	digest := sha256.New()
	digest.Write([]byte(sessionLabel))
	digest.Write(s.hash)
	return hex.EncodeToString(digest.Sum(nil)[:16])
}

func (s *Session) aad(label string) []byte {
	out := make([]byte, 0, len(label)+len(s.prologue)+len(s.hash))
	out = append(out, label...)
	out = append(out, s.prologue...)
	return append(out, s.hash...)
}

// SealFinish is the initiator's first transport message: its signature over the
// confirmation, under the finish AAD.
func (s *Session) SealFinish() ([]byte, error) {
	out, err := s.send.Encrypt(nil, s.aad(finishLabel), s.ConfirmSignature())
	if err != nil {
		return nil, fmt.Errorf("broker session finish: %w", err)
	}
	return out, nil
}

// OpenFinish is the responder verifying it.
func (s *Session) OpenFinish(ciphertext []byte, agentSigning ed25519.PublicKey) error {
	plaintext, err := s.receive.Decrypt(nil, s.aad(finishLabel), ciphertext)
	if err != nil {
		return fmt.Errorf("broker session finish: %w", err)
	}
	if !ed25519.Verify(agentSigning, s.confirmDigest(), plaintext) {
		return errors.New("broker session: the initiator confirmation does not verify")
	}
	s.ready = true
	return nil
}

// SealReady is the responder's ASCII ready, after which traffic is allowed.
func (s *Session) SealReady() ([]byte, error) {
	out, err := s.send.Encrypt(nil, s.aad(readyLabel), []byte("ready"))
	if err != nil {
		return nil, fmt.Errorf("broker session ready: %w", err)
	}
	return out, nil
}

// OpenReady is the initiator's check that the device is ready. Session traffic before
// this decrypts is forbidden, which is why it sets the flag.
func (s *Session) OpenReady(ciphertext []byte) error {
	plaintext, err := s.receive.Decrypt(nil, s.aad(readyLabel), ciphertext)
	if err != nil {
		return fmt.Errorf("broker session ready: %w", err)
	}
	if string(plaintext) != "ready" {
		return errors.New("broker session: the ready message is not ready")
	}
	s.ready = true
	return nil
}

// Ready reports whether session traffic is permitted yet.
func (s *Session) Ready() bool { return s.ready }

// SealMessage encrypts one application message under the caller's AAD.
func (s *Session) SealMessage(aad, plaintext []byte) ([]byte, error) {
	if !s.ready {
		return nil, errors.New("broker session: traffic before ready")
	}
	out, err := s.send.Encrypt(nil, aad, plaintext)
	if err != nil {
		return nil, fmt.Errorf("broker seal: %w", err)
	}
	return out, nil
}

// OpenMessage decrypts one, authenticating every field of the AAD with it.
func (s *Session) OpenMessage(aad, ciphertext []byte) ([]byte, error) {
	if !s.ready {
		return nil, errors.New("broker session: traffic before ready")
	}
	out, err := s.receive.Decrypt(nil, aad, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("broker open: %w", err)
	}
	return out, nil
}

// Rekey advances one direction to the next epoch's key, as the document requires after
// its message or byte threshold.
func (s *Session) Rekey(direction byte) {
	if (direction == DirectionAgentToDevice) == s.initiator {
		s.send.Rekey()
		return
	}
	s.receive.Rekey()
}

// PushSalt is SHA-256("agentb-push-v1" || 0x02 || pairing_id).
func PushSalt(pairingID []byte) []byte {
	digest := sha256.New()
	digest.Write([]byte(pushSaltLabel))
	digest.Write([]byte{WireVersion})
	digest.Write(pairingID)
	return digest.Sum(nil)
}

// PushKey is the HKDF-SHA-256 output over the X25519 shared secret.
func PushKey(ephemeralPrivate, devicePublic, salt []byte) ([]byte, error) {
	shared, err := curve25519.X25519(ephemeralPrivate, devicePublic)
	if err != nil {
		return nil, fmt.Errorf("push key: %w", err)
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, salt, []byte(pushInfoLabel)), key); err != nil {
		return nil, fmt.Errorf("push key: %w", err)
	}
	return key, nil
}

// PushAAD is 0x02 || pairing_id || kind.
func PushAAD(pairingID []byte, kind string) []byte {
	out := make([]byte, 0, 1+len(pairingID)+len(kind))
	out = append(out, WireVersion)
	out = append(out, pairingID...)
	return append(out, kind...)
}

// PushPayload is what a push says: which chat, and one line. Nothing else — after
// opening it the phone reconnects and resynchronizes for authoritative state.
type PushPayload struct {
	Kind   string `json:"kind"`
	ChatID string `json:"chat_id"`
	Notice string `json:"notice"`
}

// PushPlaintext is the canonical JSON: no whitespace, keys in this exact order. It is
// built by hand rather than by the encoder because the ORDER is normative and a struct
// tag change would silently break two implementations' agreement.
func PushPlaintext(kind, chatID, notice string) []byte {
	quoted := func(value string) []byte {
		out, _ := json.Marshal(value)
		return out
	}
	out := []byte(`{"kind":`)
	out = append(out, quoted(kind)...)
	out = append(out, []byte(`,"chat_id":`)...)
	out = append(out, quoted(chatID)...)
	out = append(out, []byte(`,"notice":`)...)
	out = append(out, quoted(notice)...)
	return append(out, '}')
}

// SealPush produces PUSH.ciphertext: ephemeral_public || nonce || sealed.
func SealPush(ephemeralPrivate, devicePublic, pairingID []byte, kind string, nonce, plaintext []byte) ([]byte, error) {
	if len(nonce) != chacha20poly1305.NonceSize {
		return nil, fmt.Errorf("push: nonce is %d bytes, want %d", len(nonce), chacha20poly1305.NonceSize)
	}
	key, err := PushKey(ephemeralPrivate, devicePublic, PushSalt(pairingID))
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("push: %w", err)
	}
	ephemeralPublic, err := curve25519.X25519(ephemeralPrivate, curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("push: %w", err)
	}
	box := make([]byte, 0, 32+len(nonce)+len(plaintext)+aead.Overhead())
	box = append(box, ephemeralPublic...)
	box = append(box, nonce...)
	return aead.Seal(box, nonce, plaintext, PushAAD(pairingID, kind)), nil
}

// OpenPush is the device side, and it is written here because the vector gate proves
// both roles. It tries the three fixed kinds as AAD and accepts exactly one opening
// whose plaintext repeats that kind; anything else is discarded whole.
func OpenPush(deviceAgreement, pairingID, box []byte) (PushPayload, error) {
	if len(box) < 32+chacha20poly1305.NonceSize+chacha20poly1305.Overhead {
		return PushPayload{}, errors.New("push: the box is too short")
	}
	ephemeralPublic := box[:32]
	nonce := box[32 : 32+chacha20poly1305.NonceSize]
	sealed := box[32+chacha20poly1305.NonceSize:]
	shared, err := curve25519.X25519(deviceAgreement, ephemeralPublic)
	if err != nil {
		return PushPayload{}, fmt.Errorf("push: %w", err)
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, PushSalt(pairingID), []byte(pushInfoLabel)), key); err != nil {
		return PushPayload{}, fmt.Errorf("push: %w", err)
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return PushPayload{}, fmt.Errorf("push: %w", err)
	}
	found := PushPayload{}
	openings := 0
	for _, kind := range PushKinds {
		plaintext, openErr := aead.Open(nil, nonce, sealed, PushAAD(pairingID, kind))
		if openErr != nil {
			continue
		}
		var payload PushPayload
		if json.Unmarshal(plaintext, &payload) != nil || payload.Kind != kind {
			continue
		}
		openings++
		found = payload
	}
	if openings != 1 {
		return PushPayload{}, errors.New("push: no single opening whose plaintext repeats its kind")
	}
	return found, nil
}
