package google

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"

	"harness/internal/config"
	"harness/internal/credential"
	"harness/internal/session"
	"harness/internal/tools"
)

type recordingIDP struct {
	sync.Mutex
	refreshes int
	revoked   bool
}

type rewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (r rewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.URL.Scheme, copy.URL.Host = r.target.Scheme, r.target.Host
	copy.Host = r.target.Host
	return r.base.RoundTrip(copy)
}

func (r *recordingIDP) serve(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch req.URL.Path {
	case "/token":
		_ = req.ParseForm()
		r.Lock()
		if req.Form.Get("grant_type") == "refresh_token" {
			r.refreshes++
		}
		revoked := r.revoked
		r.Unlock()
		if revoked {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `{"access_token":"google-access-proof","expires_in":1,"refresh_token":"google-refresh-proof","token_type":"Bearer"}`)
	case "/userinfo":
		fmt.Fprint(w, `{"email":"someone@example.org"}`)
	case "/revoke":
		r.Lock()
		r.revoked = true
		r.Unlock()
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, req)
	}
}

func TestInteractiveRefreshSignOutAndOrigin2xh(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the protected credential cache uses Windows DPAPI")
	}
	recording := &recordingIDP{}
	idp := httptest.NewTLSServer(http.HandlerFunc(recording.serve))
	defer idp.Close()
	seenBearer := ""
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seenBearer = req.Header.Get("Authorization")
		fmt.Fprintf(w, `{"reflected":%q}`, seenBearer)
	}))
	defer api.Close()
	vault := credential.NewVault(t.TempDir())
	if err := vault.PutGoogle("foreign", "https://api.example.test", "client-fixture", "secret-fixture", []string{"scope"}); err == nil {
		t.Fatal("Google credential accepted a non-googleapis origin")
	}
	const googleOrigin = "https://gmail.googleapis.com"
	if err := vault.PutGoogle("google-mail", googleOrigin, "client-fixture", "secret-fixture", []string{"https://www.googleapis.com/auth/gmail.readonly"}); err != nil {
		t.Fatal(err)
	}
	manager := New(vault)
	manager.setTestEndpoints(idp.URL+"/authorize", idp.URL+"/token", idp.URL+"/userinfo", idp.URL+"/revoke", idp.Client())
	opened := ""
	account, err := manager.Interactive(context.Background(), "google-mail", func(authURL string) error {
		opened = authURL
		u, parseErr := url.Parse(authURL)
		if parseErr != nil {
			return parseErr
		}
		if u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("access_type") != "offline" {
			return fmt.Errorf("PKCE or offline access missing")
		}
		redirect := u.Query().Get("redirect_uri")
		resp, postErr := http.PostForm(redirect, url.Values{"state": {u.Query().Get("state")}, "code": {"code-proof"}})
		if resp != nil {
			_ = resp.Body.Close()
		}
		return postErr
	})
	if err != nil || account != "someone@example.org" || opened == "" {
		t.Fatalf("account=%q opened=%t err=%v", account, opened != "", err)
	}
	connector := tools.NewCallService(map[string]config.Service{"mail": {BaseURL: googleOrigin, Auth: "google:google-mail", AllowedMethods: []string{"GET"}, TimeoutS: 5, MaxBodyKB: 16}})
	connector.SetTokenProvider("google", manager)
	apiTarget, _ := url.Parse(api.URL)
	connector.SetHTTPClientForTest(&http.Client{Transport: rewriteTransport{target: apiTarget, base: api.Client().Transport}})
	result, err := connector.Call(context.Background(), &session.Session{}, map[string]any{"service": "mail", "method": "GET", "path": "messages"})
	if err != nil || seenBearer != "Bearer google-access-proof" || strings.Contains(result, "google-access-proof") {
		t.Fatalf("bearer=%q result=%q err=%v", seenBearer, result, err)
	}
	if recording.refreshes == 0 {
		t.Fatal("expired access token did not refresh")
	}
	foreign := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("credential reached foreign origin") }))
	defer foreign.Close()
	foreignConnector := tools.NewCallService(map[string]config.Service{"foreign": {BaseURL: foreign.URL, Auth: "google:google-mail", AllowedMethods: []string{"GET"}, TimeoutS: 5, MaxBodyKB: 16}})
	foreignConnector.SetTokenProvider("google", manager)
	foreignConnector.SetHTTPClientForTest(foreign.Client())
	if _, foreignErr := foreignConnector.Call(context.Background(), &session.Session{}, map[string]any{"service": "foreign", "method": "GET", "path": "messages"}); foreignErr == nil || !strings.Contains(foreignErr.Error(), "not attached") {
		t.Fatalf("foreign error=%v", foreignErr)
	}
	definition, entry, err := vault.Google("google-mail")
	if err != nil || entry.Kind != "google" || definition.ClientSecret != "secret-fixture" {
		t.Fatalf("definition=%+v entry=%+v err=%v", definition, entry, err)
	}
	cache, err := vault.GoogleCache("google-mail")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := cache.Read()
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]string
	if json.Unmarshal(raw, &persisted) != nil || persisted["refresh_token"] != "google-refresh-proof" || strings.Contains(string(raw), "google-access-proof") {
		t.Fatalf("cache=%s", raw)
	}
	manager.ForgetMemoryForTest("google-mail")
	if token, tokenErr := manager.Token(context.Background(), "google-mail"); tokenErr != nil || token != "google-access-proof" {
		t.Fatalf("token=%q err=%v", token, tokenErr)
	}
	wantOrigin, _ := credential.NormalizeOrigin(googleOrigin)
	if origin, originErr := manager.Origin("google-mail"); originErr != nil || origin != wantOrigin {
		t.Fatalf("origin=%q err=%v", origin, originErr)
	}
	if _, deviceErr := manager.DeviceCode(context.Background(), "google-mail"); deviceErr == nil || !strings.Contains(deviceErr.Error(), "not offered by this provider") {
		t.Fatalf("device error=%v", deviceErr)
	}
	if err := manager.SignOut("google-mail"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Token(context.Background(), "google-mail"); err == nil || !strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("signed-out err=%v", err)
	}
}
