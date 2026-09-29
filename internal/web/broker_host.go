package web

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"time"

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
	// Item 2nz: the pairing runs until it succeeds or the operator cancels. cancel is
	// closed by CancelPairing and is the only thing besides success that ends it.
	cancel chan struct{}
	// Item 2o7: the server whose handlers answer the phone, and the paired session's stop.
	server      *Server
	stopSession context.CancelFunc
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

// BeginPairing starts a pairing and KEEPS IT UP. Item 2nz (a): a code the broker expires
// is replaced in place — a fresh code, a fresh QR, no blank moment and no click — because
// the operator asked that the square not disappear under him while he was reaching for
// his phone. Two things end it: the pairing completing, and his own Cancel.
//
// The first offer is returned synchronously, so the page has something to draw at once.
func (c *BrokerClient) BeginPairing() (broker.PairingOffer, error) {
	c.mu.Lock()
	if c.cancel != nil {
		select {
		case <-c.cancel:
		default:
			c.mu.Unlock()
			return broker.PairingOffer{}, errors.New("a pairing is already under way")
		}
	}
	cancel := make(chan struct{})
	c.cancel = cancel
	c.mu.Unlock()

	offer, err := c.beginOnce(cancel)
	if err != nil {
		c.mu.Lock()
		c.offer, c.cancel = nil, nil
		c.mu.Unlock()
		return broker.PairingOffer{}, err
	}
	return offer, nil
}

// beginOnce asks the broker for one code, publishes it, and runs the pairing in the
// background; when that attempt ends without success it asks for the next one, unless the
// operator has cancelled.
func (c *BrokerClient) beginOnce(cancel chan struct{}) (broker.PairingOffer, error) {
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
	started := time.Now()
	c.mu.Lock()
	// REPLACED IN PLACE: the new offer is written over the old one, so there is never a
	// moment when the page would find nothing to draw.
	c.offer = &broker.PairingOffer{Code: code}
	c.confirm = confirm
	c.frames = frames
	published := *c.offer
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
			select {
			case answer := <-confirm:
				return answer
			case <-cancel:
				return false
			}
		})
		_ = transport.Close(1000, "pairing ended")

		if pairErr == nil {
			c.mu.Lock()
			c.offer = nil
			c.pairing = &pairing
			c.device = "phone"
			c.status = broker.Status{State: "paired"}
			c.cancel = nil
			c.mu.Unlock()
			c.startSession(pairing)
			return
		}
		select {
		case <-cancel:
			// His Cancel: the offer is already gone, and nothing is asked for again.
			return
		default:
		}
		// The attempt ended without pairing — an expired code, a dropped connection, a
		// broker that said no. Ask for another one. A pause only when the attempt failed
		// immediately, so a broker that refuses is not hammered.
		if time.Since(started) < time.Second {
			select {
			case <-cancel:
				return
			case <-time.After(2 * time.Second):
			}
		}
		if _, retryErr := c.beginOnce(cancel); retryErr != nil {
			c.mu.Lock()
			c.offer = nil
			c.cancel = nil
			c.status = broker.Status{State: "not paired", LastError: retryErr.Error()}
			c.mu.Unlock()
		}
	}()

	return published, nil
}

// CancelPairing is the operator taking the square away. Item 2nz (a): it and success are
// the only two things that do.
func (c *BrokerClient) CancelPairing() error {
	c.mu.Lock()
	cancel := c.cancel
	if cancel == nil {
		c.mu.Unlock()
		return errors.New("no pairing is under way")
	}
	select {
	case <-cancel:
	default:
		close(cancel)
	}
	c.offer = nil
	c.cancel = nil
	if c.pairing == nil {
		c.status = broker.Status{State: "not paired"}
	}
	c.mu.Unlock()
	return nil
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
	c.stopSessionLocked()
	c.pairing = nil
	c.device = ""
	c.status = broker.Status{State: "not paired"}
	c.mu.Unlock()
	return nil
}
