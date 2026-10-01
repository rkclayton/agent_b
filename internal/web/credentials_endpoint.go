package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"harness/internal/credential"
	"harness/internal/identity"
)

// Item 2nv (c) and (d): TRUSTED MANAGEMENT, and nothing else may touch it.
//
// These endpoints are reachable only with the operator's page session cookie AND the
// mutation token, which the server's own guards enforce for every mutation and, for the
// listing, through the protected-read set — so a tool process, the model, a chat, or a
// broker-paired phone cannot create, read, list or change a credential. The value never travels
// outward: the listing carries name, kind, origin and date, and the only way in is a
// masked field in Settings.

var (
	credentialMu      sync.RWMutex
	credentialVault   *credential.Vault
	identityMu        sync.RWMutex
	identityProviders = map[string]identity.Provider{}
)

// SetCredentialVault hands the server the store. Nil means the section reports that no
// store is available rather than pretending to have one.
func SetCredentialVault(vault *credential.Vault) {
	credentialMu.Lock()
	credentialVault = vault
	credentialMu.Unlock()
}

func SetIdentityProvider(kind string, provider identity.Provider) {
	identityMu.Lock()
	defer identityMu.Unlock()
	kind = strings.ToLower(strings.TrimSpace(kind))
	if provider == nil {
		delete(identityProviders, kind)
		return
	}
	identityProviders[kind] = provider
}

func identityProvider(kind string) identity.Provider {
	identityMu.RLock()
	defer identityMu.RUnlock()
	return identityProviders[strings.ToLower(strings.TrimSpace(kind))]
}

func vaultOrNil() *credential.Vault {
	credentialMu.RLock()
	defer credentialMu.RUnlock()
	return credentialVault
}

func credentialListing(ctx context.Context, entries []credential.Entry) []map[string]any {
	listed := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		item := map[string]any{"name": entry.Name, "kind": entry.Kind, "origin": entry.Origin, "header": entry.Header, "stored_at": entry.StoredAt}
		if provider := identityProvider(entry.Kind); provider != nil {
			account, err := provider.Account(ctx, entry.Name)
			item["account"] = account
			item["sign_in_needed"] = err != nil || account == ""
		}
		listed = append(listed, item)
	}
	return listed
}

func credentialEntry(entries []credential.Entry, name string) (credential.Entry, bool) {
	for _, entry := range entries {
		if entry.Name == name {
			return entry, true
		}
	}
	return credential.Entry{}, false
}

func (s *Server) credentialsEndpoint(w http.ResponseWriter, r *http.Request) {
	// (c) and (d): secret entry is the OPERATOR'S PAGE and nothing else. A broker-paired phone
	// is a full-authority surface for chatting, deliberately — but it is not where a
	// credential is typed, read or removed, so it is refused here by name rather than
	// left to the guards that admit it everywhere else.
	if pairedDeviceAuthenticated(r) {
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
		writeJSON(w, http.StatusOK, map[string]any{"credentials": credentialListing(r.Context(), entries)})
	case http.MethodPost:
		var body struct {
			Action   string `json:"action"`
			Name     string `json:"name"`
			Origin   string `json:"origin"`
			Header   string `json:"header"`
			Secret   string `json:"secret"`
			Tenant   string `json:"tenant"`
			ClientID string `json:"client_id"`
			Scopes   string `json:"scopes"`
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
		case "add_entra":
			if err := vault.PutEntra(name, body.Origin, body.Tenant, body.ClientID, strings.Fields(body.Scopes)); err != nil {
				writeError(w, http.StatusBadRequest, err.Error(), "credentials")
				return
			}
			if provider := identityProvider("entra"); provider != nil {
				_ = provider.SignOut(name)
			}
		case "remove":
			if entries, listErr := vault.List(); listErr == nil {
				if entry, present := credentialEntry(entries, name); present {
					if provider := identityProvider(entry.Kind); provider != nil {
						_ = provider.SignOut(name)
					}
				}
			}
			if err := vault.Delete(name); err != nil {
				writeError(w, http.StatusBadRequest, err.Error(), "credentials")
				return
			}
		case "sign_in", "device_code", "sign_out":
			entries, err := vault.List()
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error(), "credentials")
				return
			}
			entry, present := credentialEntry(entries, name)
			provider := identityProvider(entry.Kind)
			if !present || provider == nil {
				writeError(w, http.StatusBadRequest, "this credential has no interactive sign-in provider", "credentials")
				return
			}
			switch strings.TrimSpace(body.Action) {
			case "sign_in":
				if _, err := provider.SignIn(r.Context(), name); err != nil {
					writeError(w, http.StatusBadRequest, "sign-in did not complete: "+err.Error(), "credentials")
					return
				}
			case "device_code":
				device, err := provider.DeviceCode(r.Context(), name)
				if err != nil {
					writeError(w, http.StatusBadRequest, "device-code sign-in did not start: "+err.Error(), "credentials")
					return
				}
				go func() { _, _ = device.Wait(context.Background()) }()
				writeJSON(w, http.StatusOK, map[string]any{"user_code": device.UserCode, "verification_url": device.VerificationURL, "message": device.Message})
				return
			case "sign_out":
				if err := provider.SignOut(name); err != nil {
					writeError(w, http.StatusInternalServerError, err.Error(), "credentials")
					return
				}
			}
		default:
			writeError(w, http.StatusBadRequest, "the credential action is not supported", "action")
			return
		}
		entries, err := vault.List()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error(), "credentials")
			return
		}
		// The answer is the listing, so nothing has to ask again — and the listing has
		// never carried a value.
		writeJSON(w, http.StatusOK, map[string]any{"credentials": credentialListing(r.Context(), entries)})
	default:
		method(w)
	}
}
