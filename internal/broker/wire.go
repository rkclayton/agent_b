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
)

// The frame layer, exactly as broker-protocol-v1.md's registry spells it. A frame is a
// three-part header — version, type, big-endian payload length — and a JSON payload.
// Item 2kq.
const (
	FrameHello         = 0x01
	FrameChallenge     = 0x02
	FrameAuth          = 0x03
	FrameReady         = 0x04
	FramePairBegin     = 0x10
	FramePairWaiting   = 0x11
	FramePairPeer      = 0x12
	FramePairConfirm   = 0x13
	FramePairComplete  = 0x14
	FrameSessionInit   = 0x18
	FrameSessionResp   = 0x19
	FrameSessionFinish = 0x1a
	FrameSessionReady  = 0x1b
	FrameCiphertext    = 0x20
	FrameAck           = 0x21
	FrameQueued        = 0x22
	FramePushRegister  = 0x23
	FramePush          = 0x24
	FramePushAccepted  = 0x25
	FrameResend        = 0x26
	FrameExpired       = 0x27
	FrameRevoke        = 0x30
	FrameRevoked       = 0x31
	FramePing          = 0x40
	FramePong          = 0x41
	FrameError         = 0x7f

	// The limits the document states. They are enforced BEFORE allocation, which is
	// why they are here and not at the call site.
	controlPayloadMax    = 64 << 10
	ciphertextPayloadMax = 8 << 20

	// CloseConnectionReplaced is the WebSocket application close code the broker uses
	// when a newer connection for the same key wins. It is not an error on this side:
	// the newest connection is meant to win.
	CloseConnectionReplaced = 4001
)

// Frame is one wire frame: a type and its JSON payload.
type Frame struct {
	Type    byte
	Payload []byte
}

// Decode reads the header and the payload, refusing anything the document calls an
// error: a wrong version byte, a length that does not match the remaining bytes, or a
// payload past its limit.
func Decode(message []byte) (Frame, error) {
	if len(message) < 6 {
		return Frame{}, errors.New("broker frame: shorter than its header")
	}
	if message[0] != WireVersion {
		return Frame{}, fmt.Errorf("broker frame: unsupported_version 0x%02x", message[0])
	}
	length := binary.BigEndian.Uint32(message[2:6])
	if int(length) != len(message)-6 {
		return Frame{}, fmt.Errorf("broker frame: declared %d bytes, carries %d", length, len(message)-6)
	}
	limit := controlPayloadMax
	if message[1] == FrameCiphertext {
		limit = ciphertextPayloadMax
	}
	if int(length) > limit {
		return Frame{}, fmt.Errorf("broker frame: payload %d over the %d limit", length, limit)
	}
	return Frame{Type: message[1], Payload: message[6:]}, nil
}

// Encode is the other direction.
func Encode(frameType byte, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("broker frame: %w", err)
	}
	limit := controlPayloadMax
	if frameType == FrameCiphertext {
		limit = ciphertextPayloadMax
	}
	if len(body) > limit {
		return nil, fmt.Errorf("broker frame: payload %d over the %d limit", len(body), limit)
	}
	out := make([]byte, 6, 6+len(body))
	out[0] = WireVersion
	out[1] = frameType
	binary.BigEndian.PutUint32(out[2:6], uint32(len(body)))
	return append(out, body...), nil
}

// DecodeInto decodes a frame's payload into a value, refusing unknown fields — the
// document calls an unknown field an error, and a client that ignores one is a client
// that silently accepts a frame it does not understand.
func DecodeInto(payload []byte, value any) error {
	decoder := json.NewDecoder(newByteReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("broker payload: %w", err)
	}
	if decoder.More() {
		return errors.New("broker payload: trailing bytes")
	}
	return nil
}

type byteReader struct {
	data []byte
	at   int
}

func newByteReader(data []byte) *byteReader { return &byteReader{data: data} }

func (r *byteReader) Read(p []byte) (int, error) {
	if r.at >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.at:])
	r.at += n
	return n, nil
}

// AuthDigest is what the connection AUTH frame signs: the v2 domain, the version byte,
// the role, the connection id and the broker's challenge.
func AuthDigest(role string, connectionID, challenge []byte) []byte {
	digest := sha256.New()
	digest.Write([]byte("agentb-auth-v2"))
	digest.Write([]byte{WireVersion})
	digest.Write([]byte(role))
	digest.Write(connectionID)
	digest.Write(challenge)
	return digest.Sum(nil)
}

// SignAuth answers a CHALLENGE.
func SignAuth(identity Identity, role string, connectionID, challenge []byte) []byte {
	return ed25519.Sign(ed25519.NewKeyFromSeed(identity.SigningSeed), AuthDigest(role, connectionID, challenge))
}

// hexID renders a raw id the way every frame field does.
func hexID(raw []byte) string { return hex.EncodeToString(raw) }
