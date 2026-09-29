package web

import (
	"net/http"
	"strconv"
	"strings"

	"harness/internal/broker"
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
	PairingOffer() (broker.PairingOffer, bool)
	BeginPairing() (broker.PairingOffer, error)
	ConfirmPairing() error
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

type brokerStatusResponse struct {
	broker.Status
	URL   string               `json:"url"`
	Offer *broker.PairingOffer `json:"offer,omitempty"`
}

func (s *Server) brokerStatus(w http.ResponseWriter, r *http.Request) {
	url := strings.TrimSpace(s.ConfigSnapshot().Broker.URL)
	response := brokerStatusResponse{URL: url}
	host := s.brokerHost()
	if host == nil || url == "" {
		response.State = "off"
		writeJSON(w, http.StatusOK, response)
		return
	}
	response.Status = host.Status()
	if offer, ok := host.PairingOffer(); ok {
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
		writeError(w, http.StatusBadRequest, "set the broker address in Settings → Security before pairing a phone", "broker.url")
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
