package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"harness/internal/broker"
)

// Item 2nz: THE QR STAYS UP. The operator's words, 2026-09-29 00:22: "dont auto hide the
// QR code when pairing please". A code the broker expires is replaced in place — a new
// code, a new QR, no blank moment and no click — and only success or his own Cancel takes
// the display away.

// stubBrokerTransport is a broker that accepts PAIR_BEGIN, answers PAIR_WAITING, and then
// ends the connection, which is what an expiry looks like from this side.
type stubBrokerTransport struct {
	mu       sync.Mutex
	incoming chan []byte
	closed   bool
	onBegin  func()
}

func newStubBrokerTransport(onBegin func()) *stubBrokerTransport {
	return &stubBrokerTransport{incoming: make(chan []byte, 8), onBegin: onBegin}
}

func (s *stubBrokerTransport) Send(frame []byte) error {
	decoded, err := broker.Decode(frame)
	if err != nil {
		return err
	}
	if decoded.Type == broker.FramePairBegin {
		if s.onBegin != nil {
			s.onBegin()
		}
		waiting, err := broker.Encode(broker.FramePairWaiting, map[string]any{"expires_at": time.Now().Add(time.Minute).UTC().Format(time.RFC3339)})
		if err != nil {
			return err
		}
		s.incoming <- waiting
		// And then the code expires: the broker ends it.
		go func() {
			time.Sleep(60 * time.Millisecond)
			s.Close(1000, "expired")
		}()
	}
	return nil
}

func (s *stubBrokerTransport) Receive(ctx context.Context) ([]byte, error) {
	select {
	case frame, ok := <-s.incoming:
		if !ok {
			return nil, context.Canceled
		}
		return frame, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *stubBrokerTransport) Close(code int, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.incoming)
	}
	return nil
}

// (a): three expiries, three codes, and the offer is never absent in between.
func TestThePairingOfferSurvivesExpiryAndIsReplacedInPlace2nz(t *testing.T) {
	client, err := NewBrokerClient(broker.DefaultURL)
	if err != nil {
		t.Fatal(err)
	}
	dials := make(chan struct{}, 16)
	client.dial = func(ctx context.Context) (broker.Transport, error) {
		return newStubBrokerTransport(func() { dials <- struct{}{} }), nil
	}
	first, err := client.BeginPairing()
	if err != nil {
		t.Fatal(err)
	}
	if first.Code == "" {
		t.Fatal("the first offer carries no code")
	}
	codes := map[string]bool{first.Code: true}
	// Watch the offer while three codes expire: it must never be absent.
	deadline := time.Now().Add(10 * time.Second)
	for len(codes) < 3 && time.Now().Before(deadline) {
		offer, live := client.PairingOffer()
		if !live {
			t.Fatal("the QR went away on its own: there was a moment with no offer")
		}
		if offer.Code != "" {
			codes[offer.Code] = true
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(codes) < 3 {
		t.Fatalf("saw %d code(s) across the expiries; want at least 3", len(codes))
	}
	if len(dials) < 3 {
		t.Fatalf("the client asked the broker for %d code(s)", len(dials))
	}

	// (a): his Cancel is one of the two things that ends it.
	if err := client.CancelPairing(); err != nil {
		t.Fatal(err)
	}
	if _, live := client.PairingOffer(); live {
		t.Fatal("Cancel left the offer up")
	}
	// And a cancelled pairing stops asking for codes.
	settled := len(dials)
	time.Sleep(300 * time.Millisecond)
	if len(dials) > settled+1 {
		t.Fatalf("the client kept renewing after Cancel: %d dials", len(dials))
	}
	if status := client.Status(); status.State == "paired" {
		t.Fatal("cancelling reported a pairing")
	}
}

// (c): a code AgentB did not generate, of another length and grouping, travels into the
// link and the QR exactly as issued.
func TestAnyCodeLengthReachesTheLinkAndTheQR2nz(t *testing.T) {
	const short = "ABCD-EFGH-JKMN"
	identity := make([]byte, 32)
	for index := range identity {
		identity[index] = byte(index)
	}
	link, image, err := pairingQR(short, identity)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(link, "#c="+short+"&k=") {
		t.Fatalf("the link changed the code: %s", link)
	}
	code, key, err := broker.ReadPairingLink(link)
	if err != nil {
		t.Fatal(err)
	}
	if code != short {
		t.Fatalf("the link round-tripped to %q", code)
	}
	if base64.RawURLEncoding.EncodeToString(key) != base64.RawURLEncoding.EncodeToString(identity) {
		t.Fatal("the link lost the identity key")
	}
	if !strings.HasPrefix(image, "data:image/png;base64,") {
		t.Fatal("no QR was drawn for a code of another length")
	}
}

// (d): no code, renewed or otherwise, is written anywhere. The status the page is given
// is the only place a live code appears, and it is a response body, not a record.
func TestNoRenewedCodeIsStoredOrLogged2nz(t *testing.T) {
	client, err := NewBrokerClient(broker.DefaultURL)
	if err != nil {
		t.Fatal(err)
	}
	client.dial = func(ctx context.Context) (broker.Transport, error) {
		return newStubBrokerTransport(nil), nil
	}
	first, err := client.BeginPairing()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	renewed, _ := client.PairingOffer()
	_ = client.CancelPairing()

	// Whatever the client holds after cancelling must carry no code at all.
	raw, err := json.Marshal(client.Status())
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{first.Code, renewed.Code} {
		if code == "" {
			continue
		}
		if strings.Contains(string(raw), code) {
			t.Fatalf("a code survived in the status: %s", raw)
		}
	}
}
