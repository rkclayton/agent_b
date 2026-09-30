package entra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/cache"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"

	"harness/internal/credential"
)

type protectedCache struct {
	mu    sync.Mutex
	store *credential.Store
}

func (c *protectedCache) Replace(ctx context.Context, target cache.Unmarshaler, _ cache.ReplaceHints) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, err := c.store.Read()
	if errors.Is(err, credential.ErrNotStored) {
		return nil
	}
	if err != nil {
		return err
	}
	return target.Unmarshal(raw)
}

func (c *protectedCache) Export(ctx context.Context, source cache.Marshaler, _ cache.ExportHints) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, err := source.Marshal()
	if err != nil {
		return err
	}
	var document map[string]json.RawMessage
	if json.Unmarshal(raw, &document) != nil {
		return fmt.Errorf("MSAL cache is unreadable")
	}
	delete(document, "AccessToken")
	delete(document, "AccessTokenPartition")
	raw, err = json.Marshal(document)
	if err != nil {
		return err
	}
	return c.store.Write(raw)
}

type Manager struct {
	mu         sync.Mutex
	vault      *credential.Vault
	clients    map[string]public.Client
	authority  func(credential.EntraDefinition) string
	httpClient *http.Client
}

func New(vault *credential.Vault) *Manager {
	return &Manager{vault: vault, clients: map[string]public.Client{}, authority: func(d credential.EntraDefinition) string {
		return "https://login.microsoftonline.com/" + url.PathEscape(d.Tenant)
	}}
}

func (m *Manager) setTestAuthority(authority func(credential.EntraDefinition) string, client *http.Client) {
	m.mu.Lock()
	m.authority, m.httpClient, m.clients = authority, client, map[string]public.Client{}
	m.mu.Unlock()
}

func (m *Manager) client(name string) (public.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if client, ok := m.clients[name]; ok {
		return client, nil
	}
	definition, _, err := m.vault.Entra(name)
	if err != nil {
		return public.Client{}, err
	}
	store, err := m.vault.EntraCache(name)
	if err != nil {
		return public.Client{}, err
	}
	options := []public.Option{public.WithAuthority(m.authority(definition)), public.WithCache(&protectedCache{store: store}), public.WithInstanceDiscovery(false)}
	if m.httpClient != nil {
		options = append(options, public.WithHTTPClient(m.httpClient))
	}
	client, err := public.New(definition.ClientID, options...)
	if err == nil {
		m.clients[name] = client
	}
	return client, err
}

func accountName(account public.Account) string {
	if account.PreferredUsername != "" {
		return account.PreferredUsername
	}
	return account.HomeAccountID
}

func (m *Manager) clear(name string) error {
	m.mu.Lock()
	delete(m.clients, name)
	m.mu.Unlock()
	store, err := m.vault.EntraCache(name)
	if err != nil {
		return err
	}
	if err := store.Clear(); err != nil && !errors.Is(err, credential.ErrNotStored) {
		return err
	}
	return nil
}

func (m *Manager) Interactive(ctx context.Context, name string, openURL func(string) error) (string, error) {
	if err := m.clear(name); err != nil {
		return "", err
	}
	client, err := m.client(name)
	if err != nil {
		return "", err
	}
	definition, _, _ := m.vault.Entra(name)
	result, err := client.AcquireTokenInteractive(ctx, definition.Scopes, public.WithOpenURL(openURL))
	if err != nil {
		_ = m.clear(name)
		return "", err
	}
	return accountName(result.Account), nil
}

type DeviceCode struct {
	UserCode, VerificationURL, Message string
	value                              public.DeviceCode
	manager                            *Manager
	name                               string
}

func (m *Manager) DeviceCode(ctx context.Context, name string) (DeviceCode, error) {
	if err := m.clear(name); err != nil {
		return DeviceCode{}, err
	}
	client, err := m.client(name)
	if err != nil {
		return DeviceCode{}, err
	}
	definition, _, _ := m.vault.Entra(name)
	value, err := client.AcquireTokenByDeviceCode(ctx, definition.Scopes)
	if err != nil {
		return DeviceCode{}, err
	}
	return DeviceCode{UserCode: value.Result.UserCode, VerificationURL: value.Result.VerificationURL, Message: value.Result.Message, value: value, manager: m, name: name}, nil
}

func (d DeviceCode) Complete(ctx context.Context) (string, error) {
	result, err := d.value.AuthenticationResult(ctx)
	if err != nil {
		_ = d.manager.clear(d.name)
		return "", err
	}
	return accountName(result.Account), nil
}

func (m *Manager) Account(ctx context.Context, name string) (string, error) {
	client, err := m.client(name)
	if err != nil {
		return "", err
	}
	accounts, err := client.Accounts(ctx)
	if err != nil || len(accounts) != 1 {
		return "", err
	}
	return accountName(accounts[0]), nil
}

func (m *Manager) Origin(name string) (string, error) {
	_, entry, err := m.vault.Entra(name)
	if err != nil {
		return "", err
	}
	return entry.Origin, nil
}

func (m *Manager) Token(ctx context.Context, name string) (string, error) {
	client, err := m.client(name)
	if err != nil {
		return "", fmt.Errorf("auth_error: sign in in Settings → Security")
	}
	accounts, err := client.Accounts(ctx)
	if err != nil || len(accounts) != 1 {
		return "", fmt.Errorf("auth_error: sign in in Settings → Security")
	}
	definition, _, _ := m.vault.Entra(name)
	result, err := client.AcquireTokenSilent(ctx, definition.Scopes, public.WithSilentAccount(accounts[0]))
	if err != nil {
		_ = m.clear(name)
		return "", fmt.Errorf("auth_error: sign in in Settings → Security")
	}
	return strings.TrimSpace(result.AccessToken), nil
}

func (m *Manager) SignOut(name string) error { return m.clear(name) }
func (m *Manager) ForgetMemoryForTest(name string) {
	m.mu.Lock()
	delete(m.clients, name)
	m.mu.Unlock()
}
