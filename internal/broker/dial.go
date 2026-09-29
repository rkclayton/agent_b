package broker

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

// Item 2kq (a): the live transport. One outbound WebSocket to the broker's
// /v1/connect, and nothing listens on this machine for it.
//
// golang.org/x/net/websocket is already a dependency of this repository; the one new
// dependency the item allows went to the Noise implementation, where being byte-exact
// with another implementation matters.

// Dial builds a Dialer for a broker address. An empty address is not an error here: it
// is the caller's business to not dial at all, which is what item 2kq (f) means by
// empty meaning off.
func Dial(address string) Dialer {
	return func(ctx context.Context) (Transport, error) {
		endpoint, err := connectURL(address)
		if err != nil {
			return nil, err
		}
		config, err := websocket.NewConfig(endpoint, "https://"+hostOf(endpoint))
		if err != nil {
			return nil, fmt.Errorf("broker dial: %w", err)
		}
		config.Header = http.Header{}
		config.Header.Set("User-Agent", "Agent_b")
		// The broker presents an ordinary certificate for its own name; nothing here
		// weakens verification.
		config.TlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: strings.Split(hostOf(endpoint), ":")[0]}
		connection, err := websocket.DialConfig(config)
		if err != nil {
			return nil, fmt.Errorf("broker dial: %w", err)
		}
		return &socketTransport{connection: connection}, nil
	}
}

// connectURL turns whatever the operator typed into the endpoint the document names.
// A bare host becomes wss://host/v1/connect; an address that already names a path is
// left as it is, because he may be pointing at something else on purpose.
func connectURL(address string) (string, error) {
	trimmed := strings.TrimSpace(address)
	if trimmed == "" {
		return "", errors.New("broker: no address")
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "wss://" + trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("broker: %q is not an address: %w", address, err)
	}
	switch parsed.Scheme {
	case "wss", "ws":
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	default:
		return "", fmt.Errorf("broker: %q is not a WebSocket address", address)
	}
	if strings.Trim(parsed.Path, "/") == "" {
		parsed.Path = "/v1/connect"
	}
	return parsed.String(), nil
}

func hostOf(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	return parsed.Host
}

type socketTransport struct {
	connection *websocket.Conn
	mu         sync.Mutex
}

func (t *socketTransport) Send(frame []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return websocket.Message.Send(t.connection, frame)
}

func (t *socketTransport) Receive(ctx context.Context) ([]byte, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = t.connection.SetReadDeadline(deadline)
	} else {
		_ = t.connection.SetReadDeadline(time.Now().Add(10 * time.Minute))
	}
	var frame []byte
	if err := websocket.Message.Receive(t.connection, &frame); err != nil {
		return nil, err
	}
	return frame, nil
}

func (t *socketTransport) Close(int, string) error {
	return t.connection.Close()
}
