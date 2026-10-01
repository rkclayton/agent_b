package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"harness/internal/buildinfo"
	"harness/internal/config"
	"harness/internal/credential"
	"harness/internal/events"
	"harness/internal/identity"
)

type recordedIdentityProvider struct {
	origin, account            string
	signIns, devices, signOuts int
}

func (p *recordedIdentityProvider) Origin(string) (string, error) { return p.origin, nil }
func (p *recordedIdentityProvider) Token(context.Context, string) (string, error) {
	return "planted-token", nil
}
func (p *recordedIdentityProvider) SignIn(context.Context, string) (string, error) {
	p.signIns++
	p.account = "someone@example.org"
	return p.account, nil
}
func (p *recordedIdentityProvider) DeviceCode(context.Context, string) (identity.DeviceAuthorization, error) {
	p.devices++
	return identity.DeviceAuthorization{UserCode: "ABCD-EFGH", VerificationURL: "https://verify.example.test", Message: "Use the code", Wait: func(context.Context) (string, error) {
		p.account = "someone@example.org"
		return p.account, nil
	}}, nil
}
func (p *recordedIdentityProvider) Account(context.Context, string) (string, error) {
	return p.account, nil
}
func (p *recordedIdentityProvider) SignOut(string) error {
	p.signOuts++
	p.account = ""
	return nil
}

func authorizeMutation(request *http.Request, server *Server) {
	request.Header.Set("X-AgentB-Mutation-Token", server.mutationToken)
	request.AddCookie(&http.Cookie{Name: browserSessionCookie, Value: server.browserSession})
}

func TestConfigPOSTRetainsSchemaStamp(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "harness.json")
	cfg := config.Defaults(root)
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	server := New(&cfg, path, root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, events.NewBus())
	request := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(`{"approval":{"mode":"mutating"}}`))
	authorizeMutation(request, server)
	request.Host = "example.com"
	request.Header.Set("Origin", "http://example.com")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	var returned config.Config
	if err := json.Unmarshal(response.Body.Bytes(), &returned); err != nil {
		t.Fatal(err)
	}
	if returned.ConfigVersion != config.CurrentConfigVersion || returned.Approval.Mode != config.ApprovalModeMutating {
		t.Fatalf("response version=%d mode=%q", returned.ConfigVersion, returned.Approval.Mode)
	}
	loaded, _, _, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ConfigVersion != config.CurrentConfigVersion || loaded.Approval.Mode != config.ApprovalModeMutating {
		t.Fatalf("disk version=%d mode=%q", loaded.ConfigVersion, loaded.Approval.Mode)
	}
}

func TestMutationGuardRequiresLaunchTokenAndSameOrigin(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	server := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, events.NewBus())

	request := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(`{"approval":{"mode":"all"}}`))
	request.AddCookie(&http.Cookie{Name: browserSessionCookie, Value: server.browserSession})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || response.Body.Len() != 0 {
		t.Fatalf("missing token status=%d body=%s", response.Code, response.Body)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(`{"approval":{"mode":"all"}}`))
	authorizeMutation(request, server)
	request.Header.Set("Origin", "https://example.invalid")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || response.Body.Len() != 0 {
		t.Fatalf("cross-origin status=%d body=%s", response.Code, response.Body)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(`{"approval":{"mode":"all"}}`))
	authorizeMutation(request, server)
	request.Header.Set("Origin", "http://example.com")
	request.Host = "example.com"
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("same-origin status=%d body=%s", response.Code, response.Body)
	}
}

func TestControlPlaneRequiresBrowserSession2jy(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	server := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, events.NewBus())

	for _, target := range []string{"/api/state", "/api/events"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("bare GET %s status=%d body=%s", target, response.Code, response.Body)
		}
	}

	request := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(`{"approval":{"mode":"all"}}`))
	request.Header.Set("X-AgentB-Mutation-Token", server.mutationToken)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("token without browser cookie status=%d body=%s", response.Code, response.Body)
	}
	if _, exposed := server.snapshotWithSessions(map[string]any{}, false)["mutation_token"]; exposed {
		t.Fatal("/api/state snapshot still exposes mutation_token")
	}
}

