package broker

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Item 2kq (b): PAIRING FROM SETTINGS, PAIR_BEGIN-first.
//
// This AgentB begins the pairing and shows the operator the code; his phone types it in.
// The code authorizes the exchange and is not input to any key. What he compares on the
// two screens afterwards is the fingerprint, and nothing routes until both ends confirm.

// crockford is the alphabet the code is written in. Only indices 0-15 are valid in a v1
// code: each byte is one high nibble and one low nibble, so the twenty random bytes
// become exactly forty digits.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewPairingCode draws 20 random bytes and renders them as eight groups of five, which
// is how the operator reads it off the screen.
func NewPairingCode() (string, []byte, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	digits := make([]byte, 0, 40)
	for _, value := range raw {
		digits = append(digits, crockford[value>>4], crockford[value&0x0f])
	}
	groups := make([]string, 0, 8)
	for index := 0; index+5 <= len(digits); index += 5 {
		groups = append(groups, string(digits[index:index+5]))
	}
	return strings.Join(groups, "-"), raw, nil
}

// DecodePairingCode is the other side of that, written here because the refusals are
// part of the protocol: hyphens are removed, ASCII is upper-cased, anything outside the
// first sixteen Crockford digits is refused, and exactly forty digits are required.
func DecodePairingCode(code string) ([]byte, error) {
	cleaned := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	if len(cleaned) != 40 {
		return nil, fmt.Errorf("pairing code: %d digits, want 40", len(cleaned))
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

type pairPeerPayload struct {
	PairingID string `json:"pairing_id"`
	PeerKeyID string `json:"peer_key_id"`
	Ed25519   string `json:"ed25519_public"`
	X25519    string `json:"x25519_public"`
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
	PairingID string `json:"pairing_id"`
	Reason    string `json:"reason"`
	Signature string `json:"signature"`
}

// PairingOffer is what Settings shows while a pairing is under way: the code to type
// into the phone, and — once the phone has arrived — the fingerprint to compare.
type PairingOffer struct {
	Code        string `json:"code"`
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
