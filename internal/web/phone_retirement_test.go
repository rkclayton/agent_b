package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"harness/internal/config"
	"harness/internal/credential"
	"harness/internal/events"
)

func TestOlderPhoneBrowserEndpointsAreNotFound2pl(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	server := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Profile: root, Workspace: cfg.Workspace}, events.NewBus())
	handler := server.Handler()
	paths := []struct{ method, path string }{
		{http.MethodGet, "/phone"},
		{http.MethodGet, "/phone-sw.js"},
		{http.MethodGet, "/static/phone.js"},
		{http.MethodGet, "/static/phone.css"},
		{http.MethodPost, "/api/phone/enrolment"},
		{http.MethodPost, "/api/phone/enrolment/redeem"},
		{http.MethodGet, "/api/phone/devices"},
		{http.MethodPost, "/api/phone/devices/revoke"},
		{http.MethodPost, "/api/phone/devices/revoke-all"},
		{http.MethodGet, "/api/phone/push"},
		{http.MethodPost, "/api/phone/push/subscriptions"},
	}
	for _, endpoint := range paths {
		for _, signedIn := range []bool{false, true} {
			name := endpoint.method + " " + endpoint.path
			if signedIn {
				name += " signed-in"
			} else {
				name += " signed-out"
			}
			t.Run(name, func(t *testing.T) {
				request := httptest.NewRequest(endpoint.method, endpoint.path, nil)
				if signedIn {
					request.AddCookie(&http.Cookie{Name: browserSessionCookie, Value: server.browserSession})
					request.Header.Set("X-AgentB-Mutation-Token", server.mutationToken)
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != http.StatusNotFound {
					t.Fatalf("status=%d, want 404", response.Code)
				}
			})
		}
	}
}

func TestOlderPhoneBrowserProfileArtifactsAreRemovedAlone2pl(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "keep.bin")
	if err := os.WriteFile(keep, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := []string{filepath.Join(root, "phone-push.json")}
	store, err := credential.NewNamed(root, "web-push-vapid")
	if err != nil {
		t.Fatal(err)
	}
	legacy = append(legacy, store.Path())
	for _, path := range legacy {
		if err := os.WriteFile(path, []byte("old phone state"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := removeLegacyPhoneAccess(root); err != nil {
		t.Fatal(err)
	}
	for _, path := range legacy {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("legacy artifact remains: %s (%v)", path, err)
		}
	}
	if got, err := os.ReadFile(keep); err != nil || string(got) != "unchanged" {
		t.Fatalf("unrelated profile file changed: %q %v", got, err)
	}
}