func TestBrowserCredentialBootstrapIsNotIssuedToToolDescendants2jy(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<!doctype html><html><head></head><body></body></html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults(root)
	server := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, events.NewBus())
	server.operatorRequest = func(*http.Request) error { return errors.New("Agent_b descendant") }

	page := httptest.NewRecorder()
	server.Handler().ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/?setup=skip", nil))
	if strings.Contains(page.Body.String(), server.mutationToken) || len(page.Result().Cookies()) != 0 {
		t.Fatalf("untrusted page received browser credentials: headers=%v body=%s", page.Header(), page.Body)
	}

	bootstrap := httptest.NewRequest(http.MethodPost, "/api/browser-session", nil)
	bootstrap.Header.Set("X-AgentB-Mutation-Token", server.mutationToken)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, bootstrap)
	if response.Code != http.StatusNoContent || len(response.Result().Cookies()) != 1 || !response.Result().Cookies()[0].HttpOnly {
		t.Fatalf("native bootstrap status=%d cookies=%+v", response.Code, response.Result().Cookies())
	}

	server.operatorRequest = func(*http.Request) error { return nil }
	page = httptest.NewRecorder()
	server.Handler().ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/?setup=skip", nil))
	if !strings.Contains(page.Body.String(), server.mutationToken) || len(page.Result().Cookies()) != 1 || !page.Result().Cookies()[0].HttpOnly {
		t.Fatalf("verified browser did not receive credentials: headers=%v body=%s", page.Header(), page.Body)
	}
}

func TestHostWindowActionsUseTheMutationBoundary(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	server := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, events.NewBus())
	var got string
	server.SetHostWindowAction(func(action string) bool { got = action; return true })

	request := httptest.NewRequest(http.MethodPost, "/api/host-window", strings.NewReader(`{"action":"maximize"}`))
	authorizeMutation(request, server)
	request.Host = "example.com"
	request.Header.Set("Origin", "http://example.com")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || got != "maximize" {
		t.Fatalf("status=%d action=%q body=%s", response.Code, got, response.Body)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/host-window", strings.NewReader(`{"action":"open"}`))
	authorizeMutation(request, server)
	request.Host = "example.com"
	request.Header.Set("Origin", "http://example.com")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid action status=%d body=%s", response.Code, response.Body)
	}
}

func TestSecurityHeaders(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	server := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, events.NewBus())
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	for _, name := range []string{"Content-Security-Policy", "Referrer-Policy", "X-Content-Type-Options", "X-Frame-Options"} {
		if response.Header().Get(name) == "" {
			t.Errorf("missing %s", name)
		}
	}
}

func TestSnapshotToolInventoryUsesPublicNames(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	server := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, events.NewBus())
	raw := server.snapshotWithSessions(map[string]any{}, false)["tools"].([]map[string]string)
	names := make([]string, 0, len(raw))
	for _, item := range raw {
		names = append(names, item["name"])
		if item["description"] == "" {
			t.Errorf("tool %q has no description", item["name"])
		}
		// Item 2gb (v1.0.1): the description names the dialect the command runs
		// in — PowerShell 7 where the host has it, else 5.1 with the two chain
		// operators rewritten.
		if item["name"] == "shell" && !strings.Contains(item["description"], "PowerShell 7: `&&` and `||` work.") && !strings.Contains(item["description"], "Windows PowerShell 5.1: `&&` and `||` are rewritten") {
			t.Errorf("shell description = %q", item["description"])
		}
	}
	want := []string{"read_file", "list_dir", "write_file", "edit_file", "search_text", "shell", "remember", "recall", "fetch_url", "web_search", "find_files", "run_script", "call_service", "delegate"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("tool inventory = %v, want %v", names, want)
	}
}

func TestSnapshotExposesBuildIdentity(t *testing.T) {
	oldCommit, oldDirty := buildinfo.Commit, buildinfo.Dirty
	t.Cleanup(func() { buildinfo.Commit, buildinfo.Dirty = oldCommit, oldDirty })
	buildinfo.Commit, buildinfo.Dirty = "abcdef0123456789", "true"
	root := t.TempDir()
	cfg := config.Defaults(root)
	server := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, events.NewBus())
	info := server.snapshotWithSessions(map[string]any{}, false)["build"].(buildinfo.Info)
	if info.Commit != buildinfo.Commit || info.Display != "abcdef012345+dirty" || !info.Dirty {
		t.Fatalf("build identity = %+v", info)
	}
}

