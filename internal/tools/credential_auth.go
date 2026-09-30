package tools

import (
	"fmt"
	"net/url"
	"strings"

	"harness/internal/credential"
	"harness/internal/identity"
)

type TokenProvider = identity.TokenProvider

func (c *CallService) SetTokenProvider(scheme string, provider TokenProvider) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.providers[strings.ToLower(strings.TrimSpace(scheme))] = provider
}

// Item 2nv (a): AUTH PROVIDERS, behind one seam. A provider is asked for a credential for
// one request and answers with a header, or refuses. It never decides where the request
// may go — DESTINATION ENFORCEMENT does that, in internal/credential, and every provider
// passes through it. Stage 2 (item 2nw) adds a signed-in provider here and changes
// nothing else.

// credentialHeader is what a provider returns: one header, and the secret itself so the
// scrubber can take it back out of anything the service reflects.
type credentialHeader struct {
	name   string
	value  string
	secret string
}

// SetVault gives the tool the named-credential store. Nil means no `stored:` connector
// can be used, which is what a build with no data root has.
func (c *CallService) SetVault(vault *credential.Vault) {
	c.mu.Lock()
	c.vault = vault
	c.mu.Unlock()
}

func (c *CallService) currentVault() *credential.Vault {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.vault
}

// storedCredential is the stored-key provider. The binding decides BOTH the header and
// the destination, and the destination is checked before the secret is read out of the
// store — so a call to the wrong origin never decrypts anything.
func (c *CallService) storedCredential(name string, target *url.URL) (credentialHeader, error) {
	vault := c.currentVault()
	if vault == nil {
		return credentialHeader{}, fmt.Errorf("auth_error: no credential store is available")
	}
	credentialName := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "stored:"))
	entries, err := vault.List()
	if err != nil {
		return credentialHeader{}, fmt.Errorf("auth_error: the credential record is unreadable")
	}
	var binding credential.Entry
	found := false
	for _, entry := range entries {
		if entry.Name == credentialName {
			binding, found = entry, true
		}
	}
	if !found {
		return credentialHeader{}, fmt.Errorf("auth_error: no credential named %q is stored; add it in Settings → Security", credentialName)
	}
	if err := credential.AllowsOrigin(binding.Origin, target); err != nil {
		return credentialHeader{}, fmt.Errorf("auth_error: %w", err)
	}
	secret, _, err := vault.Secret(credentialName)
	if err != nil {
		return credentialHeader{}, fmt.Errorf("auth_error: the stored credential could not be read")
	}
	if binding.Header == "" {
		return credentialHeader{name: "Authorization", value: "Bearer " + secret, secret: secret}, nil
	}
	return credentialHeader{name: binding.Header, value: secret, secret: secret}, nil
}

// approvedOrigin is the origin a connector's own base_url names, which is what a helper's
// output and a stored key are both held to when no other binding says otherwise.
func approvedOrigin(baseURL string) (string, error) {
	return credential.NormalizeOrigin(baseURL)
}

// enforceDestination is the one gate every credential passes through. It is called with
// the request's target before any credential is acquired, so a refusal costs nothing and
// reveals nothing.
func enforceDestination(origin string, target *url.URL) error {
	if err := credential.AllowsOrigin(origin, target); err != nil {
		return fmt.Errorf("auth_error: %w", err)
	}
	return nil
}
