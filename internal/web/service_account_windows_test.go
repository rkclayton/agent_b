//go:build windows

package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/credential"
	"harness/internal/events"
	"harness/internal/serviceaccount"
	"harness/internal/tools"
)

type fakeAccountManager struct {
	status       serviceaccount.Status
	setupResult  serviceaccount.SetupResult
	setupErr     error
	setupCalls   int
	setupAccount string
	setupPath    string
	setupReset   bool
	protection   *serviceaccount.Protection
}

func (m *fakeAccountManager) Status(context.Context, string) (serviceaccount.Status, error) {
	status := m.status
	if m.setupCalls > 0 && m.setupErr == nil {
		status.Exists, status.Enabled = true, true
	}
	return status, nil
}

func (m *fakeAccountManager) Setup(_ context.Context, account, path string, reset bool, protection *serviceaccount.Protection) (serviceaccount.SetupResult, error) {
	m.setupCalls++
	m.setupAccount, m.setupPath, m.setupReset = account, path, reset
	m.protection = protection
	return m.setupResult, m.setupErr
}

func serviceAccountTestServer(t *testing.T, manager serviceaccount.Manager) (*Server, *credential.Store, string) {
	t.Helper()
	root := t.TempDir()
	configPath := root + `\harness.json`
	cfg := config.Defaults(root)
	if err := cfg.Save(configPath); err != nil {
		t.Fatal(err)
	}
	store := credential.New(root)
	shell := tools.NewShell(cfg.Shell)
	shell.Configure(cfg)
	shell.SetCredentialStore(store)
	server := New(&cfg, configPath, root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, events.NewBus())
	server.SetShellSecurity(store, shell)
	server.SetServiceAccountManager(manager)
	server.shellTest = func(context.Context) (string, error) {
		return "service-account shell spawn succeeded", nil
	}
	return server, store, configPath
}

func randomTestPassword(t *testing.T) string {
	t.Helper()
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(value)
}

func TestServiceAccountSetupStoresTestsAndEnables(t *testing.T) {
	manager := &fakeAccountManager{
		status:      serviceaccount.Status{Supported: true, Account: "agentb-svc"},
		setupResult: serviceaccount.SetupResult{Attempted: true},
	}
	server, store, configPath := serviceAccountTestServer(t, manager)
	testCalls := 0
	server.shellTest = func(context.Context) (string, error) {
		testCalls++
		return "service-account shell spawn succeeded", nil
	}
	body := `{"action":"provision","connection_id":"local"}`
	request := httptest.NewRequest(http.MethodPost, "/api/service-account", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	authorizeMutation(request, server)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), `"ok":true`) || !strings.Contains(response.Body.String(), "protections") || testCalls != 1 || manager.setupCalls != 1 || manager.setupAccount != "agentb-svc" || manager.setupPath != store.Path() || manager.setupReset || manager.protection == nil {
		t.Fatalf("unexpected setup result: calls=%d account=%q path=%q reset=%v body=%s", manager.setupCalls, manager.setupAccount, manager.setupPath, manager.setupReset, response.Body)
	}
	stored, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) < 40 {
		t.Fatal("generated credential is unexpectedly short")
	}
	clearSecret(stored)
	loaded, _, _, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Shell.ServiceAccount.Enabled || loaded.Shell.ServiceAccount.Account != "agentb-svc" {
		t.Fatalf("service identity was not enabled: %+v", loaded.Shell.ServiceAccount)
	}
}

func TestServiceAccountSetupAdoptsExistingAccountByPasswordReset(t *testing.T) {
	manager := &fakeAccountManager{
		status:      serviceaccount.Status{Supported: true, Account: "another-name", Exists: true, Enabled: true},
		setupResult: serviceaccount.SetupResult{Attempted: true},
	}
	server, _, _ := serviceAccountTestServer(t, manager)
	server.cfg.Shell.ServiceAccount.Account = "operator-choice-is-ignored"
	request := httptest.NewRequest(http.MethodPost, "/api/service-account", strings.NewReader(`{"action":"provision","connection_id":"local"}`))
	request.Header.Set("Content-Type", "application/json")
	authorizeMutation(request, server)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK || manager.setupAccount != "agentb-svc" || !manager.setupReset || !strings.Contains(response.Body.String(), "adopted existing agentb-svc by password reset") {
		t.Fatalf("status=%d account=%q reset=%v body=%s", response.Code, manager.setupAccount, manager.setupReset, response.Body)
	}
}

