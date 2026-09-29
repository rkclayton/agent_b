package tools

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/credential"
	"harness/internal/session"
)

// Item 2nv: a connector's credential is a REFERENCE — `stored:<name>` — and every kind of
// credential goes only to the origin the operator approved. The planted secret in these
// cases is one string, and the last case looks for it in everything this tool writes.

const plantedSecret = "planted-2nv-secret-do-not-log"

func vaultConnector(t *testing.T, origin, header string) (*CallService, *credential.Vault, string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("SKIPPED: credential storage is DPAPI, so these cases are Windows-only.")
	}
	root := t.TempDir()
	vault := credential.NewVault(root)
	if err := vault.Put("depot", origin, header, plantedSecret); err != nil {
		t.Fatal(err)
	}
	service := config.Service{BaseURL: origin, Auth: "stored:depot", AllowedMethods: []string{"GET"}, TimeoutS: 5, MaxBodyKB: 16}
	tool := NewCallService(map[string]config.Service{"depot": service})
	tool.SetVault(vault)
	return tool, vault, root
}

// (b) and (f): the key reaches exactly its origin, as the header the binding names.
func TestAStoredCredentialReachesItsOriginAsItsHeader2nv(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("SKIPPED: credential storage is DPAPI, so these cases are Windows-only.")
	}
	var seen http.Header
	stub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer stub.Close()
	origin := stub.URL

	root := t.TempDir()
	vault := credential.NewVault(root)
	if err := vault.Put("depot", origin, "X-Depot-Key", plantedSecret); err != nil {
		t.Fatal(err)
	}
	service := config.Service{BaseURL: origin, Auth: "stored:depot", AllowedMethods: []string{"GET"}, TimeoutS: 5, MaxBodyKB: 16}
	tool := NewCallService(map[string]config.Service{"depot": service})
	tool.SetVault(vault)
	tool.SetHTTPClientForTest(stub.Client())

	if _, err := tool.Call(context.Background(), &session.Session{}, map[string]any{"service": "depot", "method": "GET", "path": "v1/queue"}); err != nil {
		t.Fatalf("the call was refused: %v", err)
	}
	if seen.Get("X-Depot-Key") != plantedSecret {
		t.Fatalf("the stub saw %q in its header", seen.Get("X-Depot-Key"))
	}
	if seen.Get("Authorization") != "" {
		t.Fatal("the credential was also sent as Authorization")
	}

	// With no header named, it is a bearer.
	if err := vault.Rebind("depot", origin, ""); err != nil {
		t.Fatal(err)
	}
	tool.Configure(config.Config{Services: map[string]config.Service{"depot": service}})
	tool.SetHTTPClientForTest(stub.Client())
	if _, err := tool.Call(context.Background(), &session.Session{}, map[string]any{"service": "depot", "method": "GET", "path": "v1/queue"}); err != nil {
		t.Fatal(err)
	}
	if seen.Get("Authorization") != "Bearer "+plantedSecret {
		t.Fatalf("the stub saw %q", seen.Get("Authorization"))
	}
}

// (f)'s refusals, each one BEFORE the credential is attached. The connector's base_url is
// the approved origin here; every case changes exactly one part of it.
func TestTheCredentialIsRefusedOffItsOrigin2nv(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("SKIPPED: credential storage is DPAPI, so these cases are Windows-only.")
	}
	reached := 0
	stub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer stub.Close()
	approved, err := url.Parse(stub.URL)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	vault := credential.NewVault(root)
	if err := vault.Put("depot", stub.URL, "", plantedSecret); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, base, want string }{
		{"http", "http://" + approved.Host, "https"},
		{"another port", "https://" + approved.Hostname() + ":9", "approved for"},
		{"port 443", "https://" + approved.Hostname(), "approved for"},
		{"another host", "https://127.0.0.2:" + approved.Port(), "approved for"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := config.Service{BaseURL: test.base, Auth: "stored:depot", AllowedMethods: []string{"GET"}, TimeoutS: 5, MaxBodyKB: 16}
			tool := NewCallService(map[string]config.Service{"depot": service})
			tool.SetVault(vault)
			tool.SetHTTPClientForTest(stub.Client())
			_, err := tool.Call(context.Background(), &session.Session{}, map[string]any{"service": "depot", "method": "GET", "path": "v1/queue"})
			if err == nil {
				t.Fatalf("the call was allowed to %s", test.base)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("the refusal does not say why: %v", err)
			}
			if strings.Contains(err.Error(), plantedSecret) {
				t.Fatal("the refusal carries the secret")
			}
		})
	}
	if reached != 0 {
		t.Fatalf("%d refused call(s) reached the stub", reached)
	}
}

