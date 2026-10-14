package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/session"
)

func TestMCPStdioHelper(t *testing.T) {
	if os.Getenv("AGENTB_MCP_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request map[string]any
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		id, present := request["id"]
		if !present {
			continue
		}
		fmt.Fprintln(os.Stderr, "fixture diagnostic")
		var result any
		switch request["method"] {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "stdio", "version": "1"}}
		case "tools/list":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/tools/list_changed"})
			result = map[string]any{"tools": []any{map[string]any{"name": "echo", "description": "Echo stdio", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "stdio hello"}}}
		default:
			continue
		}
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	os.Exit(0)
}

func TestMCPRealServer2rm(t *testing.T) {
	command := strings.TrimSpace(os.Getenv("AGENTB_REAL_MCP_COMMAND"))
	if command == "" {
		t.Skip("set AGENTB_REAL_MCP_COMMAND for the one-shot no-account server check")
	}
	service := config.Service{Kind: "mcp", BaseURL: "command:" + command, Auth: "none", AllowedMethods: []string{"POST"}, TimeoutS: 30, MaxBodyKB: 256}
	tool := NewCallService(map[string]config.Service{"real": service})
	tool.SetShell(NewShell(processTestShell()))
	defer tool.Close()
	item := &session.Session{ID: "real", Workspace: t.TempDir()}
	listed := tool.CallDetailed(context.Background(), item, map[string]any{"service": "real", "list": true})
	if listed.Err != nil || !strings.Contains(listed.Content, "echo") {
		t.Fatalf("list=%+v", listed)
	}
	called := tool.CallDetailed(context.Background(), item, map[string]any{"service": "real", "tool": "echo", "arguments": map[string]any{"message": "fixture-real-2rm"}})
	if called.Err != nil || !strings.Contains(called.Content, "fixture-real-2rm") || !called.Untrusted {
		t.Fatalf("call=%+v", called)
	}
	t.Logf("list=%q call=%q untrusted=%t", listed.Content, called.Content, called.Untrusted)
}

func TestMCPHTTPListsAndCallsTools2rm(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		id := request["id"]
		if request["method"] != "initialize" && r.Header.Get("MCP-Protocol-Version") != "2025-06-18" {
			t.Errorf("protocol header=%q", r.Header.Get("MCP-Protocol-Version"))
		}
		switch request["method"] {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "fixture-session")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fixture", "version": "1"}}})
		case "notifications/initialized":
			if r.Header.Get("Mcp-Session-Id") != "fixture-session" {
				t.Errorf("session header=%q", r.Header.Get("Mcp-Session-Id"))
			}
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			params, _ := request["params"].(map[string]any)
			if params["cursor"] == "more" {
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"tools": []any{map[string]any{"name": "second", "description": "Second page", "inputSchema": map[string]any{"type": "object"}}}}})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"tools": []any{map[string]any{"name": "echo", "description": "Echo one value", "inputSchema": map[string]any{"type": "object"}}}, "nextCursor": "more"}})
			}
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "hello"}}}})
		default:
			t.Fatalf("method=%v", request["method"])
		}
	}))
	defer server.Close()
	service := config.Service{Kind: "mcp", BaseURL: server.URL, Auth: "none", AllowedMethods: []string{"POST"}, TimeoutS: 5, MaxBodyKB: 64}
	tool := NewCallService(map[string]config.Service{"fixture": service})
	item := &session.Session{ID: "chat", Workspace: t.TempDir()}
	listed := tool.CallDetailed(context.Background(), item, map[string]any{"service": "fixture", "list": true})
	if listed.Err != nil || !listed.Untrusted || !strings.Contains(listed.Content, "echo — Echo one value") || !strings.Contains(listed.Content, "second — Second page") {
		t.Fatalf("list=%+v", listed)
	}
	called := tool.CallDetailed(context.Background(), item, map[string]any{"service": "fixture", "tool": "echo", "arguments": map[string]any{"value": "hello"}})
	if called.Err != nil || !called.Untrusted || called.Content != "hello" {
		t.Fatalf("call=%+v", called)
	}
	if got := fmt.Sprint(tool.Schema()); !strings.Contains(got, "list") || !strings.Contains(got, "arguments") {
		t.Fatalf("schema=%s", got)
	}
}

func TestMCPCompatibilityAndBounds2rm(t *testing.T) {
	if _, _, err := ParseConnectorChange(map[string]any{"connector": map[string]any{"operation": "add", "entry": map[string]any{"name": "local", "kind": "mcp", "command": "server --stdio", "auth": "stored:token"}}}); err == nil || !strings.Contains(err.Error(), "must use none") {
		t.Fatalf("command credential error=%v", err)
	}
	tool := NewCallService(map[string]config.Service{"old": {Kind: "mcp", BaseURL: "https://example.test/mcp", Auth: "none", TimeoutS: 5, MaxBodyKB: 64}})
	detail := tool.CallDetailed(context.Background(), nil, map[string]any{"service": "old", "method": "POST"})
	if detail.Err == nil || !strings.Contains(detail.Err.Error(), "change its kind to http") {
		t.Fatalf("legacy=%+v", detail)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.Repeat("x", 1025)))
	}))
	defer server.Close()
	bounded := NewCallService(map[string]config.Service{"bounded": {Kind: "mcp", BaseURL: server.URL, Auth: "none", TimeoutS: 5, MaxBodyKB: 1}})
	detail = bounded.CallDetailed(context.Background(), nil, map[string]any{"service": "bounded", "list": true})
	if detail.Err == nil || !strings.Contains(detail.Err.Error(), "max_body_kb") {
		t.Fatalf("bounded=%+v", detail)
	}
}

