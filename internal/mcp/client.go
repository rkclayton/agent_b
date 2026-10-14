package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

const ProtocolVersion = "2025-06-18"

type Endpoint interface {
	Exchange(context.Context, []byte) ([]byte, error)
	Notify(context.Context, []byte) error
	Close() error
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type Client struct {
	mu       sync.Mutex
	endpoint Endpoint
	next     int64
	ready    bool
}

func New(endpoint Endpoint) *Client { return &Client{endpoint: endpoint} }

func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.next++
	request := map[string]any{"jsonrpc": "2.0", "id": c.next, "method": method}
	if params != nil {
		request["params"] = params
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	for {
		raw, err := c.endpoint.Exchange(ctx, payload)
		if err != nil {
			return nil, err
		}
		payload = nil
		var response struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      *int64          `json:"id"`
			Method  string          `json:"method"`
			Result  json.RawMessage `json:"result"`
			Error   *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &response); err != nil || response.JSONRPC != "2.0" {
			return nil, fmt.Errorf("malformed MCP response")
		}
		// Notifications may arrive between a request and its response. The client
		// advertises no optional capabilities, so there are no server requests to answer.
		if response.ID == nil && response.Method != "" {
			continue
		}
		if response.ID == nil || *response.ID != c.next {
			return nil, fmt.Errorf("malformed MCP response")
		}
		if response.Error != nil {
			return nil, fmt.Errorf("MCP error %d: %s", response.Error.Code, response.Error.Message)
		}
		if len(response.Result) == 0 {
			return nil, fmt.Errorf("malformed MCP response")
		}
		return response.Result, nil
	}
}

func (c *Client) initialize(ctx context.Context) error {
	if c.ready {
		return nil
	}
	result, err := c.request(ctx, "initialize", map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "agent_b", "version": "1"}})
	if err != nil {
		return err
	}
	var initialized struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
		ServerInfo      map[string]any `json:"serverInfo"`
	}
	if json.Unmarshal(result, &initialized) != nil || initialized.ProtocolVersion != ProtocolVersion || initialized.ServerInfo == nil {
		return errors.New("malformed MCP initialize response")
	}
	notification, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	if err := c.endpoint.Notify(ctx, notification); err != nil {
		return err
	}
	c.ready = true
	return nil
}

func (c *Client) List(ctx context.Context) ([]Tool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.initialize(ctx); err != nil {
		return nil, err
	}
	tools := []Tool{}
	cursor := ""
	for page := 0; page < 100; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		result, err := c.request(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var listed struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if json.Unmarshal(result, &listed) != nil || listed.Tools == nil {
			return nil, errors.New("malformed MCP tools/list response")
		}
		if len(tools)+len(listed.Tools) > 1024 {
			return nil, errors.New("MCP tools/list exceeds 1024 tools")
		}
		tools = append(tools, listed.Tools...)
		cursor = listed.NextCursor
		if cursor == "" {
			return tools, nil
		}
	}
	return nil, errors.New("MCP tools/list exceeds 100 pages")
}

func (c *Client) Call(ctx context.Context, name string, args map[string]any) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.initialize(ctx); err != nil {
		return "", err
	}
	result, err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	var called struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if json.Unmarshal(result, &called) != nil || called.Content == nil {
		return "", errors.New("malformed MCP tools/call response")
	}
	parts := []string{}
	for _, part := range called.Content {
		if part.Type == "text" {
			parts = append(parts, part.Text)
		}
	}
	text := strings.Join(parts, "\n")
	if called.IsError {
		return "", errors.New(text)
	}
	return text, nil
}

func (c *Client) Close() error { return c.endpoint.Close() }
