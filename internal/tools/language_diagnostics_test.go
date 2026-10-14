package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
)

func TestLanguageDiagnosticsAfterEditBoundsAndFormats(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{{"one", "2:3 error broken type"}, {"many", "10 more"}} {
		t.Run(tc.mode, func(t *testing.T) {
			root := t.TempDir()
			path := root + string(os.PathSeparator) + "bad.go"
			if err := os.WriteFile(path, []byte("package fixture\nvar value int = 1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			edit, _, _, _ := testTools(root)
			exe, _ := os.Executable()
			manager := NewLanguageDiagnostics(NewShell(config.Defaults(root).Shell), map[string][]string{".go": {exe, "-test.run=TestLanguageServerFixtureProcess", "--", tc.mode}})
			t.Cleanup(manager.Close)
			edit.coordinator.SetDiagnostics(manager)
			got, err := edit.Call(context.Background(), testSession(root, "s", "S"), map[string]any{"path": "bad.go", "old_string": "var value int = 1", "new_string": "var value int = 1 // changed"})
			if err != nil || !strings.Contains(got, tc.want) {
				t.Fatalf("result=%q err=%v", got, err)
			}
		})
	}
}

func TestLanguageDiagnosticsTimeoutKeepsWriteAndMissingServerKeepsBytes(t *testing.T) {
	root := t.TempDir()
	exe, _ := os.Executable()
	baseCoordinator := NewFileCoordinator(session.NewWorkspaceRegistry(), func(string) string { return "" }, events.NewBus())
	plain := NewWriteFile(baseCoordinator)
	item := testSession(root, "plain", "plain")
	plainResult, err := plain.Call(context.Background(), item, map[string]any{"path": "plain.xyz", "content": "same"})
	if err != nil {
		t.Fatal(err)
	}
	missingCoordinator := NewFileCoordinator(session.NewWorkspaceRegistry(), func(string) string { return "" }, events.NewBus())
	missing := NewLanguageDiagnostics(NewShell(config.Defaults(root).Shell), map[string][]string{".xyz": {"server-that-is-not-installed"}})
	t.Cleanup(missing.Close)
	missingCoordinator.SetDiagnostics(missing)
	missingResult, err := NewWriteFile(missingCoordinator).Call(context.Background(), testSession(root, "missing", "missing"), map[string]any{"path": "missing.xyz", "content": "same"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Replace(missingResult, "missing.xyz", "plain.xyz", 1) != plainResult {
		t.Fatalf("missing server changed result: %q / %q", missingResult, plainResult)
	}
	manager := NewLanguageDiagnostics(NewShell(config.Defaults(root).Shell), map[string][]string{".go": {exe, "-test.run=TestLanguageServerFixtureProcess", "--", "silent"}})
	manager.timeout = 40 * time.Millisecond
	t.Cleanup(manager.Close)
	coordinator := NewFileCoordinator(session.NewWorkspaceRegistry(), func(string) string { return "" }, events.NewBus())
	coordinator.SetDiagnostics(manager)
	got, err := NewWriteFile(coordinator).Call(context.Background(), testSession(root, "s", "s"), map[string]any{"path": "kept.go", "content": "package fixture\n"})
	if err != nil || !strings.Contains(got, "no diagnostics in 3 s") {
		t.Fatalf("result=%q err=%v", got, err)
	}
	if _, err := os.Stat(root + string(os.PathSeparator) + "kept.go"); err != nil {
		t.Fatal(err)
	}
}

func TestLanguageDiagnosticsIdleAndCloseStopServers(t *testing.T) {
	root := t.TempDir()
	exe, _ := os.Executable()
	manager := NewLanguageDiagnostics(NewShell(config.Defaults(root).Shell), map[string][]string{".go": {exe, "-test.run=TestLanguageServerFixtureProcess", "--", "one"}})
	manager.idle = 20 * time.Millisecond
	manager.timeout = time.Second
	if _, ok := manager.Check(context.Background(), root, root+string(os.PathSeparator)+"one.go"); !ok {
		t.Fatal("server did not start")
	}
	time.Sleep(60 * time.Millisecond)
	manager.ReapIdle()
	if manager.Active() != 0 {
		t.Fatal("idle server remains")
	}
	if _, ok := manager.Check(context.Background(), root, root+string(os.PathSeparator)+"two.go"); !ok {
		t.Fatal("server did not restart")
	}
	manager.Close()
	if manager.Active() != 0 {
		t.Fatal("close left a server")
	}
}

func TestLanguageDiagnosticsCacheStaysAtItsFullBound(t *testing.T) {
	root := t.TempDir()
	exe, _ := os.Executable()
	manager := NewLanguageDiagnostics(NewShell(config.Defaults(root).Shell), map[string][]string{".go": {exe, "-test.run=TestLanguageServerFixtureProcess", "--", "one"}})
	manager.timeout = time.Second
	t.Cleanup(manager.Close)
	for index := 0; index < 17; index++ {
		folder := fmt.Sprintf("%s%c%d", root, os.PathSeparator, index)
		if err := os.MkdirAll(folder, 0o700); err != nil {
			t.Fatal(err)
		}
		path := folder + string(os.PathSeparator) + "one.go"
		if err := os.WriteFile(path, []byte("package fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := manager.Check(context.Background(), folder, path); !ok {
			t.Fatal("server unavailable")
		}
	}
	if manager.Active() != 16 {
		t.Fatalf("active=%d, want bounded 16", manager.Active())
	}
}

func TestLanguageServerFixtureProcess(t *testing.T) {
	if len(os.Args) < 2 || !strings.Contains(strings.Join(os.Args, " "), "TestLanguageServerFixtureProcess") {
		return
	}
	mode := os.Args[len(os.Args)-1]
	reader := bufio.NewReader(os.Stdin)
	lastQuery := ""
	fixtureRoot, _ := os.Getwd()
	for {
		header, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if !strings.HasPrefix(strings.ToLower(header), "content-length:") {
			continue
		}
		n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(strings.ToLower(header), "content-length:")))
		_, _ = reader.ReadString('\n')
		body := make([]byte, n)
		_, _ = io.ReadFull(reader, body)
		var message map[string]any
		_ = json.Unmarshal(body, &message)
		if message["method"] == "initialize" {
			fixtureLSPWrite(map[string]any{"jsonrpc": "2.0", "id": message["id"], "result": map[string]any{"capabilities": map[string]any{"textDocumentSync": 1}}})
			continue
		}
		if mode == "code" {
			method, _ := message["method"].(string)
			params, _ := message["params"].(map[string]any)
			if method == "workspace/symbol" {
				query, _ := params["query"].(string)
				lastQuery = query
				uri := fileURI(filepath.Join(fixtureRoot, "main.go"))
				lines := []int{9}
				name := "Target"
				if query == "Duplicate" {
					lines, name = []int{9, 19}, "Duplicate"
				} else if query == "Outside" {
					name = "Outside"
				}
				result := make([]any, 0, len(lines))
				for _, line := range lines {
					result = append(result, map[string]any{"name": name, "kind": 12, "location": map[string]any{"uri": uri, "range": map[string]any{"start": map[string]any{"line": line, "character": 5}, "end": map[string]any{"line": line, "character": 11}}}})
				}
				fixtureLSPWrite(map[string]any{"jsonrpc": "2.0", "id": message["id"], "result": result})
				continue
			}
			if method == "textDocument/definition" {
				doc := params["textDocument"].(map[string]any)
				uri := doc["uri"]
				fixtureLSPWrite(map[string]any{"jsonrpc": "2.0", "id": message["id"], "result": []any{map[string]any{"uri": uri, "range": map[string]any{"start": map[string]any{"line": 9, "character": 0}, "end": map[string]any{"line": 9, "character": 16}}}}})
				continue
			}
			if method == "textDocument/references" {
				doc := params["textDocument"].(map[string]any)
				result := make([]any, 80)
				for i := range result {
					result[i] = map[string]any{"uri": doc["uri"], "range": map[string]any{"start": map[string]any{"line": i, "character": 0}, "end": map[string]any{"line": i, "character": 6}}}
				}
				if lastQuery == "Outside" {
					result[0].(map[string]any)["uri"] = fileURI(filepath.Join(filepath.Dir(fixtureRoot), "outside.go"))
				}
				fixtureLSPWrite(map[string]any{"jsonrpc": "2.0", "id": message["id"], "result": result})
				continue
			}
			if method == "textDocument/documentSymbol" {
				result := []any{map[string]any{
					"name": "Target", "kind": 12,
					"range": map[string]any{"start": map[string]any{"line": 9, "character": 0}, "end": map[string]any{"line": 9, "character": 16}},
				}}
				fixtureLSPWrite(map[string]any{"jsonrpc": "2.0", "id": message["id"], "result": result})
				continue
			}
		}
		if message["method"] != "textDocument/didOpen" || mode == "silent" {
			continue
		}
		count := 1
		if mode == "many" {
			count = 30
		}
		diagnostics := make([]any, count)
		for i := range diagnostics {
			diagnostics[i] = map[string]any{"range": map[string]any{"start": map[string]any{"line": 1, "character": 2}, "end": map[string]any{"line": 1, "character": 3}}, "severity": 1, "message": fmt.Sprintf("broken type %d", i+1)}
		}
		params := message["params"].(map[string]any)
		document := params["textDocument"].(map[string]any)
		fixtureLSPWrite(map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics", "params": map[string]any{"uri": document["uri"], "diagnostics": diagnostics}})
	}
}

func fixtureLSPWrite(value any) {
	data, _ := json.Marshal(value)
	fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n%s", len(data), data)
}
