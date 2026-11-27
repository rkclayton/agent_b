package tools

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/credential"
	"harness/internal/session"
)

func TestCallServiceRegisteredMethodPathAndHeaderEnforcement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/redirect" {
			http.Redirect(w, r, "/outside", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	tool := NewCallService(map[string]config.Service{"known": testService(server.URL+"/api", "none")})
	item := &session.Session{}

	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"unknown", map[string]any{"service": "missing", "method": "GET", "path": "ok"}, "unknown service"},
		{"method", map[string]any{"service": "known", "method": "DELETE", "path": "ok"}, "not allowed"},
		{"foreign_absolute_url", map[string]any{"service": "known", "method": "GET", "path": "https://example.com/steal"}, "credential host mismatch"},
		{"absolute_path", map[string]any{"service": "known", "method": "GET", "path": "/outside"}, "relative"},
		{"dot_escape", map[string]any{"service": "known", "method": "GET", "path": "../outside"}, "escape"},
		{"encoded_escape", map[string]any{"service": "known", "method": "GET", "path": "..%2Foutside"}, "escape"},
		{"header", map[string]any{"service": "known", "method": "GET", "path": "ok", "headers": map[string]any{"X-Admin": "yes"}}, "not allowed"},
		{"authorization", map[string]any{"service": "known", "method": "GET", "path": "ok", "headers": map[string]any{"Authorization": "Bearer caller-secret"}}, "always configured"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := tool.Call(context.Background(), item, test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
	if detail := tool.CallDetailed(context.Background(), item, map[string]any{"service": "known", "method": "GET", "path": "redirect"}); detail.Err != nil {
		t.Fatalf("same-host redirect outside the configured base path: %+v", detail)
	}
}

func TestCallServiceDirectURLHasNoCredentialAndRegisteredCredentialStaysOnItsHost(t *testing.T) {
	t.Setenv("AGENTB_TEST_SERVICE_TOKEN", "registered-secret")
	var directAuthorization, registeredAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/direct":
			directAuthorization = r.Header.Get("Authorization")
		case "/registered":
			registeredAuthorization = r.Header.Get("Authorization")
		default:
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	service := testService(server.URL+"/api", "static_bearer:AGENTB_TEST_SERVICE_TOKEN")
	tool := NewCallService(map[string]config.Service{"registered": service})

	direct := tool.CallDetailed(context.Background(), &session.Session{}, map[string]any{"service": server.URL + "/direct", "method": "PATCH"})
	if direct.Err != nil || directAuthorization != "" || direct.OperatorContext {
		t.Fatalf("direct=%+v authorization=%q", direct, directAuthorization)
	}
	matching := tool.CallDetailed(context.Background(), &session.Session{}, map[string]any{"service": "registered", "method": "GET", "path": server.URL + "/registered"})
	if matching.Err != nil || registeredAuthorization != "Bearer registered-secret" {
		t.Fatalf("matching=%+v authorization=%q", matching, registeredAuthorization)
	}
	foreign := tool.CallDetailed(context.Background(), &session.Session{}, map[string]any{"service": "registered", "method": "GET", "path": "http://foreign.invalid/never"})
	if foreign.Err == nil || foreign.Err.Error() != `service "registered" credential host mismatch: registered host "`+strings.TrimPrefix(server.URL, "http://")+`", requested host "foreign.invalid"` {
		t.Fatalf("foreign=%+v", foreign)
	}
}

func TestCallServiceRefusesConfiguredAgentBListener2jy(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := config.Defaults(t.TempDir())
	cfg.Listen = strings.TrimPrefix(server.URL, "http://")
	tool := NewCallService(nil)
	tool.Configure(cfg)
	detail := tool.CallDetailed(context.Background(), &session.Session{}, map[string]any{"service": server.URL + "/api/state", "method": "GET"})
	if detail.Err == nil || !strings.Contains(detail.Err.Error(), "refused the Agent_b listener") {
		t.Fatalf("detail=%+v", detail)
	}
	if called {
		t.Fatal("refused listener request reached the server")
	}
}

func TestCallServiceRequestShapeAndFourXXAreResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/jobs/create" || r.URL.Query().Get("dry_run") != "true" || r.Header.Get("If-Match") != "v1" {
			t.Fatalf("request path=%q query=%q headers=%v", r.URL.Path, r.URL.RawQuery, r.Header)
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["name"] != "acme" {
			t.Fatalf("body=%#v", body)
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ETag", "v2")
		w.Header().Set("Set-Cookie", "never-return=this")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"title":"already exists"}`))
	}))
	defer server.Close()
	service := testService(server.URL+"/api", "none")
	service.AllowedMethods = []string{"POST"}
	tool := NewCallService(map[string]config.Service{"broker": service})
	detail := tool.CallDetailed(context.Background(), &session.Session{}, map[string]any{
		"service": "broker", "method": "POST", "path": "jobs/create",
		"query": map[string]any{"dry_run": true}, "body": map[string]any{"name": "acme"},
		"headers": map[string]any{"If-Match": "v1"},
	})
	if detail.Err != nil {
		t.Fatal(detail.Err)
	}
	registry := New(tool)
	registryItem := &session.Session{ToolsEnabled: map[string]bool{"call_service": true}}
	registered := registry.CallDetailed(context.Background(), registryItem, "call_service", map[string]any{
		"service": "broker", "method": "POST", "path": "jobs/create",
		"query": map[string]any{"dry_run": true}, "body": map[string]any{"name": "acme"},
		"headers": map[string]any{"If-Match": "v1"},
	})
	if !registered.OK {
		t.Fatalf("registered 4xx result=%+v", registered)
	}
	var result map[string]any
	if json.Unmarshal([]byte(detail.Content), &result) != nil || result["status"] != float64(http.StatusConflict) {
		t.Fatalf("result=%q", detail.Content)
	}
	headers := result["headers"].(map[string]any)
	if headers["content-type"] != "application/problem+json" || headers["etag"] != "v2" || headers["set-cookie"] != nil {
		t.Fatalf("headers=%#v", headers)
	}
	if detail.Metadata["service"] != "broker" || detail.Metadata["method"] != "POST" || detail.Metadata["status"] != http.StatusConflict {
		t.Fatalf("metadata=%#v", detail.Metadata)
	}
}

func TestCallServiceResponseCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("abcdefghij"))
	}))
	defer server.Close()
	tool := NewCallService(map[string]config.Service{"svc": testService(server.URL, "none")})
	first := callServiceResult(t, tool, map[string]any{"service": "svc", "method": "GET", "path": "body", "limit": float64(4)})
	if first["body"] != "abcd" || first["cursor"].(map[string]any)["next_offset"] != float64(5) {
		t.Fatalf("first=%#v", first)
	}
	second := callServiceResult(t, tool, map[string]any{"service": "svc", "method": "GET", "path": "body", "offset": float64(5), "limit": float64(4)})
	if second["body"] != "efgh" || second["cursor"].(map[string]any)["next_offset"] != float64(9) {
		t.Fatalf("second=%#v", second)
	}
}

func TestCallServiceSavesBinaryResponseWithoutShowingItsBytes2o4(t *testing.T) {
	payload := []byte("invented-workbook-bytes\x00\x01")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/large" {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(make([]byte, 1025))
			return
		}
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		w.Header().Set("Content-Disposition", `attachment; filename="../Quarter:*?.xlsx"`)
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	service := testService(server.URL, "none")
	service.MaxBodyKB = 1
	tool := NewCallService(map[string]config.Service{"depot": service})
	workspace := t.TempDir()
	item := &session.Session{Workspace: workspace}
	detail := tool.CallDetailed(context.Background(), item, map[string]any{"service": "depot", "method": "GET", "path": "report"})
	if detail.Err != nil {
		t.Fatal(detail.Err)
	}
	var result map[string]any
	if json.Unmarshal([]byte(detail.Content), &result) != nil || len(result) != 4 || result["path"] != "Quarter.xlsx" || result["bytes"] != float64(len(payload)) || strings.Contains(detail.Content, "invented-workbook") {
		t.Fatalf("result=%s", detail.Content)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(payload))
	if result["sha256"] != sum || result["content_type"] != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Fatalf("result=%#v", result)
	}
	stored, err := os.ReadFile(filepath.Join(workspace, "Quarter.xlsx"))
	if err != nil || string(stored) != string(payload) {
		t.Fatalf("stored=%q err=%v", stored, err)
	}
	if file, ok := detail.Metadata["file"].(map[string]any); !ok || file["path"] != "Quarter.xlsx" || file["bytes"] != int64(len(payload)) {
		t.Fatalf("metadata=%#v", detail.Metadata)
	}

	over := tool.CallDetailed(context.Background(), item, map[string]any{"service": "depot", "method": "GET", "path": "large"})
	if over.Err == nil || !strings.Contains(over.Err.Error(), "nothing was saved") {
		t.Fatalf("oversize=%+v", over)
	}
	entries, _ := os.ReadDir(workspace)
	if len(entries) != 1 {
		t.Fatalf("oversize left files: %v", entries)
	}
}

func TestCallServiceRefusesABinaryResponseThatReflectsItsCredential2o4(t *testing.T) {
	t.Setenv("AGENTB_TEST_SERVICE_TOKEN", "file-secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("prefix-" + r.Header.Get("Authorization") + "-suffix"))
	}))
	defer server.Close()
	tool := NewCallService(map[string]config.Service{"depot": testService(server.URL, "static_bearer:AGENTB_TEST_SERVICE_TOKEN")})
	workspace := t.TempDir()
	detail := tool.CallDetailed(context.Background(), &session.Session{Workspace: workspace}, map[string]any{"service": "depot", "method": "GET", "path": "report"})
	entries, _ := os.ReadDir(workspace)
	if detail.Err == nil || !strings.Contains(detail.Err.Error(), "contained a credential") || strings.Contains(detail.Err.Error(), "file-secret") || len(entries) != 0 {
		t.Fatalf("detail=%+v entries=%v", detail, entries)
	}
}

func TestCallServiceResponseCursorPreservesUTF8Boundaries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ab🙂cd"))
	}))
	defer server.Close()
	tool := NewCallService(map[string]config.Service{"svc": testService(server.URL, "none")})
	first := callServiceResult(t, tool, map[string]any{"service": "svc", "method": "GET", "path": "body", "limit": float64(5)})
	if first["body"] != "ab" || first["cursor"].(map[string]any)["next_offset"] != float64(3) {
		t.Fatalf("first=%#v", first)
	}
	second := callServiceResult(t, tool, map[string]any{"service": "svc", "method": "GET", "path": "body", "offset": float64(3), "limit": float64(5)})
	if second["body"] != "🙂c" || second["cursor"].(map[string]any)["next_offset"] != float64(8) {
		t.Fatalf("second=%#v", second)
	}
	if _, err := tool.Call(context.Background(), &session.Session{}, map[string]any{"service": "svc", "method": "GET", "path": "body", "offset": float64(4), "limit": float64(5)}); err == nil || !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Fatalf("mid-rune error=%v", err)
	}
}

func TestCallServiceStaticBearerPrefixAndReflectedCredentialRedaction(t *testing.T) {
	t.Setenv("AGENTB_TEST_SERVICE_TOKEN", "Bearer reflected-secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer reflected-secret" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		_, _ = fmt.Fprintf(w, "auth=%s token=reflected-secret", r.Header.Get("Authorization"))
	}))
	defer server.Close()
	tool := NewCallService(map[string]config.Service{"svc": testService(server.URL, "static_bearer:AGENTB_TEST_SERVICE_TOKEN")})
	detail := tool.CallDetailed(context.Background(), &session.Session{}, map[string]any{"service": "svc", "method": "GET", "path": "check"})
	if detail.Err != nil || strings.Contains(detail.Content, "reflected-secret") || !strings.Contains(detail.Content, "[redacted]") || detail.OperatorContext {
		t.Fatalf("detail=%+v", detail)
	}
}

func TestCallServiceExecCredentialSuccessBearerAndCacheExpiry(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "count.txt")
	expires := "2099-01-01T00:10:00Z"
	auth := "exec:" + helperCredentialCommand("json", counter, expires)
	var requests int
	// Item 2nv (f): a credential — including a helper's output — goes only to an https
	// origin, so this case's stub is TLS. It was plain http until then.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer exec-secret" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	// Item 2wk: the subject is success, the bearer and the cache, not the helper's timeout (its own
	// test below). The helper is this test binary started again: its first start measured
	// 0.08–1.26 s alone and 1.13–7.94 s under the apart suite's load (later starts 35–65 ms), so
	// the fixture's 2 s timed the machine's first launch of a freshly linked executable.
	service := testService(server.URL, auth)
	service.TimeoutS = 30
	tool := NewCallService(map[string]config.Service{"svc": service})
	tool.SetHTTPClientForTest(server.Client())
	clock := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	tool.now = func() time.Time { return clock }
	args := map[string]any{"service": "svc", "method": "GET", "path": "check"}
	for _, advance := range []time.Duration{0, 8 * time.Minute, time.Minute} {
		clock = clock.Add(advance)
		called := time.Now()
		detail := tool.CallDetailed(context.Background(), &session.Session{}, args)
		t.Logf("call after %v took %v", advance, time.Since(called))
		if detail.Err != nil || !detail.OperatorContext {
			t.Fatalf("detail=%+v", detail)
		}
	}
	data, err := os.ReadFile(counter)
	if err != nil || strings.Count(string(data), "x") != 2 || requests != 3 {
		t.Fatalf("counter=%q requests=%d err=%v", data, requests, err)
	}

	prefixed := NewCallService(map[string]config.Service{"svc": testService(server.URL, "exec:"+helperCredentialCommand("bearer"))})
	prefixed.SetHTTPClientForTest(server.Client())
	if detail := prefixed.CallDetailed(context.Background(), &session.Session{}, args); detail.Err != nil || !detail.OperatorContext {
		t.Fatalf("prefixed=%+v", detail)
	}
}

func TestCallServiceExecCredentialFailureAndTimeoutAreAuthErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		command string
		ctx     func() (context.Context, context.CancelFunc)
	}{
		{"failure", helperCredentialCommand("fail"), func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }},
		{"timeout", helperCredentialCommand("sleep"), func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 30*time.Millisecond)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := test.ctx()
			defer cancel()
			tool := NewCallService(map[string]config.Service{"svc": testService("http://127.0.0.1:1", "exec:"+test.command)})
			_, err := tool.Call(ctx, &session.Session{}, map[string]any{"service": "svc", "method": "GET", "path": "check"})
			if err == nil || !strings.Contains(err.Error(), "auth_error") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCallServiceExecArgvParsingDoesNotUseShellSyntax(t *testing.T) {
	argv, err := splitServiceArgv(`"C:\Program Files\entra-token.exe" --scope "api scope" & whoami`)
	want := []string{`C:\Program Files\entra-token.exe`, "--scope", "api scope", "&", "whoami"}
	if err != nil || fmt.Sprint(argv) != fmt.Sprint(want) {
		t.Fatalf("argv=%q err=%v", argv, err)
	}
}

func TestCallServiceRequireConfirmationIsAQuietPolicyFlag2rh(t *testing.T) {
	var output strings.Builder
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	service := testService("http://127.0.0.1:1", "none")
	service.RequireConfirmation = true
	tool := NewCallService(map[string]config.Service{"flagged": service})
	if output.Len() != 0 {
		t.Fatalf("log=%q", output.String())
	}
	if !tool.PolicyApprovalRequired(config.ApprovalModeBoundaryOnly, map[string]any{"service": "flagged", "method": "GET", "path": "allowed"}) {
		t.Fatal("flagged service did not require approval")
	}
	plain := NewCallService(map[string]config.Service{"plain": testService("http://127.0.0.1:1", "none")})
	for _, method := range []string{"GET", "HEAD"} {
		if plain.PolicyApprovalRequired(config.ApprovalModeMutating, map[string]any{"service": "plain", "method": method, "path": "allowed"}) {
			t.Fatalf("%s required approval", method)
		}
	}
	if !plain.PolicyApprovalRequired(config.ApprovalModeMutating, map[string]any{"service": "plain", "method": "POST", "path": "allowed"}) {
		t.Fatal("POST did not require approval")
	}
}

func TestCallServiceCredentialHelper(t *testing.T) {
	separator := -1
	for index, value := range os.Args {
		if value == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	args := os.Args[separator+1:]
	switch args[0] {
	case "json":
		file, err := os.OpenFile(args[1], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(8)
		}
		_, _ = file.WriteString("x")
		_ = file.Close()
		fmt.Printf(`{"token":"exec-secret","expires_at":%q}`+"\n", args[2])
	case "bearer":
		fmt.Println("Bearer exec-secret")
	case "headers":
		fmt.Println(`{"Authorization":"Bearer header-secret","X-Broker":"ready"}`)
	case "fail":
		fmt.Fprintln(os.Stderr, "secret stderr must not escape")
		os.Exit(7)
	case "sleep":
		time.Sleep(5 * time.Second)
		fmt.Println("too-late-secret")
	}
	os.Exit(0)
}

func TestCallServiceConnectorChangeAndExecHeaders(t *testing.T) {
	change, present, err := ParseConnectorChange(map[string]any{"connector": map[string]any{
		"operation": "add", "entry": map[string]any{"name": "broker", "url": "https://broker.test/mcp", "kind": "mcp", "auth": "exec:helper headers"},
	}})
	if err != nil || !present || change.Name != "broker" || change.Service.Kind != "mcp" || !methodAllowed("POST", change.Service.AllowedMethods) {
		t.Fatalf("change=%+v present=%t err=%v", change, present, err)
	}
	if _, _, err := ParseConnectorChange(map[string]any{"connector": map[string]any{"operation": "add", "entry": map[string]any{"name": "bad", "kind": "mcp", "auth": "pasted-secret"}}}); err == nil || !strings.Contains(err.Error(), "auth") {
		t.Fatalf("bad auth error=%v", err)
	}

	// TLS for the same reason: 2nv (f) binds helper output to an https origin.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer header-secret" || r.Header.Get("X-Broker") != "ready" {
			t.Fatalf("headers=%v", r.Header)
		}
		_, _ = w.Write([]byte(`{"reflected":"header-secret"}`))
	}))
	defer server.Close()
	service := testService(server.URL, "exec:"+helperCredentialCommand("headers"))
	service.Kind, service.AllowedMethods = "http", []string{"POST"}
	headerTool := NewCallService(map[string]config.Service{"broker": service})
	headerTool.SetHTTPClientForTest(server.Client())
	detail := headerTool.CallDetailed(context.Background(), &session.Session{}, map[string]any{"service": "broker", "method": "POST", "path": "rpc", "body": map[string]any{"method": "tools/list"}})
	if detail.Err != nil || !detail.OperatorContext || strings.Contains(detail.Content, "header-secret") {
		t.Fatalf("detail=%+v", detail)
	}
}

func TestConnectorChangeAcceptsPublishedEntraSetupOnlyForNewMCPConnector(t *testing.T) {
	entry := map[string]any{
		"name": "work-api", "url": "https://api.example.test/mcp", "kind": "mcp", "auth": "entra:work-api",
		"entra": map[string]any{
			"tenant":    "11111111-1111-1111-1111-111111111111",
			"client_id": "22222222-2222-2222-2222-222222222222",
			"scopes":    []any{"https://api.example.test/mcp/access"},
		},
	}
	change, present, err := ParseConnectorChange(map[string]any{"connector": map[string]any{"operation": "add", "entry": entry}})
	if err != nil || !present || change.Entra == nil || change.Entra.ClientID != "22222222-2222-2222-2222-222222222222" || len(change.Entra.Scopes) != 1 {
		t.Fatalf("change=%+v present=%t err=%v", change, present, err)
	}

	entry["auth"] = "none"
	if _, _, err := ParseConnectorChange(map[string]any{"connector": map[string]any{"operation": "add", "entry": entry}}); err == nil || !strings.Contains(err.Error(), "requires auth entra:") {
		t.Fatalf("wrong-auth error=%v", err)
	}
}

func helperCredentialCommand(args ...string) string {
	values := []string{quoteServiceTestArg(os.Args[0]), "-test.run=^TestCallServiceCredentialHelper$", "--"}
	for _, arg := range args {
		values = append(values, quoteServiceTestArg(arg))
	}
	return strings.Join(values, " ")
}

func quoteServiceTestArg(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, "") + `"`
}

