package google

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"harness/internal/credential"
	"harness/internal/identity"
)

const signedOut = "auth_error: not signed in; sign in in Settings → Security → Credentials"

type cachedToken struct {
	Value   string
	Expires time.Time
}
type persistedToken struct {
	RefreshToken string `json:"refresh_token"`
	Account      string `json:"account"`
}

type Manager struct {
	mu                                             sync.Mutex
	vault                                          *credential.Vault
	access                                         map[string]cachedToken
	authorizeURL, tokenURL, userinfoURL, revokeURL string
	httpClient                                     *http.Client
}

func New(vault *credential.Vault) *Manager {
	return &Manager{vault: vault, access: map[string]cachedToken{}, httpClient: http.DefaultClient,
		authorizeURL: "https://accounts.google.com/o/oauth2/v2/auth", tokenURL: "https://oauth2.googleapis.com/token",
		userinfoURL: "https://openidconnect.googleapis.com/v1/userinfo", revokeURL: "https://oauth2.googleapis.com/revoke"}
}

func (m *Manager) setTestEndpoints(authorize, token, userinfo, revoke string, client *http.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.authorizeURL, m.tokenURL, m.userinfoURL, m.revokeURL, m.httpClient = authorize, token, userinfo, revoke, client
}

func (m *Manager) ForgetMemoryForTest(name string) {
	m.mu.Lock()
	delete(m.access, name)
	m.mu.Unlock()
}

