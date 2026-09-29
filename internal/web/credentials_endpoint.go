package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"harness/internal/credential"
)

// Item 2nv (c) and (d): TRUSTED MANAGEMENT, and nothing else may touch it.
//
// These endpoints are reachable only with the operator's page session cookie AND the
// mutation token, which the server's own guards enforce for every mutation and, for the
// listing, through the protected-read set — so a tool process, the model, a chat, or a
// phone bearer cannot create, read, list or change a credential. The value never travels
// outward: the listing carries name, kind, origin and date, and the only way in is a
// masked field in Settings.

var (
	credentialMu    sync.RWMutex
	credentialVault *credential.Vault
)

// SetCredentialVault hands the server the store. Nil means the section reports that no
// store is available rather than pretending to have one.
func SetCredentialVault(vault *credential.Vault) {
	credentialMu.Lock()
	credentialVault = vault
	credentialMu.Unlock()
}

func vaultOrNil() *credential.Vault {
	credentialMu.RLock()
	defer credentialMu.RUnlock()
	return credentialVault
}

func (s *Server) credentialsEndpoint(w http.ResponseWriter, r *http.Request) {
	// (c) and (d): secret entry is the OPERATOR'S PAGE and nothing else. A paired phone
	// is a full-authority surface for chatting, deliberately — but it is not where a
	// credential is typed, read or removed, so it is refused here by name rather than
	// left to the guards that admit it everywhere else.
	if phoneAuthenticated(r) {
		writeError(w, http.StatusForbidden, "credentials are managed in Settings on this machine", "credentials")
		return
	}
	vault := vaultOrNil()
	if vault == nil {
		writeError(w, http.StatusServiceUnavailable, "no credential store is available", "credentials")
		return
	}
	switch r.Method {
	case http.MethodGet:
		entries, err := vault.List()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error(), "credentials")
			return
		}
		if entries == nil {
			entries = []credential.Entry{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"credentials": entries})
	case http.MethodPost:
		var body struct {
			Action string `json:"action"`
			Name   string `json:"name"`
			Origin string `json:"origin"`
			Header string `json:"header"`
			Secret string `json:"secret"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "the request is not readable", "credentials")
			return
		}
		name := strings.TrimSpace(body.Name)
		switch strings.TrimSpace(body.Action) {
		case "add":
			if err := vault.Put(name, body.Origin, body.Header, body.Secret); err != nil {
				writeError(w, http.StatusBadRequest, err.Error(), "credentials")
				return
			}
		case "remove":
			if err := vault.Delete(name); err != nil {
				writeError(w, http.StatusBadRequest, err.Error(), "credentials")
				return
			}
		default:
			writeError(w, http.StatusBadRequest, "the action must be add or remove", "action")
			return
		}
		entries, err := vault.List()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error(), "credentials")
			return
		}
		// The answer is the listing, so nothing has to ask again — and the listing has
		// never carried a value.
		writeJSON(w, http.StatusOK, map[string]any{"credentials": entries})
	default:
		method(w)
	}
}