func testService(baseURL, auth string) config.Service {
	return config.Service{BaseURL: baseURL, Auth: auth, AllowedMethods: []string{"GET"}, TimeoutS: 2, MaxBodyKB: 1}
}

func callServiceResult(t *testing.T, tool *CallService, args map[string]any) map[string]any {
	t.Helper()
	value, err := tool.Call(context.Background(), &session.Session{}, args)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(value), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCallServiceToolsBlockByteDelta(t *testing.T) {
	service := NewCallService(map[string]config.Service{})
	registry := New(descriptionTestTool{name: "existing"}, service)
	before, err := json.Marshal(registry.Schemas(map[string]bool{"existing": true}))
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(registry.Schemas(map[string]bool{"existing": true, "call_service": true}))
	if err != nil {
		t.Fatal(err)
	}
	// Item 2nr moved this from 1396 to 1734; 2o4's declared response selector makes it
	// 1845, and 2pz's "the user" for "the operator" 1837. The operation form is present WITH NO CONNECTOR CONFIGURED.
	// A connector that has imported a document adds its operations on top, which is the
	// point of (d) and is paid for only by the installs that have one.
	// Item 2ze documents the proposal shape and both service addressing forms.
	const wantDelta = 2604
	if delta := len(after) - len(before); delta != wantDelta {
		t.Fatalf("call_service tools-block byte delta=%d, want %d", delta, wantDelta)
	}
	t.Logf("call_service tools-block byte delta: +%d (%d to %d)", wantDelta, len(before), len(after))
}

type serviceRoundTripFunc func(*http.Request) (*http.Response, error)

func (f serviceRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestMicrosoftServiceWithoutCredentialNeedsSignIn2zh(t *testing.T) {
	tool := NewCallService(nil)
	tool.SetVault(credential.NewVault(t.TempDir()))
	requests := 0
	tool.SetHTTPClientForTest(&http.Client{Transport: serviceRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, fmt.Errorf("fixture transport received a request")
	})})
	result := tool.CallDetailed(context.Background(), &session.Session{}, map[string]any{"service": "https://graph.microsoft.com/v1.0/auditLogs/signIns", "method": "GET"})
	if requests != 0 || result.Err == nil || !strings.Contains(result.Err.Error(), "credential needed · microsoft · https://graph.microsoft.com:443") {
		t.Fatalf("requests=%d, result=%+v", requests, result)
	}
}

