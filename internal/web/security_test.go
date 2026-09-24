package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"harness/internal/buildinfo"
	"harness/internal/config"
	"harness/internal/events"
)

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
	want := []string{"read_file", "list_dir", "write_file", "edit_file", "search_text", "shell", "remember", "recall", "fetch_url", "web_search", "find_files", "run_script", "call_service"}
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
