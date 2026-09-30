package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"harness/internal/broker"
	"harness/internal/credential"
)

// Item 2kq (a), (b) and (f): the running broker client, and the three things Settings
// asks it to do. Nothing here starts unless broker.url is set.
//
// The identity and completed pairing are separate named user-scoped DPAPI blobs under
// the data root. They never enter configuration, events, logs, prompts, or tool results.

const brokerBlobVersion = 1

type storedBrokerIdentity struct {
	Version     int    `json:"version"`
	SigningSeed []byte `json:"signing_seed"`
	Agreement   []byte `json:"agreement"`
}

type storedBrokerPairing struct {
	Version         int    `json:"version"`
	PairingID       []byte `json:"pairing_id"`
	DeviceKeyID     []byte `json:"device_key_id"`
	DeviceSigning   []byte `json:"device_signing"`
	DeviceAgreement []byte `json:"device_agreement"`
}

// BrokerClient is the live host Settings talks to.
type BrokerClient struct {
	identity      broker.Identity
	dial          broker.Dialer
	client        *broker.Client
	identityStore *credential.Store
	pairingStore  *credential.Store

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

// NewBrokerClient loads or creates this install's identity and prepares to dial. The
// optional data root is omitted only by isolated tests that need an ephemeral identity.
func NewBrokerClient(address string, dataRoot ...string) (*BrokerClient, error) {
	client := &BrokerClient{dial: broker.Dial(address), status: broker.Status{State: "not paired"}}
	if len(dataRoot) > 1 {
		return nil, errors.New("one broker data root is allowed")
	}
	if len(dataRoot) == 1 {
		identityStore, err := credential.NewNamed(dataRoot[0], "broker-identity")
		if err != nil {
			return nil, err
		}
		pairingStore, err := credential.NewNamed(dataRoot[0], "broker-pairing")
		if err != nil {
			return nil, err
		}
		client.identityStore, client.pairingStore = identityStore, pairingStore
		identity, err := loadBrokerIdentity(identityStore)
		if err != nil {
			return nil, err
		}
		client.identity = identity
		pairing, err := loadBrokerPairing(pairingStore)
		if err != nil && !errors.Is(err, credential.ErrNotStored) {
			return nil, err
		}
		if err == nil {
			client.pairing = &pairing
			client.device = "phone"
			client.status = broker.Status{State: "broker unreachable"}
		}
		return client, nil
	}

	seed := make([]byte, 32)
	agreement := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	if _, err := rand.Read(agreement); err != nil {
		return nil, err
	}
	client.identity = broker.Identity{SigningSeed: seed, Agreement: agreement}
	return client, nil
}

func loadBrokerIdentity(store *credential.Store) (broker.Identity, error) {
	raw, err := store.Read()
	if errors.Is(err, credential.ErrNotStored) {
		identity := broker.Identity{SigningSeed: make([]byte, 32), Agreement: make([]byte, 32)}
		if _, err = rand.Read(identity.SigningSeed); err != nil {
			return broker.Identity{}, err
		}
		if _, err = rand.Read(identity.Agreement); err != nil {
			return broker.Identity{}, err
		}
		record := storedBrokerIdentity{Version: brokerBlobVersion, SigningSeed: identity.SigningSeed, Agreement: identity.Agreement}
		encoded, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			return broker.Identity{}, marshalErr
		}
		if err = store.Write(encoded); err != nil {
			return broker.Identity{}, fmt.Errorf("store broker identity: %w", err)
		}
		readBack, readErr := store.Read()
		if readErr != nil || !bytes.Equal(readBack, encoded) {
			_ = store.Clear()
			return broker.Identity{}, errors.New("the broker identity did not read back as written")
		}
		return identity, nil
	}
	if err != nil {
		return broker.Identity{}, fmt.Errorf("read broker identity: %w", err)
	}
	var record storedBrokerIdentity
	if json.Unmarshal(raw, &record) != nil || record.Version != brokerBlobVersion || len(record.SigningSeed) != 32 || len(record.Agreement) != 32 {
		return broker.Identity{}, errors.New("the stored broker identity is invalid")
	}
	return broker.Identity{SigningSeed: record.SigningSeed, Agreement: record.Agreement}, nil
}

func loadBrokerPairing(store *credential.Store) (broker.Pairing, error) {
	raw, err := store.Read()
	if err != nil {
		return broker.Pairing{}, err
	}
	var record storedBrokerPairing
	if json.Unmarshal(raw, &record) != nil || record.Version != brokerBlobVersion || len(record.PairingID) != 16 || len(record.DeviceKeyID) != 16 || len(record.DeviceSigning) != 32 || len(record.DeviceAgreement) != 32 {
		return broker.Pairing{}, errors.New("the stored broker pairing is invalid")
	}
	return broker.Pairing{PairingID: record.PairingID, DeviceKeyID: record.DeviceKeyID, DeviceSigning: record.DeviceSigning, DeviceAgreement: record.DeviceAgreement}, nil
}

func (c *BrokerClient) savePairing(pairing broker.Pairing) error {
	if c.pairingStore == nil {
		return nil
	}
	record := storedBrokerPairing{Version: brokerBlobVersion, PairingID: pairing.PairingID, DeviceKeyID: pairing.DeviceKeyID, DeviceSigning: pairing.DeviceSigning, DeviceAgreement: pairing.DeviceAgreement}
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := c.pairingStore.Write(encoded); err != nil {
		return fmt.Errorf("store broker pairing: %w", err)
	}
	readBack, err := c.pairingStore.Read()
	if err != nil || !bytes.Equal(readBack, encoded) {
		_ = c.pairingStore.Clear()
		return errors.New("the broker pairing did not read back as written")
	}
	return nil
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
			if storeErr := c.savePairing(pairing); storeErr != nil {
				c.mu.Lock()
				c.offer = nil
				c.cancel = nil
				c.status = broker.Status{State: "not paired", LastError: storeErr.Error()}
				c.mu.Unlock()
				return
			}
			c.mu.Lock()
			c.offer = nil
			c.pairing = &pairing
			c.device = "phone"
			c.status = broker.Status{State: "broker unreachable"}
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
	if c.pairingStore != nil {
		if err := c.pairingStore.Clear(); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.stopSessionLocked()
	c.pairing = nil
	c.device = ""
	c.status = broker.Status{State: "not paired"}
	c.mu.Unlock()
	return nil
}