// Item 2nv (c): CREDENTIALS ARE THE OPERATOR'S PAGE AND NOTHING ELSE. A caller without
// the page's session and mutation token, and a caller carrying a phone bearer, are both
// refused — for the listing as well as for a change, because the listing names what is
// stored and where it may go.
func TestOnlyTheOperatorsPageTouchesCredentials2nv(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	server := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, events.NewBus())
	SetCredentialVault(credential.NewVault(root))
	t.Cleanup(func() { SetCredentialVault(nil) })

	add := `{"action":"add","name":"depot","origin":"https://api.example.test:8443","secret":"planted"}`
	for _, test := range []struct {
		name    string
		build   func() *http.Request
		wantNot int
	}{
		{"no token", func() *http.Request {
			request := httptest.NewRequest(http.MethodPost, "/api/credentials", strings.NewReader(add))
			request.AddCookie(&http.Cookie{Name: browserSessionCookie, Value: server.browserSession})
			return request
		}, http.StatusOK},
		{"no session", func() *http.Request {
			request := httptest.NewRequest(http.MethodPost, "/api/credentials", strings.NewReader(add))
			request.Header.Set("X-AgentB-Mutation-Token", server.mutationToken)
			return request
		}, http.StatusOK},
		{"listing without the session", func() *http.Request {
			return httptest.NewRequest(http.MethodGet, "/api/credentials", nil)
		}, http.StatusOK},
		{"a phone bearer", func() *http.Request {
			request := httptest.NewRequest(http.MethodGet, "/api/credentials", nil)
			request.Header.Set("Authorization", "Bearer retired-browser-credential")
			return request
		}, http.StatusOK},
		{"a phone bearer changing one", func() *http.Request {
			request := httptest.NewRequest(http.MethodPost, "/api/credentials", strings.NewReader(add))
			request.Header.Set("Authorization", "Bearer retired-browser-credential")
			return request
		}, http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, test.build())
			if response.Code == test.wantNot {
				t.Fatalf("the caller was served: %d %s", response.Code, response.Body)
			}
			if strings.Contains(response.Body.String(), "planted") {
				t.Fatal("the refusal echoed the secret")
			}
		})
	}

	// And the operator's own page can, with the listing carrying no value. Storing is
	// DPAPI, so only this half is Windows-only; every refusal above is not.
	if runtime.GOOS != "windows" {
		t.Skip("SKIPPED the storing half: credential storage is DPAPI. The refusals above ran.")
	}
	request := httptest.NewRequest(http.MethodPost, "/api/credentials", strings.NewReader(add))
	authorizeMutation(request, server)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("the operator's page was refused: %d %s", response.Code, response.Body)
	}
	if strings.Contains(response.Body.String(), "planted") {
		t.Fatalf("the answer carries the secret: %s", response.Body)
	}
	if !strings.Contains(response.Body.String(), "https://api.example.test:8443") {
		t.Fatalf("the answer does not list the credential: %s", response.Body)
	}
}