func TestServiceAccountSetupLeavesWorkingExistingCredentialAlone(t *testing.T) {
	manager := &fakeAccountManager{status: serviceaccount.Status{Supported: true, Exists: true, Enabled: true}}
	server, store, _ := serviceAccountTestServer(t, manager)
	if err := store.Write([]byte(randomTestPassword(t))); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/service-account", strings.NewReader(`{"action":"provision","connection_id":"local"}`))
	request.Header.Set("Content-Type", "application/json")
	authorizeMutation(request, server)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK || manager.setupCalls != 0 || !strings.Contains(response.Body.String(), "no account change was needed") {
		t.Fatalf("status=%d setup_calls=%d body=%s", response.Code, manager.setupCalls, response.Body)
	}
}

func TestServiceAccountStatusNamesFourProvisioningStates(t *testing.T) {
	tests := []struct {
		name, state, action   string
		exists, stored, works bool
	}{
		{name: "missing account", state: "missing", action: "Set up"},
		{name: "existing account missing credential", exists: true, state: "missing_credential", action: "Repair"},
		{name: "existing account valid credential", exists: true, stored: true, works: true, state: "ready"},
		{name: "existing account invalid credential", exists: true, stored: true, state: "credential_check_failed", action: "Repair"},
		{name: "locked account", exists: true, stored: true, state: "locked_out", action: "Repair"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := &fakeAccountManager{status: serviceaccount.Status{Supported: true, Exists: tt.exists, Enabled: true, LockedOut: tt.state == "locked_out"}}
			server, store, _ := serviceAccountTestServer(t, manager)
			if tt.stored {
				if err := store.Write([]byte(randomTestPassword(t))); err != nil {
					t.Fatal(err)
				}
			}
			server.shellTest = func(context.Context) (string, error) {
				if tt.works {
					return "ready", nil
				}
				return "credential rejected", errors.New("credential rejected")
			}
			request := httptest.NewRequest(http.MethodGet, "/api/service-account", nil)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"account":"agentb-svc"`) || !strings.Contains(response.Body.String(), `"state":"`+tt.state+`"`) || (tt.action != "" && !strings.Contains(response.Body.String(), `"action":"`+tt.action+`"`)) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
		})
	}
}

func TestRejectedCredentialIsPresentedOnceAndTransientFailureRetries(t *testing.T) {
	manager := &fakeAccountManager{status: serviceaccount.Status{Supported: true, Exists: true, Enabled: true}}
	server, store, _ := serviceAccountTestServer(t, manager)
	if err := store.Write([]byte(randomTestPassword(t))); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	server.shellTest = func(context.Context) (string, error) {
		attempts++
		return "credential rejected", tools.ErrServiceCredentialRejected
	}
	for range 2 {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/service-account", nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"invalid_credential"`) {
			t.Fatalf("body=%s", response.Body)
		}
	}
	if attempts != 1 || store.Status().Stored {
		t.Fatalf("attempts=%d stored=%v", attempts, store.Status().Stored)
	}

	server.credentialRejected = false
	if err := store.Write([]byte(randomTestPassword(t))); err != nil {
		t.Fatal(err)
	}
	server.shellTest = func(context.Context) (string, error) {
		attempts++
		return "temporary failure", errors.New("temporary failure")
	}
	for range 2 {
		server.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/service-account", nil))
	}
	if attempts != 3 || !store.Status().Stored {
		t.Fatalf("transient attempts=%d stored=%v", attempts, store.Status().Stored)
	}
}

