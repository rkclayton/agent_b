//go:build windows

package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/broker"
	"harness/internal/credential"
)

type persistenceTransport struct {
	sent chan []byte
}

type pairingLogTransport struct {
	incoming chan []byte
	onSend   func(broker.Frame)
}

func (p *pairingLogTransport) Send(raw []byte) error {
	frame, err := broker.Decode(raw)
	if err == nil {
		p.onSend(frame)
	}
	return err
}
func (p *pairingLogTransport) Receive(ctx context.Context) ([]byte, error) {
	select {
	case raw := <-p.incoming:
		return raw, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (*pairingLogTransport) Close(int, string) error { return nil }

func encodedFrame(t *testing.T, kind byte, payload any) []byte {
	t.Helper()
	raw, err := broker.Encode(kind, payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (p *persistenceTransport) Send(frame []byte) error {
	p.sent <- append([]byte(nil), frame...)
	return nil
}

func (*persistenceTransport) Receive(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*persistenceTransport) Close(int, string) error { return nil }

func persistedTestPairing() broker.Pairing {
	return broker.Pairing{
		PairingID:       bytes.Repeat([]byte{0x11}, 16),
		DeviceKeyID:     bytes.Repeat([]byte{0x22}, 16),
		DeviceSigning:   bytes.Repeat([]byte{0x33}, 32),
		DeviceAgreement: bytes.Repeat([]byte{0x44}, 32),
	}
}

func TestPairedAgentReconnectsAfterInstallerRelaunch2ob2qp(t *testing.T) {
	root := os.Getenv("AGENTB_BROKER_EVIDENCE_ROOT")
	if root == "" {
		root = t.TempDir()
	} else if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := NewBrokerClient(broker.DefaultURL, root)
	if err != nil {
		t.Fatal(err)
	}
	firstKey := append([]byte(nil), first.IdentityKey()...)
	pairing := persistedTestPairing()
	if err := first.savePairing(pairing); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewBrokerClient(broker.DefaultURL, root)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restarted.IdentityKey(), firstKey) {
		t.Fatalf("identity changed across restart: %x then %x", firstKey, restarted.IdentityKey())
	}
	fresh, err := NewBrokerClient(broker.DefaultURL, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(fresh.IdentityKey(), firstKey) {
		t.Fatal("a fresh data root reused the stored identity")
	}

	dialed := make(chan struct{}, 1)
	restarted.dial = func(context.Context) (broker.Transport, error) {
		dialed <- struct{}{}
		return nil, errors.New("scripted stop after restored session dial")
	}
	server, _, writers, _, cfg, _ := consoleServer(t)
	defer writers.Close()
	cfg.Broker.URL = broker.DefaultURL
	server.SetBrokerHost(restarted)
	select {
	case <-dialed:
	case <-time.After(5 * time.Second):
		t.Fatal("the restored pairing did not start a session when the real server attached")
	}

	request := httptest.NewRequest("GET", "/api/broker/status", nil)
	response := httptest.NewRecorder()
	server.brokerStatus(response, request)
	if body := response.Body.String(); !bytes.Contains([]byte(body), []byte(`"paired_device":"phone"`)) {
		t.Fatalf("Settings did not retain the paired state after restart: %s", body)
	}
	restarted.mu.Lock()
	restarted.stopSessionLocked()
	restarted.mu.Unlock()

	for _, name := range []string{"broker-identity", "broker-pairing"} {
		store, err := credential.NewNamed(root, name)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(store.Path())
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range [][]byte{first.identity.SigningSeed, first.identity.Agreement, pairing.PairingID, pairing.DeviceSigning, pairing.DeviceAgreement} {
			if bytes.Contains(raw, secret) || bytes.Contains(raw, []byte(hex.EncodeToString(secret))) || bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString(secret))) {
				t.Fatalf("%s exposes broker key material instead of a protected blob", name)
			}
		}
	}
}

func TestRevokeDeletesOnlyTheStoredPairingAndNextStartDialsNothing2ob(t *testing.T) {
	root := t.TempDir()
	client, err := NewBrokerClient(broker.DefaultURL, root)
	if err != nil {
		t.Fatal(err)
	}
	identityKey := append([]byte(nil), client.IdentityKey()...)
	if err := client.savePairing(persistedTestPairing()); err != nil {
		t.Fatal(err)
	}
	client, err = NewBrokerClient(broker.DefaultURL, root)
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan []byte, 1)
	client.dial = func(context.Context) (broker.Transport, error) { return &persistenceTransport{sent: sent}, nil }
	if err := client.RevokePairing(); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-sent:
		decoded, err := broker.Decode(frame)
		if err != nil || decoded.Type != broker.FrameRevoke {
			t.Fatalf("revoke frame = %#v, %v", decoded, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revoke sent no broker frame")
	}

	pairingStore, _ := credential.NewNamed(root, "broker-pairing")
	if _, err := pairingStore.Read(); !errors.Is(err, credential.ErrNotStored) {
		t.Fatalf("stored pairing survived revoke: %v", err)
	}
	restarted, err := NewBrokerClient(broker.DefaultURL, root)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restarted.IdentityKey(), identityKey) {
		t.Fatal("revoke deleted the stored identity")
	}
	dials := make(chan struct{}, 1)
	restarted.dial = func(context.Context) (broker.Transport, error) {
		dials <- struct{}{}
		return nil, errors.New("unexpected dial")
	}
	server, _, writers, _, cfg, _ := consoleServer(t)
	defer writers.Close()
	cfg.Broker.URL = broker.DefaultURL
	server.SetBrokerHost(restarted)
	select {
	case <-dials:
		t.Fatal("a restart dialed after the pairing was revoked")
	case <-time.After(150 * time.Millisecond):
	}
}

func TestPairingRefusalIsShownAndLoggedWithoutSecrets2op(t *testing.T) {
	root := t.TempDir()
	client, err := NewBrokerClient(broker.DefaultURL, root)
	if err != nil {
		t.Fatal(err)
	}
	transport := &pairingLogTransport{incoming: make(chan []byte, 4)}
	transport.onSend = func(frame broker.Frame) {
		if frame.Type == broker.FramePairBegin {
			transport.incoming <- encodedFrame(t, broker.FrameError, map[string]any{"code": "malformed", "detail": "pairing_id", "fatal": true})
		}
	}
	client.dial = func(context.Context) (broker.Transport, error) { return transport, nil }
	offer, err := client.BeginPairing()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for client.Status().LastError == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	status := client.Status()
	if status.LastError != "refused by the broker — malformed: pairing_id" {
		t.Fatalf("status = %+v", status)
	}
	if status.LogPath != filepath.Join(root, "logs", "pairing.log") {
		t.Fatalf("log path = %q", status.LogPath)
	}
	_ = client.CancelPairing()
	logBytes, err := os.ReadFile(status.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(logBytes)), "\n")
	if len(lines) != 4 {
		t.Fatalf("log has %d lines, want 4:\n%s", len(lines), logBytes)
	}
	for _, want := range []string{"started", "sent PAIR_BEGIN", "received ERROR malformed: pairing_id", "outcome refused by the broker — malformed: pairing_id"} {
		if !strings.Contains(string(logBytes), want) {
			t.Fatalf("log lacks %q:\n%s", want, logBytes)
		}
	}
	if strings.Contains(string(logBytes), offer.Code) || strings.Contains(string(logBytes), "agentb://") {
		t.Fatalf("log exposes a code or link:\n%s", logBytes)
	}
}

