package entra

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/credential"
	"harness/internal/session"
	"harness/internal/tools"
)

type identityRecording struct {
	sync.Mutex
	host, account                      string
	codePKCE, device, refresh, revoked int
}

func (r *identityRecording) handler(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	authority := "https://" + r.host + "/tenant-fixture"
	switch {
	case strings.HasSuffix(req.URL.Path, "/.well-known/openid-configuration"):
		json.NewEncoder(w).Encode(map[string]any{"token_endpoint": authority + "/oauth2/v2.0/token", "authorization_endpoint": authority + "/oauth2/v2.0/authorize", "device_authorization_endpoint": authority + "/oauth2/v2.0/devicecode", "issuer": authority + "/v2.0"})
	case strings.HasSuffix(req.URL.Path, "/devicecode"):
		r.Lock()
		r.device++
		r.Unlock()
		fmt.Fprint(w, `{"device_code":"device-proof","user_code":"ABCD-EFGH","verification_uri":"https://verify.example.test","expires_in":600,"interval":0,"message":"Use the code"}`)
	case strings.HasSuffix(req.URL.Path, "/token"):
		req.ParseForm()
		grant := req.Form.Get("grant_type")
		r.Lock()
		if grant == "authorization_code" && req.Form.Get("code_verifier") != "" {
			r.codePKCE++
		}
		if grant == "refresh_token" {
			r.refresh++
		}
		revoked, account := r.revoked > 0, r.account
		r.Unlock()
		if revoked && grant == "refresh_token" {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_grant"}`)
			return
		}
		clientInfo := base64.RawStdEncoding.EncodeToString([]byte(`{"uid":"user-fixture","utid":"tenant-fixture"}`))
		claims := base64.RawStdEncoding.EncodeToString([]byte(fmt.Sprintf(`{"aud":"client-fixture","exp":%d,"iat":%d,"iss":%q,"tid":"tenant-fixture","preferred_username":%q}`, time.Now().Add(time.Hour).Unix(), time.Now().Unix(), authority+"/v2.0", account)))
		fmt.Fprintf(w, `{"access_token":"access-%s-%d","expires_in":1,"token_type":"Bearer","refresh_token":"refresh-planted","client_info":%q,"id_token":"h.%s.s"}`, account, r.refresh, clientInfo, claims)
	default:
		http.NotFound(w, req)
	}
}

func TestInteractiveDeviceRefreshRevocationSwitchAndSignOut2nw(t *testing.T) {
	recording := &identityRecording{account: "first@example.test"}
	idp := httptest.NewTLSServer(http.HandlerFunc(recording.handler))
	defer idp.Close()
	recording.host = strings.TrimPrefix(idp.URL, "https://")
	seenBearer := ""
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBearer = r.Header.Get("Authorization")
		fmt.Fprintf(w, `{"echo":%q}`, seenBearer)
	}))
	defer api.Close()
	vault := credential.NewVault(t.TempDir())
	if err := vault.PutEntra("work-api", api.URL, "tenant-fixture", "client-fixture", []string{"api.read"}); err != nil {
		t.Fatal(err)
	}
	manager := New(vault)
	manager.setTestAuthority(func(credential.EntraDefinition) string { return idp.URL + "/tenant-fixture" }, idp.Client())
	connector := tools.NewCallService(map[string]config.Service{"depot": {BaseURL: api.URL, Auth: "entra:work-api", AllowedMethods: []string{"GET"}, TimeoutS: 5, MaxBodyKB: 16}})
	connector.SetTokenProvider("entra", manager)
	connector.SetHTTPClientForTest(api.Client())
	if result, err := connector.Call(context.Background(), &session.Session{}, map[string]any{"service": "depot", "method": "GET", "path": "items"}); err == nil || !strings.Contains(err.Error(), "sign in in Settings") || recording.codePKCE != 0 || recording.device != 0 {
		t.Fatalf("unsigned result=%q err=%v", result, err)
	}
	open := func(authURL string) error {
		u, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		if u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("code_challenge") == "" {
			return fmt.Errorf("PKCE missing")
		}
		resp, err := http.PostForm(u.Query().Get("redirect_uri"), url.Values{"state": {u.Query().Get("state")}, "code": {"code-proof"}})
		if resp != nil {
			resp.Body.Close()
		}
		return err
	}
	if account, err := manager.Interactive(context.Background(), "work-api", open); err != nil || account != recording.account {
		t.Fatalf("interactive account=%q err=%v", account, err)
	}
	if recording.codePKCE != 1 {
		t.Fatalf("PKCE exchanges=%d", recording.codePKCE)
	}
	foreign := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("credential reached a foreign destination")
	}))
	defer foreign.Close()
	foreignConnector := tools.NewCallService(map[string]config.Service{"foreign": {BaseURL: foreign.URL, Auth: "entra:work-api", AllowedMethods: []string{"GET"}, TimeoutS: 5, MaxBodyKB: 16}})
	foreignConnector.SetTokenProvider("entra", manager)
	foreignConnector.SetHTTPClientForTest(foreign.Client())
	if _, err := foreignConnector.Call(context.Background(), &session.Session{}, map[string]any{"service": "foreign", "method": "GET", "path": "items"}); err == nil || !strings.Contains(err.Error(), "not attached") {
		t.Fatalf("foreign destination err=%v", err)
	}
	if result, err := connector.Call(context.Background(), &session.Session{}, map[string]any{"service": "depot", "method": "GET", "path": "items"}); err != nil || !strings.HasPrefix(seenBearer, "Bearer access-first@example.test-") || strings.Contains(result, "access-first") {
		t.Fatalf("connector bearer=%q err=%v", seenBearer, err)
	}
	manager.ForgetMemoryForTest("work-api")
	if token, err := manager.Token(context.Background(), "work-api"); err != nil || !strings.HasPrefix(token, "access-first@example.test-") {
		t.Fatalf("refresh token=%q err=%v", token, err)
	}
	cache, err := vault.EntraCache("work-api")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := cache.Read()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "access-first") || !strings.Contains(string(raw), "refresh-planted") {
		t.Fatalf("cache did not keep refresh-only material: %s", raw)
	}
	recording.revoked = 1
	manager.ForgetMemoryForTest("work-api")
	refreshes := recording.refresh
	if _, err := manager.Token(context.Background(), "work-api"); err == nil || !strings.Contains(err.Error(), "sign in in Settings") {
		t.Fatalf("revoked err=%v", err)
	}
	if _, err := manager.Token(context.Background(), "work-api"); err == nil || recording.refresh != refreshes+1 {
		t.Fatalf("revocation looped: refreshes=%d err=%v", recording.refresh, err)
	}
	recording.revoked, recording.account = 0, "second@example.test"
	device, err := manager.DeviceCode(context.Background(), "work-api")
	if err != nil {
		t.Fatal(err)
	}
	if device.UserCode != "ABCD-EFGH" {
		t.Fatalf("device=%+v", device)
	}
	if account, err := device.Complete(context.Background()); err != nil || account != recording.account {
		t.Fatalf("device account=%q err=%v", account, err)
	}
	if account, _ := manager.Account(context.Background(), "work-api"); account != recording.account {
		t.Fatalf("account=%q", account)
	}
	if err := manager.SignOut("work-api"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Token(context.Background(), "work-api"); err == nil || !strings.Contains(err.Error(), "sign in in Settings") {
		t.Fatalf("signed-out err=%v", err)
	}
}
