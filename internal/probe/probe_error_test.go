package probe

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"harness/internal/config"
)

func TestProbeFailureNamesMalformedBaseURL(t *testing.T) {
	connection := config.Defaults(t.TempDir()).Connections[0]
	connection.BaseURL = "https:/host"
	_, _, err := Probe(context.Background(), &connection)
	if err == nil || !strings.Contains(err.Error(), `base_url "https:/host" is malformed`) || !strings.Contains(err.Error(), "two slashes") {
		t.Fatalf("error=%v", err)
	}
}

func TestConnectionFailureNamesDNSName(t *testing.T) {
	err := connectionProbeErrorFor("https://nosuch.invalid", fmt.Errorf("props unavailable"), &net.DNSError{Name: "nosuch.invalid", Err: "no such host"})
	if got := err.Error(); got != `The name "nosuch.invalid" does not resolve.` {
		t.Fatalf("error=%q", got)
	}
}

func TestProbeFailureNamesCertificateReason(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	connection := config.Defaults(t.TempDir()).Connections[0]
	connection.BaseURL = server.URL
	_, _, err := Probe(context.Background(), &connection)
	if err == nil || !strings.Contains(err.Error(), "TLS failed:") || !strings.Contains(strings.ToLower(err.Error()), "certificate") {
		t.Fatalf("error=%v", err)
	}
}

func TestProbeFailureNamesHTTPStatusAndFirstLine(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, "endpoint says no\nsecond line")
			}))
			defer server.Close()
			connection := config.Defaults(t.TempDir()).Connections[0]
			connection.BaseURL = server.URL
			_, _, err := Probe(context.Background(), &connection)
			want := fmt.Sprintf("HTTP %d: endpoint says no", status)
			if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "second line") {
				t.Fatalf("error=%v, want %q only", err, want)
			}
		})
	}
}

func TestProbeFailureNamesUnservedModelAndListedModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"zeta"},{"id":"alpha"},{"id":"beta"},{"id":"gamma"},{"id":"delta"},{"id":"epsilon"}]}`)
	}))
	defer server.Close()
	connection := config.Defaults(t.TempDir()).Connections[0]
	connection.BaseURL, connection.Model = server.URL, "model"
	_, _, err := Probe(context.Background(), &connection)
	if err == nil || !strings.Contains(err.Error(), `Model "model" is not served; this server lists:`) {
		t.Fatalf("error=%v", err)
	}
	listed := strings.TrimPrefix(err.Error(), `Model "model" is not served; this server lists: `)
	if strings.Count(listed, ",") != 4 {
		t.Fatalf("expected at most five listed models, got %q", listed)
	}
}
