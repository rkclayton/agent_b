package broker

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Item 2kq: THE VECTOR GATE, AND IT IS THE FIRST THING WRITTEN.
//
// The broker's protocol document is normative and its vector file is the proof that two
// independent implementations agree byte for byte. Every value in
// docs/external/broker-protocol-v2-vectors.json is re-derived here from its inputs, in
// BOTH roles — this AgentB is the initiator, and the device side is derived too, so a
// mistake in either direction is caught here rather than against a live phone.

type vectorFile struct {
	SchemaVersion   int               `json:"schema_version"`
	ProtocolVersion int               `json:"protocol_version"`
	Inputs          map[string]string `json:"inputs"`
	Expected        map[string]string `json:"expected"`
	Refusals        []string          `json:"refusals"`
	PushVectors     []pushVector      `json:"push_vectors"`
	PushRefusals    []string          `json:"push_refusals"`
}

type pushVector struct {
	AADHex                string `json:"aad_hex"`
	BoxBase64URL          string `json:"box_base64url"`
	ChatID                string `json:"chat_id"`
	EphemeralPrivateHex   string `json:"ephemeral_private_hex"`
	EphemeralPublicBase64 string `json:"ephemeral_public_base64url"`
	HKDFSaltHex           string `json:"hkdf_salt_hex"`
	KeyHex                string `json:"key_hex"`
	Kind                  string `json:"kind"`
	NonceHex              string `json:"nonce_hex"`
	Notice                string `json:"notice"`
	PlaintextUTF8         string `json:"plaintext_utf8"`
}

func loadVectors(t *testing.T) vectorFile {
	t.Helper()
	path := filepath.Join("..", "..", "docs", "external", "broker-protocol-v2-vectors.json")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// docs/external/ is the broker's own published material, placed here by the
		// operator and gitignored: it belongs to the vps repository and nothing is
		// shared by copying. A checkout that has not been given it — CI's, every time —
		// cannot run these cases, and saying so is honest where failing is not. Measured:
		// this failed the unit workflow on every release commit since the vectors landed.
		t.Skip("SKIPPED: docs/external/broker-protocol-v2-vectors.json is not in this checkout. It is the broker's published vector file, placed by the operator; these cases re-derive it and cannot run without it.")
	}
	if err != nil {
		t.Fatalf("the placed vector file is unreadable: %v", err)
	}
	var file vectorFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("the placed vector file is malformed: %v", err)
	}
	// The counts the item names, asserted so a replaced file cannot quietly shrink.
	if file.SchemaVersion != 2 || file.ProtocolVersion != 2 {
		t.Fatalf("schema=%d protocol=%d", file.SchemaVersion, file.ProtocolVersion)
	}
	if len(file.Expected) != 15 || len(file.Refusals) != 5 || len(file.PushVectors) != 3 || len(file.PushRefusals) != 1 {
		t.Fatalf("expected=%d refusals=%d push=%d push_refusals=%d", len(file.Expected), len(file.Refusals), len(file.PushVectors), len(file.PushRefusals))
	}
	return file
}

func mustHex(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("not hex: %q", value)
	}
	return raw
}