func TestOperatorsPageManagesEntraSignIn2nw(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the credential store is DPAPI")
	}
	root := t.TempDir()
	cfg := config.Defaults(root)
	server := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, events.NewBus())
	vault := credential.NewVault(root)
	SetCredentialVault(vault)
	t.Cleanup(func() { SetCredentialVault(nil); SetIdentityProvider("entra", nil) })
	provider := &recordedIdentityProvider{origin: "https://api.example.test"}
	SetIdentityProvider("entra", provider)

	post := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/credentials", strings.NewReader(body))
		authorizeMutation(request, server)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		return response
	}
	added := post(`{"action":"add_entra","name":"work-api","origin":"https://api.example.test","tenant":"tenant-placeholder","client_id":"client-placeholder","scopes":"api.read api.write"}`)
	if added.Code != http.StatusOK || !strings.Contains(added.Body.String(), `"kind":"entra"`) || strings.Contains(added.Body.String(), "tenant-placeholder") {
		t.Fatalf("add response=%d %s", added.Code, added.Body)
	}
	provider.signOuts = 0
	signed := post(`{"action":"sign_in","name":"work-api"}`)
	if signed.Code != http.StatusOK || provider.signIns != 1 || !strings.Contains(signed.Body.String(), "someone@example.org") {
		t.Fatalf("sign-in response=%d %s calls=%d", signed.Code, signed.Body, provider.signIns)
	}
	device := post(`{"action":"device_code","name":"work-api"}`)
	if device.Code != http.StatusOK || provider.devices != 1 || !strings.Contains(device.Body.String(), "ABCD-EFGH") || !strings.Contains(device.Body.String(), "https://verify.example.test") {
		t.Fatalf("device response=%d %s calls=%d", device.Code, device.Body, provider.devices)
	}
	time.Sleep(10 * time.Millisecond)
	signedOut := post(`{"action":"sign_out","name":"work-api"}`)
	if signedOut.Code != http.StatusOK || provider.signOuts != 1 || !strings.Contains(signedOut.Body.String(), `"sign_in_needed":true`) {
		t.Fatalf("sign-out response=%d %s calls=%d", signedOut.Code, signedOut.Body, provider.signOuts)
	}
}

// Item 2o9 CHECKS 2-4 at the registry: the toggle writes the one Run entry Windows'
// Startup page lists; Windows turning it off reads back Off; on, the entry starts
// the app through the hidden host WITH its window, never -NoBrowser. The keys are
// pointed at a scratch path, so the operator's own Run key is never touched.
func TestSignInStartIsWindowsOwnSwitch2o9(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the Run entry is Windows'")
	}
	scratch := fmt.Sprintf(`Software\AgentB-test-2o9-%d`, os.Getpid())
	savedRun, savedApproved := signInRunKey, signInApprovedKey
	signInRunKey, signInApprovedKey = scratch+`\Run`, scratch+`\StartupApproved\Run`
	t.Cleanup(func() {
		signInRunKey, signInApprovedKey = savedRun, savedApproved
		_ = exec.Command("reg.exe", "delete", `HKCU\`+scratch, "/f").Run()
	})
	root := t.TempDir()
	cfg := config.Defaults(root)
	server := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, events.NewBus())
	call := func(method, body string) bool {
		request := httptest.NewRequest(method, "/api/sign-in-start", strings.NewReader(body))
		authorizeMutation(request, server)
		request.Host = "example.com"
		request.Header.Set("Origin", "http://example.com")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		var result struct{ Enabled bool }
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil {
			t.Fatalf("%s status=%d body=%s", method, response.Code, response.Body)
		}
		return result.Enabled
	}
	if call(http.MethodGet, "") {
		t.Fatal("nothing is registered, yet the switch reads On")
	}
	if !call(http.MethodPost, `{"enabled":true}`) {
		t.Fatal("turning it on did not read back On")
	}
	command, err := exec.Command("reg.exe", "query", `HKCU\`+signInRunKey, "/v", signInValue).Output()
	if err != nil || !strings.Contains(string(command), "launch-hidden.vbs") || !strings.Contains(string(command), " -window ") || strings.Contains(string(command), "NoBrowser") {
		t.Fatalf("the Run entry does not start the app with its window: %s (%v)", command, err)
	}
	// Windows' Startup page switching it off writes 03 to StartupApproved.
	if err := exec.Command("reg.exe", "add", `HKCU\`+signInApprovedKey, "/v", signInValue, "/t", "REG_BINARY", "/d", "030000000000000000000000", "/f").Run(); err != nil {
		t.Fatal(err)
	}
	if call(http.MethodGet, "") {
		t.Fatal("Windows turned it off, yet AgentB reads On")
	}
	if call(http.MethodPost, `{"enabled":false}`) {
		t.Fatal("turning it off did not read back Off")
	}
	if exec.Command("reg.exe", "query", `HKCU\`+signInRunKey, "/v", signInValue).Run() == nil {
		t.Fatal("the Run entry survived turning it off")
	}
}
