package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/signing"
)

type readOnlySigningManager struct{ status signing.Status }

func (m readOnlySigningManager) Status(context.Context, signing.Request) (signing.Status, error) {
	return m.status, nil
}
func (readOnlySigningManager) Create(context.Context, signing.Request) (signing.Result, error) {
	panic("unexpected signing mutation")
}
func (readOnlySigningManager) Import(context.Context, signing.Request) (signing.Result, error) {
	panic("unexpected signing mutation")
}
func (readOnlySigningManager) Select(context.Context, signing.Request) (signing.Result, error) {
	panic("unexpected signing mutation")
}
func (readOnlySigningManager) Export(context.Context, signing.Request) ([]byte, error) {
	panic("unexpected signing mutation")
}
func (readOnlySigningManager) Sign(context.Context, signing.Request) (signing.Result, error) {
	panic("unexpected signing mutation")
}

func TestSigningIsReadOnlyBuildStatusAndHasNoAPI2jw(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	server := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: root}, events.NewBus())
	server.SetSigningManager(readOnlySigningManager{status: signing.Status{Supported: true, Files: []signing.FileStatus{{Status: "Valid", Timestamped: true}}}})
	if err := server.RefreshSigningState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state := server.signingState(); len(state.Files) != 1 || !state.Files[0].Timestamped {
		t.Fatalf("signature state=%+v", state)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/signing", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("removed signing endpoint status=%d body=%s", response.Code, response.Body)
	}
}