func TestCredentialSetupWakesOnlyTheMatchingCall2zl(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows protects the fixture credential store")
	}
	vault := credential.NewVault(t.TempDir())
	services := map[string]config.Service{
		"first":  {BaseURL: "https://one.example.test", Auth: "stored:first-key", AllowedMethods: []string{"GET"}, MaxBodyKB: 64, TimeoutS: 5},
		"second": {BaseURL: "https://two.example.test", Auth: "stored:second-key", AllowedMethods: []string{"GET"}, MaxBodyKB: 64, TimeoutS: 5},
	}
	tool := NewCallService(services)
	tool.SetVault(vault)
	tool.SetHTTPClientForTest(&http.Client{Transport: serviceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer fixture-key-value" {
			return nil, fmt.Errorf("expected the protected fixture value")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/plain"}}, Body: io.NopCloser(strings.NewReader("fixture response"))}, nil
	})})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type pause struct {
		needed  *CredentialNeeded
		changed <-chan struct{}
	}
	paused := make(chan pause, 2)
	results := make(map[string]chan CallDetail)
	for name := range services {
		result := make(chan CallDetail, 1)
		results[name] = result
		go func(name string) {
			waiting := WithCredentialSetup(ctx, func(ctx context.Context, needed *CredentialNeeded, changed <-chan struct{}) error {
				paused <- pause{needed: needed, changed: changed}
				select {
				case <-changed:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			result <- tool.CallDetailed(waiting, &session.Session{}, map[string]any{"service": name, "path": "fixture", "method": "GET"})
		}(name)
	}
	waits := make(map[string]<-chan struct{})
	for range services {
		select {
		case waiting := <-paused:
			need := waiting.needed
			service, known := services[strings.TrimSuffix(need.Name, "-key")]
			if !known || need.Kind != "key" || need.Origin != service.BaseURL+":443" || waiting.changed == nil {
				t.Fatalf("unexpected pending key: %+v", need)
			}
			waits[need.Name] = waiting.changed
		case <-ctx.Done():
			t.Fatal("both calls did not reach setup")
		}
	}
	if err := vault.Put("first-key", "https://third.example.test", "", "fixture-key-value"); err != nil {
		t.Fatal(err)
	}
	vault.NotifyChanged("first-key")
	select {
	case <-waits["first-key"]:
		t.Fatal("the right name with the wrong address woke the call")
	default:
	}
	for _, name := range []string{"first", "second"} {
		if err := vault.Put(name+"-key", services[name].BaseURL, "", "fixture-key-value"); err != nil {
			t.Fatal(err)
		}
		vault.NotifyChanged(name + "-key")
		if name == "first" {
			select {
			case <-waits["second-key"]:
				t.Fatal("saving the first key woke the call waiting for the second")
			default:
			}
		}
		select {
		case got := <-results[name]:
			if got.Err != nil || !strings.Contains(got.Content, "fixture response") {
				t.Fatalf("%s call did not complete: %+v", name, got)
			}
		case <-ctx.Done():
			t.Fatalf("%s call did not resume", name)
		}
	}
}

type fixtureMicrosoftProvider struct {
	origins []string
	err     error
}

func (*fixtureMicrosoftProvider) Origin(string) (string, error) {
	return "https://graph.microsoft.com:443", nil
}
func (p *fixtureMicrosoftProvider) Token(ctx context.Context, name string) (string, error) {
	return p.TokenForOrigin(ctx, name, "https://graph.microsoft.com:443")
}
func (p *fixtureMicrosoftProvider) TokenForOrigin(_ context.Context, _ string, origin string) (string, error) {
	p.origins = append(p.origins, origin)
	if p.err != nil {
		return "", p.err
	}
	return "fixture-audience-token", nil
}

func TestMicrosoftSetupRecoversExpiredAndRevokedStandIn2zl(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows protects the fixture credential store")
	}
	for _, failure := range []string{"expired", "revoked"} {
		t.Run(failure, func(t *testing.T) {
			const origin = "https://graph.microsoft.com:443"
			vault := credential.NewVault(t.TempDir())
			if err := vault.PutEntra(credential.MicrosoftName, origin, "", "", nil); err != nil {
				t.Fatal(err)
			}
			provider := &fixtureMicrosoftProvider{err: fmt.Errorf("fixture sign-in %s", failure)}
			tool := NewCallService(nil)
			tool.SetVault(vault)
			tool.SetTokenProvider("entra", provider)
			requests, waits := 0, 0
			tool.SetHTTPClientForTest(&http.Client{Transport: serviceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				if request.Header.Get("Authorization") != "Bearer fixture-audience-token" {
					return nil, fmt.Errorf("a usable credential was not attached")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/plain"}}, Body: io.NopCloser(strings.NewReader("fixture response"))}, nil
			})})
			ctx := WithCredentialSetup(context.Background(), func(_ context.Context, need *CredentialNeeded, changed <-chan struct{}) error {
				waits++
				if requests != 0 || need.Kind != "microsoft" || need.Origin != origin || need.ServiceSignIn {
					t.Fatalf("requests=%d need=%+v", requests, need)
				}
				provider.err = nil
				vault.NotifyChanged(need.Name)
				select {
				case <-changed:
					return nil
				default:
					return fmt.Errorf("completed sign-in did not signal its call")
				}
			})
			result := tool.CallDetailed(ctx, &session.Session{}, map[string]any{"service": origin + "/fixture", "method": "GET"})
			if waits != 1 || requests != 1 || result.Err != nil || !strings.Contains(result.Content, "fixture response") {
				t.Fatalf("waits=%d requests=%d result=%+v", waits, requests, result)
			}
		})
	}
}

