package web

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"harness/internal/broker"
	"harness/internal/config"
)

// Item 2nu: the address is built in, it is not in the page, and the client opens nothing
// until a phone is pairing or paired.

// (a) and (c): the default is the one written in source, and a configuration file still
// wins — which is how a test, or anyone running their own broker, points it elsewhere.
func TestTheBrokerAddressIsBuiltInAndTheFileStillWins2nu(t *testing.T) {
	if broker.DefaultURL != "wss://broker.agentb.app/v1/connect" {
		t.Fatalf("the built-in address is %q", broker.DefaultURL)
	}
	var fresh config.Config
	if err := json.Unmarshal([]byte(`{"config_version":11}`), &fresh); err != nil {
		t.Fatal(err)
	}
	config.ApplyDefaults(&fresh)
	if fresh.Broker.URL != broker.DefaultURL {
		t.Fatalf("a configuration with no broker key dials %q", fresh.Broker.URL)
	}
	var overridden config.Config
	if err := json.Unmarshal([]byte(`{"config_version":11,"broker":{"url":"wss://someone.example/v1/connect"}}`), &overridden); err != nil {
		t.Fatal(err)
	}
	config.ApplyDefaults(&overridden)
	if overridden.Broker.URL != "wss://someone.example/v1/connect" {
		t.Fatalf("the file's address was replaced by %q", overridden.Broker.URL)
	}
}

// (b): the status the page reads carries no address at all. The page cannot show what it
// is never sent.
func TestTheBrokerStatusCarriesNoAddress2nu(t *testing.T) {
	server := &Server{cfg: &config.Config{Broker: config.Broker{URL: broker.DefaultURL}}}
	server.SetBrokerHost(countingBrokerHost{})
	request := httptest.NewRequest("GET", "/api/broker/status", nil)
	response := httptest.NewRecorder()
	server.brokerStatus(response, request)
	body := response.Body.String()
	if strings.Contains(body, "wss://") || strings.Contains(body, "\"url\"") {
		t.Fatalf("the status carries the broker address: %s", body)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatal(err)
	}
	if _, present := payload["state"]; !present {
		t.Fatalf("the status lost its state: %s", body)
	}
}

// (d): DIALS ONLY WHEN NEEDED. Asserted on the client — the number of times it opened a
// connection — and not by waiting to see whether anything happens.
func TestTheClientDialsOnlyWhilePairingOrPaired2nu(t *testing.T) {
	dials := 0
	client, err := NewBrokerClient(broker.DefaultURL)
	if err != nil {
		t.Fatal(err)
	}
	client.dial = func(ctx context.Context) (broker.Transport, error) {
		dials++
		return nil, context.Canceled
	}
	// Everything the page asks of an install with no phone.
	_ = client.Status()
	_, _ = client.PairingOffer()
	_ = client.IdentityKey()
	if dials != 0 {
		t.Fatalf("an install with no phone opened %d connection(s)", dials)
	}
	if err := client.RevokePairing(); err == nil {
		t.Fatal("revoking with nothing paired was accepted")
	}
	if dials != 0 {
		t.Fatalf("revoking with nothing paired dialed %d time(s)", dials)
	}
	if _, err := client.BeginPairing(); err == nil {
		t.Fatal("the pairing succeeded against a dialer that fails")
	}
	if dials != 1 {
		t.Fatalf("beginning a pairing dialed %d time(s)", dials)
	}
}

type countingBrokerHost struct{}

func (countingBrokerHost) Status() broker.Status                     { return broker.Status{State: "not paired"} }
func (countingBrokerHost) PairingOffer() (broker.PairingOffer, bool) { return broker.PairingOffer{}, false }
func (countingBrokerHost) BeginPairing() (broker.PairingOffer, error) {
	return broker.PairingOffer{}, nil
}
func (countingBrokerHost) ConfirmPairing() error  { return nil }
func (countingBrokerHost) CancelPairing() error   { return nil }
func (countingBrokerHost) RevokePairing() error   { return nil }
func (countingBrokerHost) IdentityKey() []byte    { return make([]byte, 32) }
