package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/signing"
)

type fakeSigningManager struct {
	status signing.Status
	request signing.Request
	called string
}
func (m *fakeSigningManager) Status(context.Context, signing.Request) (signing.Status, error) { return m.status, nil }
func (m *fakeSigningManager) Create(context.Context, signing.Request) (signing.Result, error) { m.called="create"; return signing.Result{Thumbprint:"ABC123", Subject:"CN=test", Message:"created"},nil }
func (m *fakeSigningManager) Import(context.Context, signing.Request) (signing.Result, error) { m.called="import"; return signing.Result{Thumbprint:"ABC123"},nil }
func (m *fakeSigningManager) Select(_ context.Context, request signing.Request) (signing.Result, error) { m.called="select"; m.request=request; return signing.Result{Thumbprint:request.Thumbprint},nil }
func (m *fakeSigningManager) Export(context.Context, signing.Request) ([]byte,error) { m.called="export"; return []byte("cer"),nil }
func (m *fakeSigningManager) Sign(_ context.Context, request signing.Request) (signing.Result,error) { m.called="sign"; m.request=request; return signing.Result{Thumbprint:request.Thumbprint,Message:"restart"},nil }

func TestSigningAPIRequiresVerifiedManageCapableOperatorAndPersistsThumbprint(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	path := filepath.Join(root,"harness.json")
	if err := cfg.Save(path); err != nil { t.Fatal(err) }
	manager := &fakeSigningManager{status: signing.Status{Supported:true,CanManage:true}}
	server := New(&cfg,path,root,RuntimeRoots{Application:root,Data:root,Workspace:root},events.NewBus())
	server.SetSigningManager(manager)
	server.operatorRequest = func(*http.Request) error { return nil }
	handler := server.Handler()
	request := httptest.NewRequest(http.MethodPost,"/api/signing",strings.NewReader(`{"action":"select","thumbprint":"ab c123"}`))
	request.Header.Set("X-AgentB-Mutation-Token",server.mutationToken)
	response := httptest.NewRecorder(); handler.ServeHTTP(response,request)
	if response.Code != http.StatusOK || manager.called != "select" || server.ConfigSnapshot().Signing.Thumbprint != "ABC123" { t.Fatalf("code=%d body=%s called=%s config=%+v",response.Code,response.Body.String(),manager.called,server.ConfigSnapshot().Signing) }
	var saved config.Config
	data, _ := os.ReadFile(path); if err := json.Unmarshal(data,&saved); err != nil || saved.Signing.Thumbprint!="ABC123" { t.Fatalf("saved=%+v err=%v",saved.Signing,err) }
}

func TestSigningAPIStandardUserCanVerifyOnly(t *testing.T) {
	root := t.TempDir(); cfg := config.Defaults(root)
	manager := &fakeSigningManager{status: signing.Status{Supported:true,CanManage:false}}
	server := New(&cfg,filepath.Join(root,"harness.json"),root,RuntimeRoots{Application:root,Data:root,Workspace:root},events.NewBus())
	server.SetSigningManager(manager); server.operatorRequest = func(*http.Request) error { return nil }
	get := httptest.NewRecorder(); server.Handler().ServeHTTP(get,httptest.NewRequest(http.MethodGet,"/api/signing",nil))
	if get.Code != http.StatusOK { t.Fatalf("verify code=%d body=%s",get.Code,get.Body.String()) }
	postRequest := httptest.NewRequest(http.MethodPost,"/api/signing",strings.NewReader(`{"action":"create"}`)); postRequest.Header.Set("X-AgentB-Mutation-Token",server.mutationToken)
	post := httptest.NewRecorder(); server.Handler().ServeHTTP(post,postRequest)
	if post.Code != http.StatusForbidden || manager.called != "" { t.Fatalf("manage code=%d body=%s called=%s",post.Code,post.Body.String(),manager.called) }
}
