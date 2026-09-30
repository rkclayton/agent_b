package broker

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Item 2kq (b): PAIRING FROM SETTINGS, PAIR_BEGIN-first.
//
// This AgentB begins the pairing and shows the operator the code; his phone types it in.
// The code authorizes the exchange and is not input to any key. What he compares on the
// two screens afterwards is the fingerprint, and nothing routes until both ends confirm.

// crockford is the unambiguous alphabet the code is written in.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewPairingCode draws 60 random bits and renders three groups of four.
func NewPairingCode() (string, []byte, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	raw[0] &= 0x0f
	value := binary.BigEndian.Uint64(raw)
	digits := make([]byte, 12)
	for index := range digits {
		digits[index] = crockford[(value>>uint(55-index*5))&31]
	}
	return string(digits[:4]) + "-" + string(digits[4:8]) + "-" + string(digits[8:]), raw, nil
}

// DecodePairingCode is the other side of that, written here because the refusals are
// part of the protocol: separators and case are folded, as are Crockford confusables.
func DecodePairingCode(code string) ([]byte, error) {
	cleaned := strings.Map(func(value rune) rune {
		if value == '-' || value == ' ' {
			return -1
		}
		value = []rune(strings.ToUpper(string(value)))[0]
		if value == 'O' {
			return '0'
		}
		if value == 'I' || value == 'L' {
			return '1'
		}
		return value
	}, strings.TrimSpace(code))
	if len(cleaned) == 12 {
		var value uint64
		for _, digit := range []byte(cleaned) {
			index := strings.IndexByte(crockford, digit)
			if index < 0 {
				return nil, fmt.Errorf("pairing code: %q is not a Crockford digit", digit)
			}
			value = value<<5 | uint64(index)
		}
		raw := make([]byte, 8)
		binary.BigEndian.PutUint64(raw, value)
		return raw, nil
	}
	if len(cleaned) != 40 {
		return nil, fmt.Errorf("pairing code: %d digits, want 12 or 40", len(cleaned))
	}
	raw := make([]byte, 0, 20)
	for index := 0; index < len(cleaned); index += 2 {
		high := strings.IndexByte(crockford, cleaned[index])
		low := strings.IndexByte(crockford, cleaned[index+1])
		if high < 0 || high > 15 || low < 0 || low > 15 {
			return nil, fmt.Errorf("pairing code: %q is not a v1 code digit", cleaned[index:index+2])
		}
		raw = append(raw, byte(high<<4|low))
	}
	return raw, nil
}

// CodeHash is what the agent sends: SHA-256 over the decoded bytes, never the code.
func CodeHash(raw []byte) []byte {
	digest := sha256.Sum256(raw)
	return digest[:]
}

// TranscriptHash is what PAIR_CONFIRM signs: the label, the pairing id and the four
// public keys, agent endpoint first.
func TranscriptHash(pairingID []byte, agentSigning ed25519.PublicKey, agentAgreement []byte, deviceSigning ed25519.PublicKey, deviceAgreement []byte) []byte {
	digest := sha256.New()
	digest.Write([]byte("agentb-pair-transcript-v1"))
	digest.Write(pairingID)
	digest.Write(agentSigning)
	digest.Write(agentAgreement)
	digest.Write(deviceSigning)
	digest.Write(deviceAgreement)
	return digest.Sum(nil)
}

type pairBeginPayload struct {
	Role     string `json:"role"`
	Ed25519  string `json:"ed25519_public"`
	X25519   string `json:"x25519_public"`
	CodeHash string `json:"code_hash"`
}

type pairWaitingPayload struct {
	ExpiresAt string `json:"expires_at"`
}

// The field names are the LIVE broker's, read off the wire rather than guessed: the
// document's frame registry says PAIR_PEER carries "both peer public keys" without
// naming the fields, and the broker calls them peer_ed25519_public and
// peer_x25519_public. Measured against wss://…:8444 at rel-1.36.0/W5.
type pairPeerPayload struct {
	PairingID string `json:"pairing_id"`
	PeerKeyID string `json:"peer_key_id"`
	Ed25519   string `json:"peer_ed25519_public"`
	X25519    string `json:"peer_x25519_public"`
}