func TestMicrosoftSetupResumesOnlyAfterOperatorAction2zh(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows protects the fixture credential store")
	}
	vault := credential.NewVault(t.TempDir())
	tool := NewCallService(nil)
	tool.SetVault(vault)
	provider := &fixtureMicrosoftProvider{}
	tool.SetTokenProvider("entra", provider)
	requests := 0
	tool.SetHTTPClientForTest(&http.Client{Transport: serviceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.Header.Get("Authorization") != "Bearer fixture-audience-token" {
			t.Error("the fixture did not receive its access token")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/plain"}}, Body: io.NopCloser(strings.NewReader("fixture response"))}, nil
	})})
	resumed := 0
	ctx := WithCredentialSetup(context.Background(), func(_ context.Context, need *CredentialNeeded, changed <-chan struct{}) error {
		if requests != 0 || need.Kind != "microsoft" {
			t.Fatalf("requests=%d; need=%+v", requests, need)
		}
		if err := vault.PutEntra(need.Name, need.Origin, "", "", nil); err != nil {
			t.Fatal(err)
		}
		vault.NotifyChanged(need.Name)
		<-changed
		resumed++
		return nil
	})
	for _, address := range []string{"https://graph.microsoft.com/v1.0/auditLogs/signIns", "https://management.azure.com/subscriptions"} {
		result := tool.CallDetailed(ctx, &session.Session{}, map[string]any{"service": address, "method": "GET"})
		if result.Err != nil || !strings.Contains(result.Content, "fixture response") {
			t.Fatalf("request did not complete: %+v", result)
		}
	}
	if resumed != 1 || requests != 2 || strings.Join(provider.origins, " ") != "https://graph.microsoft.com:443 https://management.azure.com:443" {
		t.Fatalf("resumes=%d requests=%d origins=%v", resumed, requests, provider.origins)
	}
}

