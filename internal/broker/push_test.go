package broker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// Item 2kq (d): the push the broker forwards blind.

func TestThePushTheBrokerForwardsIsSealedToTheDevice2kq(t *testing.T) {
	agent, device, pairing := testPair(t)
	broker := newScriptedBroker(t, agent, device, pairing)
	client := NewClient(agent, pairing, func(context.Context) (Transport, error) { return broker, nil }, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { _ = client.Run(ctx) }()
	broker.handshake(t)
	waitConnected(t, client)

	messageID := make([]byte, 16)
	if _, err := rand.Read(messageID); err != nil {
		t.Fatal(err)
	}
	if err := client.Push(broker, messageID, "approval_required", "0123456789abcdef0123456789abcdef", "agent_b needs an answer"); err != nil {
		t.Fatal(err)
	}
	frame := broker.next(t)
	if frame.Type != FramePush {
		t.Fatalf("expected PUSH, got 0x%02x", frame.Type)
	}
	var payload struct {
		PairingID      string `json:"pairing_id"`
		RecipientKeyID string `json:"recipient_key_id"`
		MessageID      string `json:"message_id"`
		Ciphertext     string `json:"ciphertext"`
	}
	if err := DecodeInto(frame.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	// WHAT THE BROKER CAN SEE: routing ids and a base64url string. Nothing else.
	text := string(frame.Payload)
	for _, leak := range []string{"approval_required", "agent_b needs an answer", "0123456789abcdef0123456789abcdef"} {
		if strings.Contains(text, leak) {
			t.Errorf("the push frame carries %q in the clear", leak)
		}
	}
	// And the device opens it, because it is sealed to the device's own static key.
	box, err := base64.RawURLEncoding.DecodeString(payload.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := OpenPush(device.Agreement, pairing.PairingID, box)
	if err != nil {
		t.Fatalf("the device could not open the push: %v", err)
	}
	if opened.Kind != "approval_required" || opened.Notice != "agent_b needs an answer" {
		t.Fatalf("opened = %+v", opened)
	}
}

func TestNotifyPushDoesNotRequirePhoneSession2pn(t *testing.T) {
	agent, device, pairing := testPair(t)
	transport := newScriptedBroker(t, agent, device, pairing)
	client := NewClient(agent, pairing, nil, nil)
	client.mu.Lock()
	client.transport = transport
	client.mu.Unlock()
	events := make(chan string, 3)
	client.OnSessionEvent(func(event string) { events <- event })

	cases := []struct {
		kind, answer string
		frame        byte
		payload      any
	}{
		{"approval_required", "no_token", FrameError, errorPayload{Code: "malformed", Detail: "recipient_no_token", Fatal: false}},
		{"run_stopped", "accepted", FramePushAccepted, nil},
		{"item_stuck", "refused", FrameError, errorPayload{Code: "push_provider", Detail: "named reason", Fatal: false}},
	}
	for _, test := range cases {
		if err := client.Notify(test.kind, strings.Repeat("a", 32), "PLANTED notice must not enter the log"); err != nil {
			t.Fatal(err)
		}
		_ = transport.next(t)
		transport.push(test.frame, test.payload)
		transport.push(FrameSessionResp, sessionFrame{})
		if frame, err := client.readHandshake(context.Background(), transport); err != nil || frame.Type != FrameSessionResp {
			t.Fatalf("the nonfatal push answer ended the phone handshake: %v", err)
		}
		event := <-events
		want := "PUSH kind=" + test.kind + " answer=" + test.answer
		if event != want {
			t.Fatalf("push event = %q, want %q", event, want)
		}
		for _, secret := range []string{"PLANTED", "named reason", strings.Repeat("a", 9)} {
			if strings.Contains(event, secret) {
				t.Fatalf("push event leaked %q: %q", secret, event)
			}
		}
	}
}

func TestAPushIsOneLineAndOneOfThreeKinds2kq(t *testing.T) {
	agent, device, pairing := testPair(t)
	broker := newScriptedBroker(t, agent, device, pairing)
	client := NewClient(agent, pairing, func(context.Context) (Transport, error) { return broker, nil }, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { _ = client.Run(ctx) }()
	broker.handshake(t)
	waitConnected(t, client)

	messageID := make([]byte, 16)
	if _, err := rand.Read(messageID); err != nil {
		t.Fatal(err)
	}
	// A kind outside the three is refused rather than sent and discarded at the phone.
	if err := client.Push(broker, messageID, "something_else", "0123456789abcdef0123456789abcdef", "x"); err == nil {
		t.Fatal("a push of an unknown kind was sent")
	}
	// A long notice with line breaks becomes one line within the limit.
	notice := strings.Repeat("a long line of notice text ", 20) + "\r\nand a second line"
	if err := client.Push(broker, messageID, "run_stopped", "0123456789abcdef0123456789abcdef", notice); err != nil {
		t.Fatal(err)
	}
	frame := broker.next(t)
	var payload pushPayload
	if err := DecodeInto(frame.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	box, err := base64.RawURLEncoding.DecodeString(payload.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := OpenPush(device.Agreement, pairing.PairingID, box)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Notice) > NoticeLimit {
		t.Fatalf("the notice is %d bytes, over the %d limit", len(opened.Notice), NoticeLimit)
	}
	if strings.ContainsAny(opened.Notice, "\r\n") {
		t.Fatal("the notice carries a line break")
	}
}

func TestTheDeviceTokenIsRelayedAndNotKept2kq(t *testing.T) {
	agent, device, pairing := testPair(t)
	broker := newScriptedBroker(t, agent, device, pairing)
	client := NewClient(agent, pairing, func(context.Context) (Transport, error) { return broker, nil }, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { _ = client.Run(ctx) }()
	broker.handshake(t)
	waitConnected(t, client)

	token := strings.Repeat("ab", 32)
	if err := client.RegisterPush(broker, token); err != nil {
		t.Fatal(err)
	}
	frame := broker.next(t)
	if frame.Type != FramePushRegister {
		t.Fatalf("expected PUSH_REGISTER, got 0x%02x", frame.Type)
	}
	var payload pushRegisterPayload
	if err := DecodeInto(frame.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.DeviceToken != token {
		t.Fatalf("the relayed token is %q", payload.DeviceToken)
	}
	// A token of the wrong shape never leaves this machine.
	if err := client.RegisterPush(broker, "short"); err == nil {
		t.Fatal("a malformed device token was relayed")
	}
	_ = device
}
