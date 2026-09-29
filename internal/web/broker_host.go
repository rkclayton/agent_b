package web

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"

	"harness/internal/broker"
)

// Item 2kq (a), (b) and (f): the running broker client, and the three things Settings
// asks it to do. Nothing here starts unless broker.url is set.
//
// THE IDENTITY LIVES IN THIS PROCESS AND NOWHERE ELSE. It is generated at start and held
// in memory: no file under the data root carries it, which is what the capability arm
// asserts and what keeps a tool process away from it. The cost is stated rather than
// hidden — a pairing does not survive a restart of Agent_b, because the identity it was
// made with is gone. Persisting it is a security decision that has not been made.

// BrokerClient is the live host Settings talks to.
type BrokerClient struct {
	identity broker.Identity
	dial     broker.Dialer
	client   *broker.Client

	mu      sync.Mutex
	offer   *broker.PairingOffer
	pairing *broker.Pairing
	confirm chan bool
	frames  chan broker.Frame
	status  broker.Status
	device  string
}

// NewBrokerClient generates this install's identity and prepares to dial. The address is
// the operator's broker.url; an empty one never reaches here.
func NewBrokerClient(address string) (*BrokerClient, error) {
	seed := make([]byte, 32)
	agreement := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	if _, err := rand.Read(agreement); err != nil {
		return nil, err
	}
	return &BrokerClient{
		identity: broker.Identity{SigningSeed: seed, Agreement: agreement},
		dial:     broker.Dial(address),
		status:   broker.Status{State: "not connected"},
	}, nil
}

// IdentityKey is the Ed25519 identity public key the pairing link carries, so the phone
// can compare it with what the broker sends it.
func (c *BrokerClient) IdentityKey() []byte { return c.identity.SigningPublic() }

// Status is what Settings shows.
func (c *BrokerClient) Status() broker.Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		status := c.client.Status()
		status.PairedDevice = c.device
		return status
	}
	status := c.status
	status.PairedDevice = c.device
	return status
}

// PairingOffer is the live offer, if there is one. Item 2ns (b): the QR exists only
// while this returns true.
func (c *BrokerClient) PairingOffer() (broker.PairingOffer, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.offer == nil {
		return broker.PairingOffer{}, false
	}
	return *c.offer, true
}

// BeginPairing dials, sends PAIR_BEGIN with the hash of a fresh code, and holds the
// offer so Settings can show it. The pairing itself completes when the operator confirms.
func (c *BrokerClient) BeginPairing() (broker.PairingOffer, error) {
	code, _, err := broker.NewPairingCode()
	if err != nil {
		return broker.PairingOffer{}, err
	}
	transport, err := c.dial(context.Background())
	if err != nil {
		return broker.PairingOffer{}, err
	}
	frames := make(chan broker.Frame, 16)
	confirm := make(chan bool, 1)
	c.mu.Lock()
	c.offer = &broker.PairingOffer{Code: code}
	c.confirm = confirm
	c.frames = frames
	c.mu.Unlock()

	go func() {
		raw := make(chan []byte, 16)
		go func() {
			for {
				message, receiveErr := transport.Receive(context.Background())
				if receiveErr != nil {
					close(raw)
					return
				}
				raw <- message
			}
		}()
		receive := func() (broker.Frame, error) {
			message, ok := <-raw
			if !ok {
				return broker.Frame{}, errors.New("the pairing connection ended")
			}
			return broker.Decode(message)
		}
		pairing, pairErr := broker.BeginPairing(transport, c.identity, code, receive, func(offer broker.PairingOffer) bool {
			c.mu.Lock()
			c.offer = &offer
			c.mu.Unlock()
			return <-confirm
		})
		c.mu.Lock()
		defer c.mu.Unlock()
		// Item 2ns (b): the offer — and with it the QR — goes the moment the pairing
		// completes or fails.
		c.offer = nil
		if pairErr != nil {
			c.status = broker.Status{State: "not paired", LastError: pairErr.Error()}
			_ = transport.Close(1000, "pairing ended")
			return
		}
		c.pairing = &pairing
		c.device = "phone"
		c.status = broker.Status{State: "paired"}
		_ = transport.Close(1000, "paired")
	}()

	c.mu.Lock()
	defer c.mu.Unlock()
	return *c.offer, nil
}

// ConfirmPairing carries the operator's judgement that the two fingerprints match.
func (c *BrokerClient) ConfirmPairing() error {
	c.mu.Lock()
	confirm := c.confirm
	c.mu.Unlock()
	if confirm == nil {
		return errors.New("no pairing is waiting to be confirmed")
	}
	select {
	case confirm <- true:
		return nil
	default:
		return errors.New("the pairing is not at the fingerprint step")
	}
}

// RevokePairing ends the pairing from this side.
func (c *BrokerClient) RevokePairing() error {
	c.mu.Lock()
	pairing := c.pairing
	c.mu.Unlock()
	if pairing == nil {
		return errors.New("nothing is paired")
	}
	transport, err := c.dial(context.Background())
	if err != nil {
		return err
	}
	defer func() { _ = transport.Close(1000, "revoked") }()
	if err := broker.Revoke(transport, c.identity, pairing.PairingID); err != nil {
		return err
	}
	c.mu.Lock()
	c.pairing = nil
	c.device = ""
	c.status = broker.Status{State: "not paired"}
	c.mu.Unlock()
	return nil
}