func TestMicrosoftScopeChallengeWaitsAndReplaysOnce2zh(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows protects the fixture credential store")
	}
	vault := credential.NewVault(t.TempDir())
	if err := vault.PutEntra("microsoft", "https://graph.microsoft.com", "", "", nil); err != nil {
		t.Fatal(err)
	}
	tool := NewCallService(nil)
	tool.SetVault(vault)
	tool.SetTokenProvider("entra", &fixtureMicrosoftProvider{})
	requests := 0
	tool.SetHTTPClientForTest(&http.Client{Transport: serviceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1.0/auditLogs/signIns" {
			t.Error("recovery changed the request")
		}
		requests++
		status, body := http.StatusOK, "fixture response"
		header := http.Header{"Content-Type": {"text/plain"}}
		if requests == 1 {
			status, body = http.StatusForbidden, "scope missing"
			header.Set("WWW-Authenticate", `Bearer error="insufficient_scope"`)
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	waits := 0
	ctx := WithCredentialSetup(context.Background(), func(_ context.Context, need *CredentialNeeded, changed <-chan struct{}) error {
		waits++
		if requests != 1 || need.Kind != "microsoft" || need.Origin != "https://graph.microsoft.com:443" || !need.ServiceSignIn {
			t.Fatalf("requests=%d need=%+v", requests, need)
		}
		vault.NotifyChanged(need.Name)
		<-changed
		return nil
	})
	result := tool.CallDetailed(ctx, &session.Session{}, map[string]any{"service": "https://graph.microsoft.com/v1.0/auditLogs/signIns", "method": "GET"})
	if waits != 1 || requests != 2 || result.Err != nil || !strings.Contains(result.Content, "fixture response") {
		t.Fatalf("waits=%d requests=%d result=%+v", waits, requests, result)
	}
}

func TestAutomaticKeyMatchesOnlyItsListedOrigins2zi(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows protects the fixture credential store")
	}
	vault := credential.NewVault(t.TempDir())
	origins := []string{"https://one.example.test", "https://two.example.test:8443"}
	if err := vault.PutKeyOrigins("fixture", origins, "", "fixture-key-value"); err != nil {
		t.Fatal(err)
	}
	if err := vault.Put("duplicate", "https://one.example.test:443", "", "other-fixture-key"); err == nil || !strings.Contains(err.Error(), `"fixture"`) {
		t.Fatalf("duplicate origin: %v", err)
	}
	tool := NewCallService(nil)
	tool.SetVault(vault)
	requests, approved := 0, 0
	tool.SetHTTPClientForTest(&http.Client{Transport: serviceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		want := ""
		if requests <= 2 {
			want = "Bearer fixture-key-value"
			if approved != requests {
				t.Error("credential was attached before approval")
			}
		}
		if request.Header.Get("Authorization") != want {
			t.Error("credential destination was not enforced")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/plain"}}, Body: io.NopCloser(strings.NewReader(want))}, nil
	})})
	ctx := WithCredentialApproval(context.Background(), func(context.Context, credential.Entry, string) error { approved++; return nil })
	for _, address := range append(origins, "https://three.example.test") {
		result := tool.CallDetailed(ctx, &session.Session{}, map[string]any{"service": address, "method": "GET"})
		if result.Err != nil || strings.Contains(result.Content, "fixture-key-value") {
			t.Fatalf("unexpected result: %+v", result)
		}
	}
	if requests != 3 || approved != 2 {
		t.Fatalf("requests=%d approvals=%d", requests, approved)
	}
}