func TestMCPStdioListsAndCallsTools2rm(t *testing.T) {
	command := processTestCommand(fmt.Sprintf(`$env:AGENTB_MCP_HELPER='1'; & '%s' '-test.run=TestMCPStdioHelper'`, strings.ReplaceAll(os.Args[0], "'", "''")), fmt.Sprintf(`AGENTB_MCP_HELPER=1 %s -test.run=TestMCPStdioHelper`, strconv.Quote(os.Args[0])))
	service := config.Service{Kind: "mcp", BaseURL: "command:" + command, Auth: "none", AllowedMethods: []string{"POST"}, TimeoutS: 5, MaxBodyKB: 64}
	tool := NewCallService(map[string]config.Service{"fixture": service})
	tool.SetShell(NewShell(processTestShell()))
	defer tool.Close()
	item := &session.Session{ID: "chat", Workspace: t.TempDir()}
	listed := tool.CallDetailed(context.Background(), item, map[string]any{"service": "fixture", "list": true})
	if listed.Err != nil || !strings.Contains(listed.Content, "echo — Echo stdio") {
		t.Fatalf("list=%+v", listed)
	}
	called := tool.CallDetailed(context.Background(), item, map[string]any{"service": "fixture", "tool": "echo"})
	if called.Err != nil || called.Content != "stdio hello" {
		t.Fatalf("call=%+v", called)
	}
}

func TestMCPStdioStopsAtIdleAndAppClose2rm(t *testing.T) {
	command := processTestCommand(fmt.Sprintf(`$env:AGENTB_MCP_HELPER='1'; & '%s' '-test.run=TestMCPStdioHelper'`, strings.ReplaceAll(os.Args[0], "'", "''")), fmt.Sprintf(`AGENTB_MCP_HELPER=1 %s -test.run=TestMCPStdioHelper`, strconv.Quote(os.Args[0])))
	service := config.Service{Kind: "mcp", BaseURL: "command:" + command, Auth: "none", AllowedMethods: []string{"POST"}, TimeoutS: 5, MaxBodyKB: 64}
	tool := NewCallService(map[string]config.Service{"fixture": service})
	tool.SetShell(NewShell(processTestShell()))
	clock := time.Now()
	tool.now = func() time.Time { return clock }
	item := &session.Session{ID: "chat", Workspace: t.TempDir()}
	if detail := tool.CallDetailed(context.Background(), item, map[string]any{"service": "fixture", "list": true}); detail.Err != nil {
		t.Fatal(detail.Err)
	}
	tool.mu.Lock()
	first := tool.mcpClients["fixture"]
	tool.mu.Unlock()
	clock = clock.Add(10 * time.Minute)
	tool.closeIdleMCP()
	select {
	case <-first.done:
	case <-time.After(5 * time.Second):
		t.Fatal("idle MCP command survived")
	}
	if detail := tool.CallDetailed(context.Background(), item, map[string]any{"service": "fixture", "list": true}); detail.Err != nil {
		t.Fatal(detail.Err)
	}
	tool.mu.Lock()
	second := tool.mcpClients["fixture"]
	tool.mu.Unlock()
	if first == second {
		t.Fatal("idle session was reused")
	}
	tool.Close()
	select {
	case <-second.done:
	case <-time.After(5 * time.Second):
		t.Fatal("MCP command survived app close")
	}
}

func TestMCPExitHangAndMalformedAreOneLine2rm(t *testing.T) {
	t.Run("exits", func(t *testing.T) {
		service := config.Service{Kind: "mcp", BaseURL: "command:" + processTestCommand(`exit 0`, `exit 0`), Auth: "none", TimeoutS: 1, MaxBodyKB: 64}
		tool := NewCallService(map[string]config.Service{"gone": service})
		tool.SetShell(NewShell(processTestShell()))
		defer tool.Close()
		detail := tool.CallDetailed(context.Background(), &session.Session{Workspace: t.TempDir()}, map[string]any{"service": "gone", "list": true})
		if detail.Err == nil || !strings.Contains(detail.Err.Error(), "gone") || strings.Contains(detail.Err.Error(), "\n") {
			t.Fatalf("detail=%+v", detail)
		}
	})
	for _, test := range []struct {
		name, body string
		hang       bool
	}{{"hangs", "", true}, {"malformed", "not-json", false}} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if test.hang {
					time.Sleep(200 * time.Millisecond)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			tool := NewCallService(map[string]config.Service{"remote": {Kind: "mcp", BaseURL: server.URL, Auth: "none", TimeoutS: 1, MaxBodyKB: 64}})
			defer tool.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			detail := tool.CallDetailed(ctx, nil, map[string]any{"service": "remote", "list": true})
			if detail.Err == nil || !strings.Contains(detail.Err.Error(), "remote") || strings.Contains(detail.Err.Error(), "\n") {
				t.Fatalf("detail=%+v", detail)
			}
		})
	}
}
