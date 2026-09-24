package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
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
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["name"] != "vesper" {
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
		"query": map[string]any{"dry_run": true}, "body": map[string]any{"name": "vesper"},
		"headers": map[string]any{"If-Match": "v1"},
	})
	if detail.Err != nil {
		t.Fatal(detail.Err)
	}
	registry := New(tool)
	registryItem := &session.Session{ToolsEnabled: map[string]bool{"call_service": true}}
	registered := registry.CallDetailed(context.Background(), registryItem, "call_service", map[string]any{
		"service": "broker", "method": "POST", "path": "jobs/create",
		"query": map[string]any{"dry_run": true}, "body": map[string]any{"name": "vesper"},
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer exec-secret" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	tool := NewCallService(map[string]config.Service{"svc": testService(server.URL, auth)})
	clock := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	tool.now = func() time.Time { return clock }
	args := map[string]any{"service": "svc", "method": "GET", "path": "check"}
	for _, advance := range []time.Duration{0, 8 * time.Minute, time.Minute} {
		clock = clock.Add(advance)
		detail := tool.CallDetailed(context.Background(), &session.Session{}, args)
		if detail.Err != nil || !detail.OperatorContext {
			t.Fatalf("detail=%+v", detail)
		}
	}
	data, err := os.ReadFile(counter)
	if err != nil || strings.Count(string(data), "x") != 2 || requests != 3 {
		t.Fatalf("counter=%q requests=%d err=%v", data, requests, err)
	}

	prefixed := NewCallService(map[string]config.Service{"svc": testService(server.URL, "exec:"+helperCredentialCommand("bearer"))})
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

func TestCallServiceRequireConfirmationIsLoggedButNotEnforced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	var output strings.Builder
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	service := testService(server.URL, "none")
	service.RequireConfirmation = true
	tool := NewCallService(map[string]config.Service{"flagged": service})
	if !strings.Contains(output.String(), `service="flagged" require_confirmation=true`) || !strings.Contains(output.String(), "not enforced") {
		t.Fatalf("log=%q", output.String())
	}
	if _, err := tool.Call(context.Background(), &session.Session{}, map[string]any{"service": "flagged", "method": "GET", "path": "allowed"}); err != nil {
		t.Fatalf("reserved flag was enforced: %v", err)
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
	case "fail":
		fmt.Fprintln(os.Stderr, "secret stderr must not escape")
		os.Exit(7)
	case "sleep":
		time.Sleep(5 * time.Second)
		fmt.Println("too-late-secret")
	}
	os.Exit(0)
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
	const wantDelta = 1134
	if delta := len(after) - len(before); delta != wantDelta {
		t.Fatalf("call_service tools-block byte delta=%d, want %d", delta, wantDelta)
	}
	t.Logf("call_service tools-block byte delta: +%d (%d to %d)", wantDelta, len(before), len(after))
}