func TestServiceAccountSetupFailedTestLeavesSplitOnAndBlocked(t *testing.T) {
	manager := &fakeAccountManager{status: serviceaccount.Status{Supported: true, Account: "agentb-svc"}, setupResult: serviceaccount.SetupResult{Attempted: true}}
	server, _, configPath := serviceAccountTestServer(t, manager)
	server.shellTest = func(context.Context) (string, error) { return "credential rejected", errors.New("bad credential") }
	request := httptest.NewRequest(http.MethodPost, "/api/service-account", strings.NewReader(`{"action":"provision","connection_id":"local"}`))
	request.Header.Set("Content-Type", "application/json")
	authorizeMutation(request, server)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "not set up") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	loaded, _, _, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Shell.ServiceAccount.Enabled {
		t.Fatal("failed credential test silently disabled the service split")
	}
}

func TestServiceAccountSetupRejectsLegacyPasswordActionBeforeMutation(t *testing.T) {
	manager := &fakeAccountManager{status: serviceaccount.Status{Supported: true, Account: "agentb-svc"}}
	server, store, _ := serviceAccountTestServer(t, manager)
	request := httptest.NewRequest(http.MethodPost, "/api/service-account", strings.NewReader(`{"action":"create","password":"must-not-be-used"}`))
	request.Header.Set("Content-Type", "application/json")
	authorizeMutation(request, server)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || manager.setupCalls != 0 || store.Status().Stored {
		t.Fatalf("mismatch mutated state: status=%d calls=%d stored=%v body=%s", response.Code, manager.setupCalls, store.Status().Stored, response.Body)
	}
}

func TestServiceAccountCanceledElevationRestoresCredential(t *testing.T) {
	manager := &fakeAccountManager{
		status:      serviceaccount.Status{Supported: true, Account: "agentb-svc"},
		setupResult: serviceaccount.SetupResult{},
		setupErr:    errors.New("Windows elevation was canceled"),
	}
	server, store, _ := serviceAccountTestServer(t, manager)
	previous := randomTestPassword(t)
	if err := store.Write([]byte(previous)); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/service-account", strings.NewReader(`{"action":"provision","connection_id":"local"}`))
	request.Header.Set("Content-Type", "application/json")
	authorizeMutation(request, server)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	stored, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	defer clearSecret(stored)
	if string(stored) != previous {
		t.Fatal("prior credential was not restored after canceled elevation")
	}
	if bytes.Contains(response.Body.Bytes(), []byte(previous)) {
		t.Fatal("cancellation response returned a credential")
	}
}

func TestServiceAccountAttemptedFailureRetainsSubmittedCredentialAndWarns(t *testing.T) {
	manager := &fakeAccountManager{
		status:      serviceaccount.Status{Supported: true, Account: "agentb-svc"},
		setupResult: serviceaccount.SetupResult{Attempted: true},
		setupErr:    errors.New("setup validation failed"),
	}
	server, store, _ := serviceAccountTestServer(t, manager)
	request := httptest.NewRequest(http.MethodPost, "/api/service-account", strings.NewReader(`{"action":"provision","connection_id":"local"}`))
	request.Header.Set("Content-Type", "application/json")
	authorizeMutation(request, server)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"attempted":true`) || !strings.Contains(response.Body.String(), "potentially partial") && !strings.Contains(response.Body.String(), "script started") {
		t.Fatalf("partial failure was not loud: status=%d body=%s", response.Code, response.Body)
	}
	stored, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	defer clearSecret(stored)
	if len(stored) < 40 {
		t.Fatal("generated credential was not retained after a potentially partial password change")
	}
}

func TestGeneratedServicePasswordIsLongAndRandom(t *testing.T) {
	first, err := generatedServicePassword()
	if err != nil {
		t.Fatal(err)
	}
	second, err := generatedServicePassword()
	if err != nil {
		clearSecret(first)
		t.Fatal(err)
	}
	defer clearSecret(first)
	defer clearSecret(second)
	if len(first) < 40 || bytes.Equal(first, second) || bytes.Contains(first, []byte("\r")) || bytes.Contains(first, []byte("\n")) {
		t.Fatalf("generated credentials did not meet the noninteractive contract")
	}
}