func TestStoredCredentialBindingIsVisibleWithoutItsValue306(t *testing.T) {
	vault := credential.NewVault(t.TempDir())
	const secret = "fixture-secret-that-must-stay-private"
	if err := vault.Put("Fixture key", "https://fixture.example.test", "", secret); err != nil {
		t.Fatal(err)
	}
	tool := NewCallService(nil)
	t.Cleanup(tool.Close)
	tool.SetVault(vault)
	before := vault.SecretReads()
	schema, err := json.Marshal(tool.Schema())
	if err != nil {
		t.Fatal(err)
	}
	binding := string(schema)
	if !strings.Contains(binding, "Fixture key") || !strings.Contains(binding, "https://fixture.example.test") ||
		strings.Contains(binding, secret) || vault.SecretReads() != before {
		t.Fatalf("credential metadata absent or secret was read, schema bytes=%d", len(schema))
	}
	result := tool.CallDetailed(context.Background(), &session.Session{}, map[string]any{"service": "fixture", "list": true})
	if result.Err == nil || !strings.Contains(result.Err.Error(), "send method") {
		t.Fatalf("missing argument correction: %v", result.Err)
	}
}

func TestContentSearchReportsBoundAndProgress306(t *testing.T) {
	root := t.TempDir()
	journals := filepath.Join(root, "sessions")
	if err := os.Mkdir(journals, 0o755); err != nil {
		t.Fatal(err)
	}
	block := strings.Repeat("fixture line without the search term\n", 30_000)
	for i := 0; i < 100; i++ {
		path := filepath.Join(journals, fmt.Sprintf("%03d.jsonl", i))
		if err := os.WriteFile(path, []byte(block), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Defaults(root)
	grep := NewGrep(cfg.Tools.Grep, cfg.Tools.ListDir)
	progress := false
	ctx := WithOutputObserver(context.Background(), func(string) { progress = true })
	start := time.Now()
	output, err := grep.Call(ctx, &session.Session{Workspace: root}, map[string]any{"pattern": "no-fixture-matches", "path": "."})
	elapsed := time.Since(start)
	t.Logf("search over %d bytes took %s; progress=%t; %s", len(block)*100, elapsed, progress, output)
	if elapsed >= 30*time.Second || err != nil || !progress || !strings.Contains(output, "covered") || !strings.Contains(output, "skipped") {
		t.Fatalf("bounded search result=%q, error=%v, progress=%t, elapsed=%s", output, err, progress, elapsed)
	}
}

func TestCallServiceNetworkFailureLabels306(t *testing.T) {
	cases := []struct {
		err   error
		label string
		class string
	}{
		{&net.DNSError{Err: "fixture", Name: "fixture.example.test"}, "name not resolved", "not_found"},
		{syscall.ECONNREFUSED, "connection refused", "not_found"},
		{context.DeadlineExceeded, "connection timed out", "timeout"},
		{x509.HostnameError{}, "certificate not valid for this name", "denied"},
		{x509.UnknownAuthorityError{}, "certificate not trusted", "denied"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			wrapped := &url.Error{Op: "Get", URL: "https://fixture.example.test/alerts", Err: tc.err}
			label, class := serviceNetworkFailure(wrapped)
			if label != tc.label || class != tc.class {
				t.Fatalf("label=%q class=%q", label, class)
			}
		})
	}
}

func TestFileToolsKeepVaultRecordsOutOfResults306(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".agentb-credentials.json")
	if err := os.WriteFile(path, []byte("fixture metadata not for file tools"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults(root)
	item := &session.Session{Workspace: root, LastSeen: map[string]time.Time{}}
	ctx := context.Background()
	match := map[string]any{"pattern": "fixture metadata"}
	grep, err := NewGrep(cfg.Tools.Grep, cfg.Tools.ListDir).Call(ctx, item, match)
	if err != nil || strings.Contains(grep, "fixture metadata") {
		t.Fatalf("grep=%q err=%v", grep, err)
	}
	names, err := NewGlob(cfg.Tools.FindFiles).Call(ctx, item, map[string]any{"pattern": "*"})
	if err != nil || strings.Contains(names, filepath.Base(path)) {
		t.Fatalf("names=%q err=%v", names, err)
	}
	list, err := NewListDir(cfg.Tools.ListDir).Call(ctx, item, map[string]any{})
	if err != nil || strings.Contains(list, filepath.Base(path)) {
		t.Fatalf("list=%q err=%v", list, err)
	}
	_, err = NewReadFile(cfg.Tools.ReadFile).Call(ctx, item, map[string]any{"path": path})
	if err == nil || !strings.Contains(err.Error(), "credential store rule") {
		t.Fatalf("direct read error=%v", err)
	}
}
