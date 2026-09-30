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
	"testing"
	"time"

	"harness/internal/broker"
	"harness/internal/credential"
)

type persistenceTransport struct {
	sent chan []byte
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

func TestBrokerIdentityAndPairingSurviveARealStoreRestart2ob(t *testing.T) {
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
