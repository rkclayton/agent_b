package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
)

func TestFilesRouteDownloadsWorkspaceBytesAndRefusesJailEscape(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	want := []byte("finished artifact\n")
	if err := os.MkdirAll(filepath.Join(workspace, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "reports", "final.txt"), want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults(workspace)
	server := New(&cfg, filepath.Join(t.TempDir(), "harness.json"), t.TempDir(), RuntimeRoots{Workspace: workspace}, events.NewBus())

	request := httptest.NewRequest(http.MethodGet, "/api/files/reports/final.txt", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	body, _ := io.ReadAll(response.Result().Body)
	if response.Code != http.StatusOK || string(body) != string(want) {
		t.Fatalf("status=%d body=%q", response.Code, body)
	}
	if disposition := response.Header().Get("Content-Disposition"); !strings.Contains(disposition, "attachment") || !strings.Contains(disposition, "final.txt") {
		t.Fatalf("Content-Disposition=%q", disposition)
	}

	escape := httptest.NewRequest(http.MethodGet, "/api/files/"+url.PathEscape(outside), nil)
	escape.URL.Path = "/api/files/" + outside
	refused := httptest.NewRecorder()
	server.file(refused, escape)
	if refused.Code != http.StatusNotFound || strings.Contains(refused.Body.String(), "secret") {
		t.Fatalf("jail escape status=%d body=%q", refused.Code, refused.Body.String())
	}

	directory := httptest.NewRequest(http.MethodGet, "/api/files/reports", nil)
	listed := httptest.NewRecorder()
	server.Handler().ServeHTTP(listed, directory)
	if listed.Code != http.StatusNotFound || strings.Contains(listed.Body.String(), "final.txt") {
		t.Fatalf("directory status=%d body=%q", listed.Code, listed.Body.String())
	}
}

func TestOpenFolderUsesJailResolvedWorkspaceFile(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "result.txt")
	if err := os.WriteFile(path, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults(workspace)
	server := New(&cfg, filepath.Join(t.TempDir(), "harness.json"), t.TempDir(), RuntimeRoots{Workspace: workspace}, events.NewBus())
	opened := ""
	server.openFolder = func(value string) error { opened = value; return nil }
	request := httptest.NewRequest(http.MethodPost, "/api/open-folder", strings.NewReader(`{"path":"result.txt"}`))
	authorizeMutation(request, server)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || opened != path {
		t.Fatalf("status=%d opened=%q body=%s", response.Code, opened, response.Body)
	}
}
