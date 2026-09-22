package probe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"harness/internal/config"
)

func TestDiscoveryCandidatesStayOnTypedHost(t *testing.T) {
	candidates, host, err := discoveryCandidates("example.test")
	if err != nil || host != "example.test" {
		t.Fatalf("host=%q err=%v", host, err)
	}
	for _, candidate := range candidates {
		parsed, parseErr := url.Parse(candidate)
		if parseErr != nil || parsed.Hostname() != host {
			t.Fatalf("candidate escaped typed host: %q (%v)", candidate, parseErr)
		}
	}
	joined := strings.Join(candidates, "\n")
	for _, want := range []string{"https://example.test:443", "http://example.test:8000/v1", "http://example.test:5000"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %v", want, candidates)
		}
	}
}

func TestDiscoverEndpointUsesExactURLAndListsModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"second"},{"id":"first"}]}`)
	}))
	defer server.Close()
	profile := config.Defaults(t.TempDir()).Servers[0]
	profile.BaseURL = server.URL
	result, err := DiscoverEndpoint(context.Background(), &profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.BaseURL != server.URL || fmt.Sprint(result.Models) != "[first second]" {
		t.Fatalf("result=%+v", result)
	}
	if len(result.Attempts) != 1 || !result.Attempts[0].Allowed {
		t.Fatalf("attempts=%+v", result.Attempts)
	}
}

func TestDiscoverEndpointRefusesMalformedSchemeBeforeRequests(t *testing.T) {
	profile := config.Defaults(t.TempDir()).Servers[0]
	profile.BaseURL = "https:/host"
	result, err := DiscoverEndpoint(context.Background(), &profile)
	if err == nil || !strings.Contains(err.Error(), "two slashes") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(result.Attempts) != 1 || result.Attempts[0].Allowed {
		t.Fatalf("attempts=%+v", result.Attempts)
	}
}
