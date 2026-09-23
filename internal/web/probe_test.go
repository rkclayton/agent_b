package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/probe"
)

func TestReadyConnectionTestDoesNotRewriteConfig(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"C:\\models\\Friendly.gguf"}]}`)
	}))
	defer model.Close()
	root := t.TempDir()
	path := filepath.Join(root, "harness.json")
	cfg := config.Defaults(root)
	cfg.Servers[0].BaseURL = model.URL
	cfg.Servers[0].Model = "Friendly"
	cfg.Servers[0].Capabilities.ProbedAt = "2026-09-23T10:00:00Z"
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	server := New(&cfg, path, root, RuntimeRoots{Application: root, Data: root, Workspace: root}, events.NewBus())
	request := httptest.NewRequest(http.MethodPost, "/api/servers/local/probe", strings.NewReader(`{"base_url":"`+model.URL+`","model":"Friendly"}`))
	response := httptest.NewRecorder()
	server.server(response, request)
	after, _ := os.ReadFile(path)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"ready"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("ready Test changed the config file")
	}
}

func TestDiscoveryProposesBaseURLWithoutSaving(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"only"}]}`)
	}))
	defer model.Close()
	root := t.TempDir()
	path := filepath.Join(root, "harness.json")
	cfg := config.Defaults(root)
	cfg.Servers[0].BaseURL = model.URL + "/wrong"
	cfg.Servers[0].Model = "only"
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	server := New(&cfg, path, root, RuntimeRoots{Application: root, Data: root, Workspace: root}, events.NewBus())
	request := httptest.NewRequest(http.MethodPost, "/api/servers/local/probe", nil)
	response := httptest.NewRecorder()
	server.server(response, request)
	after, _ := os.ReadFile(path)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"changes_required"`) || !strings.Contains(response.Body.String(), "changed base_url from") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("discovery saved its proposal")
	}
}

func TestModelListedRecognizesLlamaIdentity(t *testing.T) {
	path := `C:\Users\Randy\models\Friendly.gguf`
	for _, configured := range []string{"Friendly.gguf", "Friendly", path} {
		if !modelListed(configured, []string{path}) {
			t.Fatalf("%q did not match %q", configured, path)
		}
	}
}

func TestFailedProbePreservesPreviousTimestamp(t *testing.T) {
	profile := &config.Profile{Capabilities: config.Capabilities{ProbedAt: "2026-09-04T12:00:00Z", Server: "llama.cpp"}}
	caps, findings := failedProbeCapabilities(profile, fmt.Errorf("connection refused"))
	if caps.ProbedAt != profile.Capabilities.ProbedAt || caps.Server != "llama.cpp" {
		t.Fatalf("failed probe capabilities=%+v", caps)
	}
	if len(findings) != 1 || findings[0] != "probe failed: connection refused" {
		t.Fatalf("findings=%v", findings)
	}
}

func TestConnectionTestReturnsDiscoveryListAndLogsEveryRequest(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"one"},{"id":"two"}]}`)
	}))
	defer model.Close()

	root := t.TempDir()
	cfg := config.Defaults(root)
	cfg.Servers[0].BaseURL, cfg.Servers[0].Model = model.URL, "model"
	configPath := filepath.Join(root, "harness.json")
	if err := cfg.Save(configPath); err != nil {
		t.Fatal(err)
	}
	configBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	stream, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	server := New(&cfg, configPath, root, RuntimeRoots{Application: root, Data: root, Workspace: root}, bus)
	request := httptest.NewRequest(http.MethodPost, "/api/servers/local/probe", nil)
	response := httptest.NewRecorder()
	server.server(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	var body struct {
		Status  string   `json:"status"`
		BaseURL string   `json:"base_url"`
		Message string   `json:"message"`
		Error   string   `json:"error"`
		Models  []string `json:"models"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "model_required" || body.BaseURL != model.URL || body.Message != "found "+model.URL || fmt.Sprint(body.Models) != "[one two]" || !strings.Contains(body.Error, `Model "model" is not served`) {
		t.Fatalf("body=%+v", body)
	}
	configAfter, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(configBefore, configAfter) {
		t.Fatalf("connection Test silently changed config: err=%v", err)
	}
	deadline := time.After(time.Second)
	for {
		select {
		case event := <-stream:
			if event.Type != events.ProbeRequest {
				continue
			}
			data, ok := event.Data.(map[string]any)
			if !ok || data["guard"] != "operator_typed_host_only" || data["allowed"] != true || data["base_url"] != model.URL {
				t.Fatalf("probe.request=%#v", event.Data)
			}
			return
		case <-deadline:
			t.Fatal("probe.request was not published")
		}
	}
}

func TestProbeNonJSONFailuresUseFriendlyRowsAndDiagnosticFindings(t *testing.T) {
	tests := []struct {
		name, want string
		handler    http.HandlerFunc
	}{
		{name: "html root", want: "Connection returned a web page, not model API JSON. Add the API path to base_url.", handler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<!doctype html><title>Server home</title>")
		}},
		{name: "unauthorized", want: "The server returned HTTP 401: <html>Unauthorized</html>.", handler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, "<html>Unauthorized</html>")
		}},
		{name: "login redirect", want: "Connection was redirected to a sign-in page. Use the model API URL and configure its credential.", handler: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/login" {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html>Please sign in</html>")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			profile := config.Defaults(t.TempDir()).Servers[0]
			profile.BaseURL, profile.Model = server.URL, "fake"
			_, _, err := probe.Probe(context.Background(), &profile)
			if err == nil {
				t.Fatal("probe unexpectedly passed")
			}
			_, findings := failedProbeCapabilities(&profile, err)
			if len(findings) != 2 || findings[0] != "probe failed: "+test.want {
				t.Fatalf("findings=%v", findings)
			}
			if !strings.Contains(findings[1], "status ") || !strings.Contains(findings[1], "content_type") || !strings.Contains(findings[1], "prefix") {
				t.Fatalf("diagnostic metadata missing: %v", findings)
			}
			if test.name != "unauthorized" && !strings.Contains(findings[1], "invalid character") {
				t.Fatalf("decoder detail missing: %v", findings)
			}
		})
	}
}
