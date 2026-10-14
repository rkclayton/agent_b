package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type HTTP struct {
	URL     string
	Client  *http.Client
	Headers http.Header
	Session string
	Limit   int64
	ready   bool
}

func (h *HTTP) send(ctx context.Context, payload []byte, notification bool) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header = h.Headers.Clone()
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if h.Session != "" {
		request.Header.Set("Mcp-Session-Id", h.Session)
	}
	if h.ready {
		request.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	}
	response, err := h.Client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if session := response.Header.Get("Mcp-Session-Id"); session != "" {
		h.Session = session
	}
	h.ready = true
	if notification && response.StatusCode == http.StatusAccepted {
		return nil, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	limit := h.Limit
	if limit <= 0 || limit > 4<<20 {
		limit = 4 << 20
	}
	if strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		scanner := bufio.NewScanner(io.LimitReader(response.Body, limit+1))
		scanner.Buffer(make([]byte, 4096), int(limit)+1)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data:") {
				data := []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
				if int64(len(data)) > limit {
					return nil, fmt.Errorf("MCP response exceeds max_body_kb")
				}
				var message map[string]json.RawMessage
				if json.Unmarshal(data, &message) != nil {
					return nil, fmt.Errorf("malformed MCP event stream")
				}
				if _, response := message["id"]; response {
					return data, nil
				}
			}
		}
		return nil, errorsOrMalformed(scanner.Err())
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err == nil && int64(len(data)) > limit {
		return nil, fmt.Errorf("MCP response exceeds max_body_kb")
	}
	return data, err
}
func errorsOrMalformed(err error) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("malformed MCP event stream")
}
func (h *HTTP) Exchange(ctx context.Context, p []byte) ([]byte, error) { return h.send(ctx, p, false) }
func (h *HTTP) Notify(ctx context.Context, p []byte) error {
	_, err := h.send(ctx, p, true)
	return err
}
func (h *HTTP) Close() error { return nil }