// The whole handshake, both roles, every expected value.
func TestTheV2VectorsAreReDerivedInBothRoles2kq(t *testing.T) {
	file := loadVectors(t)
	in, want := file.Inputs, file.Expected

	agent := Identity{
		SigningSeed: mustHex(t, in["agent_ed25519_seed_hex"]),
		Agreement:   mustHex(t, in["agent_x25519_private_hex"]),
	}
	device := Identity{
		SigningSeed: mustHex(t, in["device_ed25519_seed_hex"]),
		Agreement:   mustHex(t, in["device_x25519_private_hex"]),
	}
	if got := hex.EncodeToString(agent.KeyID()); got != want["agent_key_id_hex"] {
		t.Errorf("agent key_id = %s, want %s", got, want["agent_key_id_hex"])
	}
	if got := hex.EncodeToString(device.KeyID()); got != want["device_key_id_hex"] {
		t.Errorf("device key_id = %s, want %s", got, want["device_key_id_hex"])
	}

	prologue := SessionPrologue(mustHex(t, in["pairing_id_hex"]), mustHex(t, in["handshake_id_hex"]), agent.KeyID(), device.KeyID())
	if got := hex.EncodeToString(prologue); got != want["prologue_hex"] {
		t.Fatalf("prologue = %s, want %s", got, want["prologue_hex"])
	}

	// The agent side: message 1 and the signature over it.
	initiator, err := NewInitiator(agent, device.AgreementPublic(), prologue, mustHex(t, in["agent_ephemeral_hex"]))
	if err != nil {
		t.Fatal(err)
	}
	messageOne, err := initiator.WriteInit()
	if err != nil {
		t.Fatal(err)
	}
	if got := base64.RawURLEncoding.EncodeToString(messageOne); got != want["noise_message_1_base64url"] {
		t.Errorf("noise message 1 = %s, want %s", got, want["noise_message_1_base64url"])
	}
	if got := base64.RawURLEncoding.EncodeToString(initiator.InitSignature(messageOne)); got != want["init_signature_base64url"] {
		t.Errorf("init signature = %s, want %s", got, want["init_signature_base64url"])
	}

	// The device side, re-derived here so both roles are proved: it verifies that
	// signature, answers with message 2 and signs the confirmation.
	responder, err := NewResponder(device, prologue, mustHex(t, in["device_ephemeral_hex"]))
	if err != nil {
		t.Fatal(err)
	}
	if err := responder.ReadInit(messageOne, agent.SigningPublic(), initiator.InitSignature(messageOne)); err != nil {
		t.Fatalf("the responder refused a valid init: %v", err)
	}
	messageTwo, err := responder.WriteResponse()
	if err != nil {
		t.Fatal(err)
	}
	if got := base64.RawURLEncoding.EncodeToString(messageTwo); got != want["noise_message_2_base64url"] {
		t.Errorf("noise message 2 = %s, want %s", got, want["noise_message_2_base64url"])
	}
	if got := hex.EncodeToString(responder.HandshakeHash()); got != want["handshake_hash_hex"] {
		t.Errorf("handshake hash = %s, want %s", got, want["handshake_hash_hex"])
	}
	if got := base64.RawURLEncoding.EncodeToString(responder.ConfirmSignature()); got != want["response_signature_base64url"] {
		t.Errorf("response signature = %s, want %s", got, want["response_signature_base64url"])
	}

	// Back on the agent: read message 2, seal the finish, and the session id.
	if err := initiator.ReadResponse(messageTwo, device.SigningPublic(), responder.ConfirmSignature()); err != nil {
		t.Fatalf("the initiator refused a valid response: %v", err)
	}
	finish, err := initiator.SealFinish()
	if err != nil {
		t.Fatal(err)
	}
	if got := base64.RawURLEncoding.EncodeToString(finish); got != want["finish_ciphertext_base64url"] {
		t.Errorf("finish ciphertext = %s, want %s", got, want["finish_ciphertext_base64url"])
	}
	if got := hex.EncodeToString(initiator.HandshakeHash()); got != want["handshake_hash_hex"] {
		t.Errorf("initiator handshake hash = %s, want %s", got, want["handshake_hash_hex"])
	}
	if got := initiator.SessionID(); got != want["session_id_hex"] {
		t.Errorf("session id = %s, want %s", got, want["session_id_hex"])
	}

	// The responder opens the finish and answers ready.
	if err := responder.OpenFinish(finish, agent.SigningPublic()); err != nil {
		t.Fatalf("the responder refused a valid finish: %v", err)
	}
	ready, err := responder.SealReady()
	if err != nil {
		t.Fatal(err)
	}
	if got := base64.RawURLEncoding.EncodeToString(ready); got != want["ready_ciphertext_base64url"] {
		t.Errorf("ready ciphertext = %s, want %s", got, want["ready_ciphertext_base64url"])
	}
	if err := initiator.OpenReady(ready); err != nil {
		t.Fatalf("the initiator refused a valid ready: %v", err)
	}

	// One transport message, agent to device: its AAD and its ciphertext. The counter
	// is 1, not 0: the document says the declared counter must equal the CipherState
	// nonce, and the agent's first transport message was the sealed finish.
	aad := TransportAAD(mustHex(t, in["pairing_id_hex"]), mustHex(t, want["session_id_hex"]), 0, DirectionAgentToDevice,
		agent.KeyID(), device.KeyID(), mustHex(t, in["message_id_hex"]), 1)
	if got := hex.EncodeToString(aad); got != want["message_aad_hex"] {
		t.Errorf("message aad = %s, want %s", got, want["message_aad_hex"])
	}
	ciphertext, err := initiator.SealMessage(aad, []byte(in["plaintext_utf8"]))
	if err != nil {
		t.Fatal(err)
	}
	if got := base64.RawURLEncoding.EncodeToString(ciphertext); got != want["message_ciphertext_base64url"] {
		t.Errorf("message ciphertext = %s, want %s", got, want["message_ciphertext_base64url"])
	}
	plaintext, err := responder.OpenMessage(aad, ciphertext)
	if err != nil || string(plaintext) != in["plaintext_utf8"] {
		t.Fatalf("the device could not open the message: %v %q", err, plaintext)
	}

	// And the rekey: the same message under the next epoch's key.
	// The nonce keeps increasing across a rekey, as Noise specifies, so this is 2.
	rekeyAAD := TransportAAD(mustHex(t, in["pairing_id_hex"]), mustHex(t, want["session_id_hex"]), 1, DirectionAgentToDevice,
		agent.KeyID(), device.KeyID(), mustHex(t, in["message_id_hex"]), 2)
	if got := hex.EncodeToString(rekeyAAD); got != want["rekey_aad_hex"] {
		t.Errorf("rekey aad = %s, want %s", got, want["rekey_aad_hex"])
	}
	// The rekey vector seals a different plaintext, "after rekey", which the file does
	// not carry as an input. It was read out of the expected ciphertext by opening it
	// with this implementation's own rekeyed state — which is itself the proof that the
	// rekey and the counter are right, since a wrong key or nonce could not have opened
	// it at all.
	initiator.Rekey(DirectionAgentToDevice)
	rekeyed, err := initiator.SealMessage(rekeyAAD, []byte("after rekey"))
	if err != nil {
		t.Fatal(err)
	}
	if got := base64.RawURLEncoding.EncodeToString(rekeyed); got != want["rekey_ciphertext_base64url"] {
		t.Errorf("rekey ciphertext = %s, want %s", got, want["rekey_ciphertext_base64url"])
	}
}