func randomURLString(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func openBrowser(address string) error {
	return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", address).Start()
}

func (m *Manager) SignIn(ctx context.Context, name string) (string, error) {
	return m.Interactive(ctx, name, openBrowser)
}

func (m *Manager) Interactive(ctx context.Context, name string, openURL func(string) error) (string, error) {
	definition, _, err := m.vault.Google(name)
	if err != nil {
		return "", err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer listener.Close()
	state, err := randomURLString(24)
	if err != nil {
		return "", err
	}
	verifier, err := randomURLString(48)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(verifier))
	redirect := "http://" + listener.Addr().String() + "/oauth2/callback"
	callback := make(chan url.Values, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			callback <- r.Form
		} else {
			callback <- r.URL.Query()
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "Sign-in complete. You can close this page.")
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	query := url.Values{"client_id": {definition.ClientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "scope": {strings.Join(definition.Scopes, " ")}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}, "code_challenge_method": {"S256"}, "access_type": {"offline"}, "prompt": {"consent"}}
	if openURL == nil {
		openURL = openBrowser
	}
	if err := openURL(m.authorizeURL + "?" + query.Encode()); err != nil {
		return "", err
	}
	var values url.Values
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case values = <-callback:
	}
	if values.Get("state") != state || values.Get("code") == "" {
		return "", errors.New("Google sign-in callback was invalid")
	}
	token, err := m.exchange(ctx, definition, url.Values{"grant_type": {"authorization_code"}, "code": {values.Get("code")}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
	if err != nil {
		return "", err
	}
	account, err := m.userinfo(ctx, token.AccessToken)
	if err != nil {
		return "", err
	}
	if token.RefreshToken == "" {
		return "", errors.New("Google did not return an offline refresh token")
	}
	if err := m.writeCache(name, persistedToken{RefreshToken: token.RefreshToken, Account: account}); err != nil {
		return "", err
	}
	m.remember(name, token.AccessToken, token.ExpiresIn)
	return account, nil
}

type tokenAnswer struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

func (m *Manager) exchange(ctx context.Context, definition credential.GoogleDefinition, form url.Values) (tokenAnswer, error) {
	form.Set("client_id", definition.ClientID)
	form.Set("client_secret", definition.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenAnswer{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return tokenAnswer{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return tokenAnswer{}, fmt.Errorf("Google token endpoint returned %s", resp.Status)
	}
	var answer tokenAnswer
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&answer) != nil || strings.TrimSpace(answer.AccessToken) == "" {
		return tokenAnswer{}, errors.New("Google token response was unreadable")
	}
	return answer, nil
}

func (m *Manager) userinfo(ctx context.Context, access string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, m.userinfoURL, nil)
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var answer struct {
		Email string `json:"email"`
	}
	if resp.StatusCode/100 != 2 || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&answer) != nil || strings.TrimSpace(answer.Email) == "" {
		return "", errors.New("Google account response was unreadable")
	}
	return strings.TrimSpace(answer.Email), nil
}

func (m *Manager) writeCache(name string, value persistedToken) error {
	store, err := m.vault.GoogleCache(name)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return store.Write(raw)
}

func (m *Manager) readCache(name string) (persistedToken, error) {
	store, err := m.vault.GoogleCache(name)
	if err != nil {
		return persistedToken{}, err
	}
	raw, err := store.Read()
	if err != nil {
		return persistedToken{}, err
	}
	var value persistedToken
	if json.Unmarshal(raw, &value) != nil || value.RefreshToken == "" {
		return persistedToken{}, errors.New("Google sign-in cache is unreadable")
	}
	return value, nil
}

func (m *Manager) remember(name, value string, seconds int64) {
	if seconds < 1 {
		seconds = 3600
	}
	m.mu.Lock()
	m.access[name] = cachedToken{Value: value, Expires: time.Now().Add(time.Duration(seconds) * time.Second)}
	m.mu.Unlock()
}

func (m *Manager) Token(ctx context.Context, name string) (string, error) {
	m.mu.Lock()
	cached := m.access[name]
	m.mu.Unlock()
	if cached.Value != "" && time.Until(cached.Expires) > 30*time.Second {
		return cached.Value, nil
	}
	definition, _, err := m.vault.Google(name)
	if err != nil {
		return "", errors.New(signedOut)
	}
	persisted, err := m.readCache(name)
	if err != nil {
		return "", errors.New(signedOut)
	}
	answer, err := m.exchange(ctx, definition, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {persisted.RefreshToken}})
	if err != nil {
		_ = m.clear(name)
		return "", errors.New(signedOut)
	}
	if answer.RefreshToken != "" && answer.RefreshToken != persisted.RefreshToken {
		persisted.RefreshToken = answer.RefreshToken
		if err := m.writeCache(name, persisted); err != nil {
			return "", errors.New(signedOut)
		}
	}
	m.remember(name, answer.AccessToken, answer.ExpiresIn)
	return answer.AccessToken, nil
}

func (m *Manager) Account(_ context.Context, name string) (string, error) {
	value, err := m.readCache(name)
	return value.Account, err
}
func (m *Manager) Origin(name string) (string, error) {
	_, entry, err := m.vault.Google(name)
	return entry.Origin, err
}
func (m *Manager) DeviceCode(context.Context, string) (identity.DeviceAuthorization, error) {
	return identity.DeviceAuthorization{}, errors.New("device-code sign-in is not offered by this provider")
}

func (m *Manager) clear(name string) error {
	m.ForgetMemoryForTest(name)
	store, err := m.vault.GoogleCache(name)
	if err != nil {
		return err
	}
	if err := store.Clear(); err != nil && !errors.Is(err, credential.ErrNotStored) {
		return err
	}
	return nil
}

func (m *Manager) SignOut(name string) error {
	persisted, err := m.readCache(name)
	var revokeErr error
	if err == nil {
		form := url.Values{"token": {persisted.RefreshToken}}
		req, _ := http.NewRequest(http.MethodPost, m.revokeURL, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, requestErr := m.httpClient.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if requestErr != nil {
			revokeErr = requestErr
		} else if resp.StatusCode/100 != 2 {
			revokeErr = fmt.Errorf("Google revocation returned %s", resp.Status)
		}
	}
	clearErr := m.clear(name)
	if revokeErr != nil {
		return revokeErr
	}
	return clearErr
}
