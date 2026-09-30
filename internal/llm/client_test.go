package llm

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
)

func TestConnectionTLSUsesSystemTrustAndNamesUnknownAuthority(t *testing.T) {
	if os.Getenv("AGENTB_TLS_MACHINE_STORE_LIVE") != "1" {
		t.Skip("set AGENTB_TLS_MACHINE_STORE_LIVE=1 for the Windows machine-store acceptance")
	}
	trusted, err := (&http.Client{Timeout: 15 * time.Second, Transport: sharedTransport}).Head("https://broker.agentb.app/")
	if err != nil {
		t.Fatalf("machine-store trusted endpoint failed: %v", err)
	}
	trusted.Body.Close()

	untrusted := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	untrusted.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	untrusted.StartTLS()
	defer untrusted.Close()
	_, err = (&http.Client{Timeout: 5 * time.Second, Transport: sharedTransport}).Get(untrusted.URL)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "certificate") || !strings.Contains(strings.ToLower(err.Error()), "unknown authority") {
		t.Fatalf("untrusted chain error=%v", err)
	}
}

func TestChatStreamRejectsMalformedChunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {not-json}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	client := New(&config.Connection{BaseURL: server.URL, RequestTimeoutS: 5})
	if _, err := client.ChatStream(context.Background(), Request{}, func(Delta) {}); err == nil || !strings.Contains(err.Error(), "decode chat stream chunk") {
		t.Fatalf("malformed stream error=%v", err)
	}
}

func TestTransportErrorsDistinguishDialFromConnectedDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	dialClient := New(&config.Connection{BaseURL: "http://" + address, RequestTimeoutS: 1})
	if _, _, err := dialClient.DoJSON(context.Background(), http.MethodGet, "/props", nil); TransportKindOf(err) != TransportDial {
		t.Fatalf("dial error kind=%q err=%v", TransportKindOf(err), err)
	}

	connected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer connected.Close()
	connectedClient := New(&config.Connection{BaseURL: connected.URL, RequestTimeoutS: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, _, err := connectedClient.DoJSON(ctx, http.MethodGet, "/props", nil); TransportKindOf(err) != TransportConnected {
		t.Fatalf("connected error kind=%q err=%v", TransportKindOf(err), err)
	}
}

func TestChatStreamRejectsEmptyStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()
	client := New(&config.Connection{BaseURL: server.URL, RequestTimeoutS: 5})
	if _, err := client.ChatStream(context.Background(), Request{}, func(Delta) {}); err == nil || !strings.Contains(err.Error(), "no decodable chunks") {
		t.Fatalf("empty stream error=%v", err)
	}
}

func TestReadBoundedRejectsTruncation(t *testing.T) {
	if raw, err := readBounded(strings.NewReader("123456"), 5); err == nil || raw != nil {
		t.Fatalf("oversize body raw=%q err=%v", raw, err)
	}
	if raw, err := readBounded(strings.NewReader("12345"), 5); err != nil || string(raw) != "12345" {
		t.Fatalf("at-limit body raw=%q err=%v", raw, err)
	}
}