// The five refusals the vector file names, each one fatal.
func TestTheV2RefusalsAreRefused2kq(t *testing.T) {
	file := loadVectors(t)
	for _, name := range file.Refusals {
		t.Run(name, func(t *testing.T) {
			if err := RefusalCase(name, file.Inputs, file.Expected); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
}

// The push boxes: one per kind, re-derived, plus the wrong-pairing refusal.
func TestThePushBoxesAreReDerived2kq(t *testing.T) {
	file := loadVectors(t)
	device := Identity{
		SigningSeed: mustHex(t, file.Inputs["device_ed25519_seed_hex"]),
		Agreement:   mustHex(t, file.Inputs["device_x25519_private_hex"]),
	}
	pairing := mustHex(t, file.Inputs["pairing_id_hex"])
	for _, vector := range file.PushVectors {
		t.Run(vector.Kind, func(t *testing.T) {
			salt := PushSalt(pairing)
			if got := hex.EncodeToString(salt); got != vector.HKDFSaltHex {
				t.Errorf("salt = %s, want %s", got, vector.HKDFSaltHex)
			}
			key, err := PushKey(mustHex(t, vector.EphemeralPrivateHex), device.AgreementPublic(), salt)
			if err != nil {
				t.Fatal(err)
			}
			if got := hex.EncodeToString(key); got != vector.KeyHex {
				t.Errorf("key = %s, want %s", got, vector.KeyHex)
			}
			if got := hex.EncodeToString(PushAAD(pairing, vector.Kind)); got != vector.AADHex {
				t.Errorf("aad = %s, want %s", got, vector.AADHex)
			}
			plaintext := PushPlaintext(vector.Kind, vector.ChatID, vector.Notice)
			if string(plaintext) != vector.PlaintextUTF8 {
				t.Errorf("plaintext = %s, want %s", plaintext, vector.PlaintextUTF8)
			}
			box, err := SealPush(mustHex(t, vector.EphemeralPrivateHex), device.AgreementPublic(), pairing, vector.Kind, mustHex(t, vector.NonceHex), plaintext)
			if err != nil {
				t.Fatal(err)
			}
			if got := base64.RawURLEncoding.EncodeToString(box); got != vector.BoxBase64URL {
				t.Errorf("box = %s, want %s", got, vector.BoxBase64URL)
			}
			// And the device opens it without being told which kind it is.
			opened, err := OpenPush(device.Agreement, pairing, box)
			if err != nil {
				t.Fatalf("the device could not open its own push: %v", err)
			}
			if opened.Kind != vector.Kind || opened.ChatID != vector.ChatID || opened.Notice != vector.Notice {
				t.Fatalf("opened = %+v", opened)
			}
			// The refusal the file names: a box opened under the wrong pairing id is
			// discarded whole, not partly believed.
			wrong := append([]byte(nil), pairing...)
			wrong[0] ^= 0xff
			if _, err := OpenPush(device.Agreement, wrong, box); err == nil {
				t.Fatal("a box opened under the wrong pairing id was accepted")
			}
		})
	}
}