func TestSuccessfulPairingWritesItsFrameSequenceAndOutcome2op(t *testing.T) {
	root := t.TempDir()
	client, err := NewBrokerClient(broker.DefaultURL, root)
	if err != nil {
		t.Fatal(err)
	}
	deviceSeed := bytes.Repeat([]byte{0x51}, 32)
	deviceAgreement := bytes.Repeat([]byte{0x52}, 32)
	device := broker.Identity{SigningSeed: deviceSeed, Agreement: deviceAgreement}
	pairingID := bytes.Repeat([]byte{0x53}, 16)
	transport := &pairingLogTransport{incoming: make(chan []byte, 8)}
	transport.onSend = func(frame broker.Frame) {
		switch frame.Type {
		case broker.FramePairBegin:
			transport.incoming <- encodedFrame(t, broker.FramePairWaiting, map[string]any{"expires_at": "2026-09-30T16:00:00Z"})
			transport.incoming <- encodedFrame(t, broker.FramePairPeer, map[string]any{"pairing_id": hex.EncodeToString(pairingID), "peer_key_id": hex.EncodeToString(device.KeyID()), "peer_ed25519_public": base64.RawURLEncoding.EncodeToString(device.SigningPublic()), "peer_x25519_public": base64.RawURLEncoding.EncodeToString(device.AgreementPublic())})
		case broker.FramePairConfirm:
			transport.incoming <- encodedFrame(t, broker.FramePairComplete, map[string]any{"pairing_id": hex.EncodeToString(pairingID)})
		}
	}
	client.dial = func(context.Context) (broker.Transport, error) { return transport, nil }
	offer, err := client.BeginPairing()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		current, _ := client.PairingOffer()
		if current.Fingerprint != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fingerprint was not offered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := client.ConfirmPairing(); err != nil {
		t.Fatal(err)
	}
	for client.Status().PairedDevice == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	logBytes, err := os.ReadFile(filepath.Join(root, "logs", "pairing.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sent PAIR_BEGIN", "received PAIR_WAITING", "received PAIR_PEER", "sent PAIR_CONFIRM", "received PAIR_COMPLETE", "outcome paired"} {
		if !strings.Contains(string(logBytes), want) {
			t.Fatalf("log lacks %q:\n%s", want, logBytes)
		}
	}
	for _, secret := range []string{offer.Code, hex.EncodeToString(pairingID), base64.RawURLEncoding.EncodeToString(device.SigningPublic())} {
		if strings.Contains(string(logBytes), secret) {
			t.Fatalf("log exposes pairing material %q:\n%s", secret, logBytes)
		}
	}
}

func TestSessionLogIsRedactedAndRotatesOnce2p0(t *testing.T) {
	root := t.TempDir()
	client := &BrokerClient{pairingLog: filepath.Join(root, "logs", "pairing.log")}
	pairing := broker.Pairing{PairingID: bytes.Repeat([]byte{0x53}, 16)}
	for _, event := range []string{"connected", "PEER_QUERY sent", "PEER received connected=true", "PUSH kind=approval_required answer=no_token", "disconnected reason=EOF", "reconnect attempt backoff=1s", "connected"} {
		client.recordSession(pairing, event)
	}
	logBytes, err := os.ReadFile(client.pairingLog)
	if err != nil || strings.Count(string(logBytes), "session 53535353") != 7 || !strings.Contains(string(logBytes), "PUSH kind=approval_required answer=no_token") || !strings.HasSuffix(strings.TrimSpace(string(logBytes)), "connected") || strings.Contains(string(logBytes), hex.EncodeToString(pairing.PairingID)) {
		t.Fatalf("session log = %q, %v", logBytes, err)
	}
	for turn := 0; turn < 2; turn++ {
		if err := os.WriteFile(client.pairingLog, bytes.Repeat([]byte{'x'}, pairingLogMaxBytes), 0o600); err != nil {
			t.Fatal(err)
		}
		client.recordSession(pairing, "reconnect attempt backoff=1s")
	}
	if matches, _ := filepath.Glob(client.pairingLog + ".*"); len(matches) != 1 || matches[0] != client.pairingLog+".1" {
		t.Fatalf("rotated logs = %v", matches)
	}
}