type pairConfirmPayload struct {
	PairingID      string `json:"pairing_id"`
	TranscriptHash string `json:"transcript_hash"`
	Signature      string `json:"signature"`
}

type pairCompletePayload struct {
	PairingID string `json:"pairing_id"`
}

type revokePayload struct {
	PairingID   string `json:"pairing_id"`
	Reason      string `json:"reason,omitempty"`
	Signature   string `json:"signature,omitempty"`
	EffectiveAt string `json:"effective_at,omitempty"`
}

// PairingOffer is what Settings shows while a pairing is under way: the code to type
// into the phone, and — once the phone has arrived — the fingerprint to compare.
type PairingOffer struct {
	Code string `json:"code"`
	// Item 2ns (b): the link and its QR, present only while the code is live and kept
	// nowhere. They are not persisted with an offer; the endpoint fills them per
	// response.
	Link        string `json:"link,omitempty"`
	QR          string `json:"qr,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	PairingID   string `json:"pairing_id,omitempty"`
	Complete    bool   `json:"complete"`
}

// BeginPairing runs the agent's half of the flow over one transport: PAIR_BEGIN with
// the code hash, then PAIR_PEER, then PAIR_CONFIRM once the caller has shown the
// fingerprint, then PAIR_COMPLETE. The caller supplies `confirm`, which is where the
// operator's comparison happens — this function does not decide that for him.
func BeginPairing(transport Transport, identity Identity, code string, receive func() (Frame, error), confirm func(offer PairingOffer) bool) (Pairing, error) {
	raw, err := DecodePairingCode(code)
	if err != nil {
		return Pairing{}, err
	}
	begin, err := Encode(FramePairBegin, pairBeginPayload{
		Role:     "agent",
		Ed25519:  base64.RawURLEncoding.EncodeToString(identity.SigningPublic()),
		X25519:   base64.RawURLEncoding.EncodeToString(identity.AgreementPublic()),
		CodeHash: hexID(CodeHash(raw)),
	})
	if err != nil {
		return Pairing{}, err
	}
	if err := transport.Send(begin); err != nil {
		return Pairing{}, err
	}

	offer := PairingOffer{Code: code}
	var pairing Pairing
	for {
		frame, err := receive()
		if err != nil {
			return Pairing{}, err
		}
		switch frame.Type {
		case FramePairWaiting:
			var waiting pairWaitingPayload
			if err := DecodeInto(frame.Payload, &waiting); err != nil {
				return Pairing{}, err
			}
			offer.ExpiresAt = waiting.ExpiresAt

		case FramePairPeer:
			var peer pairPeerPayload
			if err := DecodeInto(frame.Payload, &peer); err != nil {
				return Pairing{}, err
			}
			pairingID, err := hex.DecodeString(peer.PairingID)
			if err != nil {
				return Pairing{}, fmt.Errorf("pairing id is not hex: %w", err)
			}
			deviceSigning, err := base64.RawURLEncoding.DecodeString(peer.Ed25519)
			if err != nil {
				return Pairing{}, fmt.Errorf("peer signing key is not base64url: %w", err)
			}
			deviceAgreement, err := base64.RawURLEncoding.DecodeString(peer.X25519)
			if err != nil {
				return Pairing{}, fmt.Errorf("peer agreement key is not base64url: %w", err)
			}
			peerKeyID, err := hex.DecodeString(peer.PeerKeyID)
			if err != nil {
				return Pairing{}, fmt.Errorf("peer key id is not hex: %w", err)
			}
			// The broker told us an id for those keys; it is derived here rather than
			// believed, because a key id that does not match its keys is a broker
			// substituting an endpoint.
			derived := Identity{}.keyIDFor(deviceSigning, deviceAgreement)
			if hexID(derived) != hexID(peerKeyID) {
				return Pairing{}, errors.New("pairing: the peer key id does not match the peer keys")
			}
			pairing = Pairing{PairingID: pairingID, DeviceKeyID: derived, DeviceSigning: deviceSigning, DeviceAgreement: deviceAgreement}
			offer.PairingID = peer.PairingID
			offer.Fingerprint = Fingerprint(identity.SigningPublic(), identity.AgreementPublic(), deviceSigning, deviceAgreement)

			// THE OPERATOR'S COMPARISON. Nothing is confirmed until he says the two
			// screens match.
			if confirm != nil && !confirm(offer) {
				return Pairing{}, errors.New("pairing: the operator did not confirm the fingerprint")
			}
			transcript := TranscriptHash(pairingID, identity.SigningPublic(), identity.AgreementPublic(), deviceSigning, deviceAgreement)
			confirmFrame, err := Encode(FramePairConfirm, pairConfirmPayload{
				PairingID:      peer.PairingID,
				TranscriptHash: hexID(transcript),
				Signature:      base64.RawURLEncoding.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(identity.SigningSeed), transcript)),
			})
			if err != nil {
				return Pairing{}, err
			}
			if err := transport.Send(confirmFrame); err != nil {
				return Pairing{}, err
			}

		case FramePairComplete:
			var complete pairCompletePayload
			if err := DecodeInto(frame.Payload, &complete); err != nil {
				return Pairing{}, err
			}
			if len(pairing.PairingID) == 0 {
				return Pairing{}, errors.New("pairing: completed without a peer")
			}
			return pairing, nil

		case FrameError:
			var problem errorPayload
			if err := DecodeInto(frame.Payload, &problem); err != nil {
				return Pairing{}, err
			}
			return Pairing{}, fmt.Errorf("pairing refused: %s (%s)", problem.Code, problem.Detail)

		default:
			return Pairing{}, fmt.Errorf("pairing: unexpected frame 0x%02x", frame.Type)
		}
	}
}

// keyIDFor derives a key id from a peer's public keys, which is the same rule an
// endpoint applies to its own.
func (Identity) keyIDFor(signing, agreement []byte) []byte {
	digest := sha256.New()
	digest.Write(signing)
	digest.Write(agreement)
	return digest.Sum(nil)[:16]
}

// Revoke ends a pairing from this side. The reason is the fixed one the operator's
// action means: he pressed Revoke.
func Revoke(transport Transport, identity Identity, pairingID []byte) error {
	const reason = "operator_revoked"
	digest := sha256.New()
	digest.Write([]byte("agentb-revoke-v1"))
	digest.Write(pairingID)
	digest.Write([]byte(reason))
	frame, err := Encode(FrameRevoke, revokePayload{
		PairingID: hexID(pairingID),
		Reason:    reason,
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(identity.SigningSeed), digest.Sum(nil))),
	})
	if err != nil {
		return err
	}
	return transport.Send(frame)
}

// Item 2ns (a): THE PAIRING LINK, defined by docs/pairing-link-v1.md and built in one
// place. The operator never types the code: "this code is waaay too long its insane",
// "we dont need to have them enter a code do we?". The code itself is unchanged — its
// length is its margin, and shortening it would be a wire change in three repositories.
//
// BOTH VALUES GO IN THE FRAGMENT, which is never sent to a server. A phone with no app
// that opens this link reaches agentb.app with the path and nothing else.
const pairingLinkPrefix = "https://agentb.app/pair#"

// PairingLink renders the link for a live code and this AgentB's identity key.
func PairingLink(code string, agentSigning []byte) string {
	return pairingLinkPrefix + "c=" + code + "&k=" + base64.RawURLEncoding.EncodeToString(agentSigning)
}

// ReadPairingLink is the document's reading rule, here so the one definition includes
// what a reader must do: the scheme, host and path are exact; c and k are required; any
// other field is IGNORED, which is what lets the version stay 1; and the key is exactly
// 32 bytes of unpadded base64url. A link that fails any of that is refused whole.
func ReadPairingLink(link string) (string, []byte, error) {
	parsed, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return "", nil, fmt.Errorf("pairing link: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "agentb.app" || parsed.Path != "/pair" {
		return "", nil, errors.New("pairing link: not an agentb.app pairing link")
	}
	code, key := "", ""
	for _, pair := range strings.Split(parsed.Fragment, "&") {
		name, value, found := strings.Cut(pair, "=")
		if !found {
			continue
		}
		switch name {
		case "c":
			code = value
		case "k":
			key = value
		}
	}
	if code == "" || key == "" {
		return "", nil, errors.New("pairing link: it carries no code or no key")
	}
	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(raw) != 32 {
		return "", nil, errors.New("pairing link: the key is not 32 bytes of unpadded base64url")
	}
	return code, raw, nil
}
