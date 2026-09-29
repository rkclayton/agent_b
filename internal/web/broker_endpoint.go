package web

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"harness/internal/broker"

	"rsc.io/qr"
)

// Item 2kq (b) and (e): the three things Settings asks the product about the broker —
// what the connection is doing, start a pairing, and revoke one.
//
// A CONTROL-PLANE SURFACE, under item 2jy. These handlers are reached from the loopback
// page like every other Settings action; no tool process is anywhere near them, and no
// broker credential is ever in a response.

// BrokerHost is what the server needs from the broker client. It is an interface so the
// endpoint can be tested without a network and so the client's own lifetime is owned by
// the harness rather than by an HTTP handler.
type BrokerHost interface {
	Status() broker.Status
	// IdentityKey is this AgentB's Ed25519 identity public key, which the pairing link
	// carries so the phone can compare it with what the broker sends it.
	IdentityKey() []byte
	PairingOffer() (broker.PairingOffer, bool)
	BeginPairing() (broker.PairingOffer, error)
	ConfirmPairing() error
	CancelPairing() error
	RevokePairing() error
}

// SetBrokerHost hands the server the running client. Nil means the feature is off,
// which is what an empty broker.url leaves.
func (s *Server) SetBrokerHost(host BrokerHost) {
	s.brokerMu.Lock()
	s.broker = host
	s.brokerMu.Unlock()
}

func (s *Server) brokerHost() BrokerHost {
	s.brokerMu.RLock()
	defer s.brokerMu.RUnlock()
	return s.broker
}

// Item 2nu (b): NO ADDRESS LEAVES THE PROCESS. The page cannot show, edit or leak what it
// is never sent, so the status says what the connection is doing and nothing about where.
type brokerStatusResponse struct {
	broker.Status
	Offer *broker.PairingOffer `json:"offer,omitempty"`
}

func (s *Server) brokerStatus(w http.ResponseWriter, r *http.Request) {
	url := strings.TrimSpace(s.ConfigSnapshot().Broker.URL)
	response := brokerStatusResponse{}
	host := s.brokerHost()
	if host == nil || url == "" {
		response.State = "off"
		writeJSON(w, http.StatusOK, response)
		return
	}
	response.Status = host.Status()
	if offer, ok := host.PairingOffer(); ok {
		// Item 2ns (b): while a code is LIVE the page gets the link as a QR code. It is
		// built here, for this response, and kept nowhere: (c) says the link is never
		// stored and never logged, and a value that exists only in one response body
		// cannot be either.
		if offer.Code != "" {
			if link, image, err := pairingQR(offer.Code, host.IdentityKey()); err == nil {
				offer.Link, offer.QR = link, image
			}
		}
		response.Offer = &offer
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) brokerAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	if !decode(w, r, &body) {
		return
	}
	host := s.brokerHost()
	if host == nil || strings.TrimSpace(s.ConfigSnapshot().Broker.URL) == "" {
		writeError(w, http.StatusBadRequest, "this build has no broker address, so a phone cannot be paired", "broker.url")
		return
	}
	switch body.Action {
	case "pair":
		offer, err := host.BeginPairing()
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error(), "broker")
			return
		}
		writeJSON(w, http.StatusOK, offer)
	case "confirm":
		// The operator has compared the two fingerprints and says they match. That
		// judgement is his; this only carries it.
		if err := host.ConfirmPairing(); err != nil {
			writeError(w, http.StatusBadGateway, err.Error(), "broker")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "cancel":
		// Item 2nz (a): the operator taking the square away, which is one of the two
		// things that end a pairing. It is the same control as Pair, which reads Cancel
		// while one is under way — not a new one.
		if err := host.CancelPairing(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error(), "broker")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "revoke":
		if err := host.RevokePairing(); err != nil {
			writeError(w, http.StatusBadGateway, err.Error(), "broker")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeError(w, http.StatusBadRequest, "unknown broker action "+strconv.Quote(body.Action), "action")
	}
}

// brokerDiagnostics is item 2kq (e)'s redacted section for the diagnostics export: what
// state the connection is in and which build answered, and no identifier that would let
// a reader of the export reach the pairing.
func (s *Server) brokerDiagnostics() map[string]any {
	url := strings.TrimSpace(s.ConfigSnapshot().Broker.URL)
	section := map[string]any{"configured": url != ""}
	host := s.brokerHost()
	if host == nil || url == "" {
		section["state"] = "off"
		return section
	}
	status := host.Status()
	section["state"] = status.State
	section["broker_build"] = status.BrokerBuild
	section["reconnects"] = status.Reconnects
	section["paired"] = status.PairedDevice != ""
	// Deliberately absent: the URL, the session id, the pairing id, the device name.
	return section
}

// pairingQR renders the link as a PNG data URI. The encoder is rsc.io/qr: pure Go,
// BSD-3-Clause from the Go Authors, no network and no cgo in the package imported here.
// Medium correction, because this is read off a screen at arm's length rather than
// printed on a box.
func pairingQR(code string, identityKey []byte) (string, string, error) {
	if len(identityKey) != 32 {
		return "", "", errors.New("the identity key is not 32 bytes")
	}
	link := broker.PairingLink(code, identityKey)
	encoded, err := qr.Encode(link, qr.M)
	if err != nil {
		return "", "", err
	}
	return link, "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.PNG()), nil
}
