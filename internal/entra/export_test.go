package entra

import (
	"net/http"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"

	"harness/internal/credential"
)

// Item 2wl: the two test-only hooks, moved out of the product build.

func (m *Manager) setTestAuthority(authority func(credential.EntraDefinition) string, client *http.Client) {
	m.mu.Lock()
	m.authority, m.httpClient, m.clients = authority, client, map[string]public.Client{}
	m.mu.Unlock()
}

func (m *Manager) ForgetMemoryForTest(name string) {
	m.mu.Lock()
	delete(m.clients, name)
	m.mu.Unlock()
}
