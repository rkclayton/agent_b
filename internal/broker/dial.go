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

// the live transport. One outbound WebSocket to the broker's
// /v1/connect, and nothing listens on this machine for it.
//
// golang.org/x/net/websocket is already a dependency of this repository; the one new
// dependency the item allows went to the Noise implementation, where being byte-exact
// with another implementation matters.

// Dial builds a Dialer for a broker address. An empty address is not an error here: it
// is the caller's business to not dial at all, which is what means by
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
		return newSocketTransport(connection), nil
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

// SendFrameRate is the most frames a connection writes in a second: one step below the
// broker's limit of 240 frames per connection per second, past which it stops reading the
// connection until the second turns (item 2wo).
const SendFrameRate = 200

// The frames waiting for the writer are bounded by count and, for data, by bytes, the
// same bounds the agent's retained deliveries have; past them Send waits.
const maxQueuedFrames, maxQueuedBytes = maxPendingDeliveries, maxPendingBytes

// socketTransport has ONE WRITER, paced to SendFrameRate, so whoever reads the link never
// waits on a write. ACK and PONG go ahead of everything else, so an ACK never waits behind
// data; every other frame keeps its order.
type socketTransport struct {
	connection    *websocket.Conn
	control, data chan []byte
	done, drained chan struct{}
	once          sync.Once
	mu            sync.Mutex
	err           error
	queuedBytes   int
}

func newSocketTransport(connection *websocket.Conn) *socketTransport {
	t := &socketTransport{connection: connection, control: make(chan []byte, maxQueuedFrames), data: make(chan []byte, maxQueuedFrames), done: make(chan struct{}), drained: make(chan struct{}, 1)}
	go t.write()
	return t
}

func (t *socketTransport) Send(frame []byte) error {
	queue := t.data
	if len(frame) > 1 && (frame[1] == FrameAck || frame[1] == FramePong) {
		queue = t.control
	}
	for queue == t.data {
		t.mu.Lock()
		if t.queuedBytes == 0 || t.queuedBytes+len(frame) <= maxQueuedBytes {
			t.queuedBytes += len(frame)
			t.mu.Unlock()
			break
		}
		t.mu.Unlock()
		select {
		case <-t.drained:
		case <-t.done:
			return t.failure()
		}
	}
	select {
	case queue <- frame:
		return nil
	case <-t.done:
		return t.failure()
	}
}

func (t *socketTransport) failure() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

func (t *socketTransport) write() {
	pace := time.NewTicker(time.Second / SendFrameRate)
	defer pace.Stop()
	for {
		var frame []byte
		data := false
		select {
		case frame = <-t.control:
		default:
			select {
			case frame = <-t.control:
			case frame = <-t.data:
				data = true
			case <-t.done:
				return
			}
		}
		if err := websocket.Message.Send(t.connection, frame); err != nil {
			t.end(err)
			return
		}
		if data {
			t.mu.Lock()
			t.queuedBytes -= len(frame)
			t.mu.Unlock()
			select {
			case t.drained <- struct{}{}:
			default:
			}
		}
		select {
		case <-pace.C:
		case <-t.done:
			return
		}
	}
}

func (t *socketTransport) end(err error) {
	t.once.Do(func() {
		t.mu.Lock()
		t.err = err
		t.mu.Unlock()
		close(t.done)
		_ = t.connection.Close()
	})
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
	t.end(errors.New("broker: connection closed"))
	return nil
}