// (f): a redirect that leaves the origin is never followed with the credential — not even
// to the same host on another port or scheme.
func TestARedirectOffTheOriginIsNotFollowed2nv(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("SKIPPED: credential storage is DPAPI, so these cases are Windows-only.")
	}
	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"followed":true}`))
	}))
	defer elsewhere.Close()
	var away string
	stub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/queue" {
			http.Redirect(w, r, away+"/taken", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer stub.Close()
	away = elsewhere.URL

	root := t.TempDir()
	vault := credential.NewVault(root)
	if err := vault.Put("depot", stub.URL, "", plantedSecret); err != nil {
		t.Fatal(err)
	}
	tool := NewCallService(map[string]config.Service{"depot": {BaseURL: stub.URL, Auth: "stored:depot", AllowedMethods: []string{"GET"}, TimeoutS: 5, MaxBodyKB: 16}})
	tool.SetVault(vault)
	client := stub.Client()
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	tool.SetHTTPClientForTest(client)
	_, err := tool.Call(context.Background(), &session.Session{}, map[string]any{"service": "depot", "method": "GET", "path": "v1/queue"})
	if err == nil {
		t.Fatal("the redirect was followed")
	}
	if strings.Contains(err.Error(), plantedSecret) {
		t.Fatal("the refusal carries the secret")
	}
}

// (g): the helper is bounded, windowless, and its output is bound to the origin too.
func TestTheHelperIsBoundedAndItsOutputIsBoundToTheOrigin2nv(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("SKIPPED: the helper here is a Windows one.")
	}
	stub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer stub.Close()
	// A helper that prints nothing leaves the connector unauthenticated with one line.
	service := config.Service{BaseURL: stub.URL, Auth: `exec:cmd /c "rem" token`, AllowedMethods: []string{"GET"}, TimeoutS: 5, MaxBodyKB: 16}
	tool := NewCallService(map[string]config.Service{"depot": service})
	tool.SetHTTPClientForTest(stub.Client())
	_, err := tool.Call(context.Background(), &session.Session{}, map[string]any{"service": "depot", "method": "GET", "path": "v1/queue"})
	if err == nil || !strings.Contains(err.Error(), "auth_error") {
		t.Fatalf("an empty helper gave %v", err)
	}
	// A helper that hangs is killed at the connector's timeout rather than waited on.
	hanging := config.Service{BaseURL: stub.URL, Auth: `exec:powershell -NoProfile -Command "Start-Sleep -Seconds 30" token`, AllowedMethods: []string{"GET"}, TimeoutS: 1, MaxBodyKB: 16}
	tool = NewCallService(map[string]config.Service{"depot": hanging})
	tool.SetHTTPClientForTest(stub.Client())
	if _, err := tool.Call(context.Background(), &session.Session{}, map[string]any{"service": "depot", "method": "GET", "path": "v1/queue"}); err == nil {
		t.Fatal("a hanging helper was waited on")
	}
	// And the helper starts without a window: the quiet-start helper is applied.
	source, err := os.ReadFile(filepath.Join("call_service.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "quietproc.Quiet(command)") {
		t.Error("the credential helper is started without the quiet-start helper, so it can put a console window on the operator's desktop")
	}
}

// (i): a planted secret appears in NOTHING this tool writes — not the result, not the
// error text, not the log.
func TestThePlantedSecretIsInNothingTheToolWrites2nv(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("SKIPPED: credential storage is DPAPI, so these cases are Windows-only.")
	}
	// The stub reflects the credential back, which is the worst case: the tool must
	// scrub it out of the body it returns.
	stub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"echo":%q}`, r.Header.Get("Authorization"))
	}))
	defer stub.Close()
	root := t.TempDir()
	vault := credential.NewVault(root)
	if err := vault.Put("depot", stub.URL, "", plantedSecret); err != nil {
		t.Fatal(err)
	}
	tool := NewCallService(map[string]config.Service{"depot": {BaseURL: stub.URL, Auth: "stored:depot", AllowedMethods: []string{"GET"}, TimeoutS: 5, MaxBodyKB: 16}})
	tool.SetVault(vault)
	tool.SetHTTPClientForTest(stub.Client())

	var logged bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(previous)

	content, err := tool.Call(context.Background(), &session.Session{}, map[string]any{"service": "depot", "method": "GET", "path": "v1/queue"})
	if err != nil {
		t.Fatal(err)
	}
	// One more call that fails, so the error text is searched too.
	_, failure := tool.Call(context.Background(), &session.Session{}, map[string]any{"service": "depot", "method": "POST", "path": "v1/queue"})

	searched := map[string]string{
		"the tool result": content,
		"the log":         logged.String(),
		"the error text":  fmt.Sprint(failure),
	}
	// And the sanitized arguments an event would carry.
	sanitized, _ := json.Marshal(map[string]any{"service": "depot", "method": "GET", "path": "v1/queue"})
	searched["the event arguments"] = string(sanitized)
	for where, text := range searched {
		if strings.Contains(text, plantedSecret) {
			t.Errorf("the planted secret is in %s: %s", where, text)
		}
	}
	if !strings.Contains(content, "[redacted]") {
		t.Errorf("the reflected credential was not redacted: %s", content)
	}
}

// (b): `stored:` is a valid connector auth, and `static_bearer:` is refused for a NEW one.
func TestStoredIsValidAuthAndStaticBearerIsRefusedForNewConnectors2nv(t *testing.T) {
	if err := config.ValidateServiceAuth("stored:depot"); err != nil {
		t.Errorf("stored: was refused: %v", err)
	}
	if err := config.ValidateServiceAuth("stored:Depot Key"); err == nil {
		t.Error("stored: accepted a name that is not a slug")
	}
	// A configuration carrying one must still LOAD — the migration reads it — but nothing
	// may create or save one.
	if err := config.ValidateServiceAuth("static_bearer:DEPOT_KEY"); err != nil {
		t.Errorf("an existing static_bearer connector no longer loads: %v", err)
	}
	if err := config.ValidateNewServiceAuth("static_bearer:DEPOT_KEY"); err == nil {
		t.Error("static_bearer: is still accepted for a new connector")
	}
	change, present, err := ParseConnectorChange(map[string]any{"connector": map[string]any{
		"operation": "add",
		"entry":     map[string]any{"name": "depot", "url": "https://api.example.test", "kind": "http", "auth": "static_bearer:DEPOT_KEY", "allowed_methods": []any{"GET"}},
	}})
	if !present || err == nil {
		t.Fatalf("a static_bearer proposal was accepted: %+v", change)
	}
}
