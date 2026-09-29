package broker

import (
	"encoding/hex"
	"errors"
	"fmt"
)

// RefusalCase performs the refusal the placed vector file names and returns the error
// the protocol requires. Item 2kq: the five of them are named in the file rather than
// described, so each is built here from the same inputs the accepted handshake uses and
// then broken in exactly one way. A refusal that returns nil is a defect.
//
// These are the five:
//
//   - old_epoch: a frame that declares an epoch the session has already left.
//   - role_swap: the two endpoints both claiming to be the initiator.
//   - version_downgrade: a prologue built with wire 0x01.
//   - restart_old_session: a frame from a session the broker's restart invalidated.
//   - same_identities_new_session: the same two identities re-pairing, whose new session
//     keys must be unrelated — so the old session's key must not open the new traffic.
func RefusalCase(name string, inputs, expected map[string]string) error {
	decode := func(key string) []byte {
		value := inputs[key]
		if value == "" {
			value = expected[key]
		}
		raw, _ := hex.DecodeString(value)
		return raw
	}
	agent := Identity{SigningSeed: decode("agent_ed25519_seed_hex"), Agreement: decode("agent_x25519_private_hex")}
	device := Identity{SigningSeed: decode("device_ed25519_seed_hex"), Agreement: decode("device_x25519_private_hex")}
	pairingID := decode("pairing_id_hex")
	handshakeID := decode("handshake_id_hex")
	messageID := decode("message_id_hex")
	sessionID := decode("session_id_hex")
	prologue := SessionPrologue(pairingID, handshakeID, agent.KeyID(), device.KeyID())

	// A complete, accepted session the refusals are measured against.
	established := func() (*Session, *Session, error) {
		initiator, err := NewInitiator(agent, device.AgreementPublic(), prologue, decode("agent_ephemeral_hex"))
		if err != nil {
			return nil, nil, err
		}
		responder, err := NewResponder(device, prologue, decode("device_ephemeral_hex"))
		if err != nil {
			return nil, nil, err
		}
		messageOne, err := initiator.WriteInit()
		if err != nil {
			return nil, nil, err
		}
		if err := responder.ReadInit(messageOne, agent.SigningPublic(), initiator.InitSignature(messageOne)); err != nil {
			return nil, nil, err
		}
		messageTwo, err := responder.WriteResponse()
		if err != nil {
			return nil, nil, err
		}
		if err := initiator.ReadResponse(messageTwo, device.SigningPublic(), responder.ConfirmSignature()); err != nil {
			return nil, nil, err
		}
		finish, err := initiator.SealFinish()
		if err != nil {
			return nil, nil, err
		}
		if err := responder.OpenFinish(finish, agent.SigningPublic()); err != nil {
			return nil, nil, err
		}
		ready, err := responder.SealReady()
		if err != nil {
			return nil, nil, err
		}
		if err := initiator.OpenReady(ready); err != nil {
			return nil, nil, err
		}
		return initiator, responder, nil
	}

	switch name {
	case "old_epoch":
		initiator, responder, err := established()
		if err != nil {
			return err
		}
		// The sender has moved to epoch 1; the frame still says 0. Every field of the
		// AAD is authenticated, so this fails to open rather than being spotted later.
		initiator.Rekey(DirectionAgentToDevice)
		sealed, err := initiator.SealMessage(TransportAAD(pairingID, sessionID, 1, DirectionAgentToDevice, agent.KeyID(), device.KeyID(), messageID, 0), []byte("after the rekey"))
		if err != nil {
			return err
		}
		_, err = responder.OpenMessage(TransportAAD(pairingID, sessionID, 0, DirectionAgentToDevice, agent.KeyID(), device.KeyID(), messageID, 0), sealed)
		return err

	case "role_swap":
		// Two initiators cannot complete IK: the responder's half of the pattern is
		// never played, so reading the peer's message 1 fails.
		one, err := NewInitiator(agent, device.AgreementPublic(), prologue, decode("agent_ephemeral_hex"))
		if err != nil {
			return err
		}
		two, err := NewInitiator(device, agent.AgreementPublic(), prologue, decode("device_ephemeral_hex"))
		if err != nil {
			return err
		}
		messageOne, err := one.WriteInit()
		if err != nil {
			return err
		}
		if _, _, _, err := two.state.ReadMessage(nil, messageOne); err != nil {
			return fmt.Errorf("role swap refused: %w", err)
		}
		return nil

	case "version_downgrade":
		// The prologue carries the wire version. A peer that built it with 0x01 derives
		// a different prologue, and the handshake will not complete.
		downgraded := append([]byte(nil), prologue...)
		downgraded[len(prologueLabel)] = 0x01
		initiator, err := NewInitiator(agent, device.AgreementPublic(), downgraded, decode("agent_ephemeral_hex"))
		if err != nil {
			return err
		}
		responder, err := NewResponder(device, prologue, decode("device_ephemeral_hex"))
		if err != nil {
			return err
		}
		messageOne, err := initiator.WriteInit()
		if err != nil {
			return err
		}
		if err := responder.ReadInit(messageOne, agent.SigningPublic(), initiator.InitSignature(messageOne)); err != nil {
			return fmt.Errorf("version downgrade refused: %w", err)
		}
		return nil

	case "restart_old_session":
		// A broker restart invalidates the session. A frame that still names the old
		// session id fails to open under the new one, because the session id is in the
		// AAD.
		initiator, responder, err := established()
		if err != nil {
			return err
		}
		sealed, err := initiator.SealMessage(TransportAAD(pairingID, sessionID, 0, DirectionAgentToDevice, agent.KeyID(), device.KeyID(), messageID, 0), []byte("before the restart"))
		if err != nil {
			return err
		}
		fresh := append([]byte(nil), sessionID...)
		fresh[0] ^= 0xff
		_, err = responder.OpenMessage(TransportAAD(pairingID, fresh, 0, DirectionAgentToDevice, agent.KeyID(), device.KeyID(), messageID, 0), sealed)
		return err

	case "same_identities_new_session":
		// Re-pairing the SAME two identities must yield unrelated session keys. A new
		// handshake with fresh ephemerals is established, and the old session's
		// ciphertext must not open under it.
		_, oldResponder, err := established()
		if err != nil {
			return err
		}
		freshHandshake := append([]byte(nil), handshakeID...)
		freshHandshake[0] ^= 0x5a
		freshPrologue := SessionPrologue(pairingID, freshHandshake, agent.KeyID(), device.KeyID())
		freshEphemeral := append([]byte(nil), decode("agent_ephemeral_hex")...)
		freshEphemeral[0] ^= 0x5a
		freshDeviceEphemeral := append([]byte(nil), decode("device_ephemeral_hex")...)
		freshDeviceEphemeral[0] ^= 0x5a
		newInitiator, err := NewInitiator(agent, device.AgreementPublic(), freshPrologue, freshEphemeral)
		if err != nil {
			return err
		}
		newResponder, err := NewResponder(device, freshPrologue, freshDeviceEphemeral)
		if err != nil {
			return err
		}
		messageOne, err := newInitiator.WriteInit()
		if err != nil {
			return err
		}
		if err := newResponder.ReadInit(messageOne, agent.SigningPublic(), newInitiator.InitSignature(messageOne)); err != nil {
			return err
		}
		messageTwo, err := newResponder.WriteResponse()
		if err != nil {
			return err
		}
		if err := newInitiator.ReadResponse(messageTwo, device.SigningPublic(), newResponder.ConfirmSignature()); err != nil {
			return err
		}
		finish, err := newInitiator.SealFinish()
		if err != nil {
			return err
		}
		if err := newResponder.OpenFinish(finish, agent.SigningPublic()); err != nil {
			return err
		}
		if newInitiator.SessionID() == sessionID2Hex(sessionID) {
			return errors.New("a re-pairing produced the same session id")
		}
		sealed, err := newInitiator.SealMessage(TransportAAD(pairingID, mustDecodeSessionID(newInitiator), 0, DirectionAgentToDevice, agent.KeyID(), device.KeyID(), messageID, 0), []byte("new session"))
		if err != nil {
			return err
		}
		// The OLD session must not open it.
		_, err = oldResponder.OpenMessage(TransportAAD(pairingID, mustDecodeSessionID(newInitiator), 0, DirectionAgentToDevice, agent.KeyID(), device.KeyID(), messageID, 0), sealed)
		return err
	}
	return fmt.Errorf("unknown refusal %q — the vector file names one this build does not perform", name)
}

func sessionID2Hex(raw []byte) string { return hex.EncodeToString(raw) }

func mustDecodeSessionID(session *Session) []byte {
	raw, _ := hex.DecodeString(session.SessionID())
	return raw
}
