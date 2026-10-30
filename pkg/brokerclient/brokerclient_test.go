package brokerclient_test

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	broker "harness/pkg/brokerclient"
)

// Item 2ww: a phone opens the broker's push vector boxes through the public names
// alone, and refuses one opened under the wrong pairing id.
func TestThePushVectorsOpenThroughThePublicNames2ww(t *testing.T) {
	// The server's push vectors and refusals, copied whole (item 2wx) so this runs in CI.
	raw, err := os.ReadFile(filepath.Join("testdata", "push-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Inputs       map[string]string `json:"inputs"`
		PushRefusals []string          `json:"push_refusals"`
		PushVectors  []struct {
			Box    string `json:"box_base64url"`
			ChatID string `json:"chat_id"`
			Kind   string `json:"kind"`
			Notice string `json:"notice"`
		} `json:"push_vectors"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	agreement, _ := hex.DecodeString(file.Inputs["device_x25519_private_hex"])
	pairing, _ := hex.DecodeString(file.Inputs["pairing_id_hex"])
	if len(file.PushVectors) != len(broker.PushKinds) || !slices.Equal(file.PushRefusals, []string{"push_wrong_pairing_id"}) {
		t.Fatalf("%d push vectors, refusals %v", len(file.PushVectors), file.PushRefusals)
	}
	for _, vector := range file.PushVectors {
		box, err := base64.RawURLEncoding.DecodeString(vector.Box)
		if err != nil {
			t.Fatal(err)
		}
		opened, err := broker.OpenPush(agreement, pairing, box)
		if err != nil || opened != (broker.PushPayload{Kind: vector.Kind, ChatID: vector.ChatID, Notice: vector.Notice}) {
			t.Fatalf("%s opened as %+v (%v)", vector.Kind, opened, err)
		}
		wrong := append([]byte(nil), pairing...)
		wrong[0] ^= 0xff
		if _, err := broker.OpenPush(agreement, wrong, box); err == nil {
			t.Fatalf("%s opened under the wrong pairing id", vector.Kind)
		}
	}
	// The device's own registration is reached through the public alias.
	var _ func(*broker.DeviceClient, string, string) error = (*broker.DeviceClient).RegisterPush
	var _ func(*broker.DeviceClient, string) error = (*broker.DeviceClient).RegisterPushFID
	if broker.FramePushRegister != 0x23 {
		t.Fatalf("FramePushRegister = %#x", broker.FramePushRegister)
	}
}
