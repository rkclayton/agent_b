package broker

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Item 2kq (d): PUSH THROUGH THE BROKER.
//
// The phone's device token arrives over the session and is relayed with PUSH_REGISTER.
// The broker forwards a push BLIND: the payload is a sealed box to the device's own
// static key, and the broker holds no key that opens it. The three kinds are the set
// item 2kl already established; a push is a wake hint and carries no state.

type pushRegisterPayload struct {
	PairingID   string `json:"pairing_id"`
	DeviceToken string `json:"device_token"`
}

type pushPayload struct {
	PairingID      string `json:"pairing_id"`
	RecipientKeyID string `json:"recipient_key_id"`
	MessageID      string `json:"message_id"`
	Ciphertext     string `json:"ciphertext"`
}

// NoticeLimit is the document's cap on the one line a push carries.
const NoticeLimit = 200

// RegisterPush relays the token the device sent. The token is the phone's, not this
// machine's, and nothing here keeps it: it is forwarded and forgotten.
func (c *Client) RegisterPush(transport Transport, deviceToken string) error {
	if len(deviceToken) != 64 {
		return fmt.Errorf("push register: the token is %d characters, want 64", len(deviceToken))
	}
	frame, err := Encode(FramePushRegister, pushRegisterPayload{
		PairingID:   hexID(c.pairing.PairingID),
		DeviceToken: strings.ToLower(deviceToken),
	})
	if err != nil {
		return err
	}
	if err := transport.Send(frame); err != nil {
		return err
	}
	return nil
}

// Push seals one notice to the device and sends it. The kind is one of the three fixed
// values; a notice is one line and at most 200 bytes, because the complete APNs payload
// has to stay under its own ceiling and because a push is a hint, not a transcript.
func (c *Client) Push(transport Transport, messageID []byte, kind, chatID, notice string) error {
	if !validPushKind(kind) {
		return fmt.Errorf("push: %q is not one of the three kinds", kind)
	}
	notice = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return ' '
		}
		return r
	}, notice)
	for len(notice) > NoticeLimit {
		// Trimmed on a rune boundary: the limit is bytes and the text is UTF-8.
		_, size := utf8.DecodeLastRuneInString(notice)
		notice = notice[:len(notice)-size]
	}
	ephemeral := make([]byte, 32)
	if _, err := rand.Read(ephemeral); err != nil {
		return err
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	box, err := SealPush(ephemeral, c.pairing.DeviceAgreement, c.pairing.PairingID, kind, nonce, PushPlaintext(kind, chatID, notice))
	if err != nil {
		return err
	}
	frame, err := Encode(FramePush, pushPayload{
		PairingID:      hexID(c.pairing.PairingID),
		RecipientKeyID: hexID(c.pairing.DeviceKeyID),
		MessageID:      hexID(messageID),
		Ciphertext:     base64.RawURLEncoding.EncodeToString(box),
	})
	if err != nil {
		return err
	}
	if err := transport.Send(frame); err != nil {
		return err
	}
	return nil
}

func validPushKind(kind string) bool {
	for _, known := range PushKinds {
		if kind == known {
			return true
		}
	}
	return false
}
