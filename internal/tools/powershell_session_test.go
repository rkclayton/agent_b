//go:build windows

package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/credential"
	"harness/internal/session"
)

type powerShellTokenFixture struct {
	mu     sync.Mutex
	values []string
	calls  int
}

func (*powerShellTokenFixture) Origin(string) (string, error) { return "https://example.test", nil }
func (f *powerShellTokenFixture) Token(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.values) == 0 {
		return "fixture-token", nil
	}
	index := min(f.calls-1, len(f.values)-1)
	return f.values[index], nil
}

func powerShellToolForTest(t *testing.T, executable string, root string, token *powerShellTokenFixture) *PowerShellSession {
	t.Helper()
	cfg := config.Defaults(root)
	cfg.Shell.Command = []string{executable, "-NoProfile", "-NonInteractive", "-Command"}
	cfg.Shell.ServiceAccount.Enabled = false
	shell := NewShell(cfg.Shell)
	shell.Configure(cfg)
	tool := NewPowerShellSession(shell, credential.NewVault(root), token, func() string { return root })
	if tool == nil {
		t.Fatalf("persistent PowerShell unavailable for %s", executable)
	}
	t.Cleanup(tool.Close)
	return tool
}

func powerShellHostsForTest(t *testing.T) []string {
	t.Helper()
	cfg := config.Defaults(t.TempDir())
	seven := shellHostFor(cfg.Shell).Executable
	five := filepath.Join(os.Getenv("SystemRoot"), `System32\WindowsPowerShell\v1.0\powershell.exe`)
	hosts := []string{seven}
	if !strings.EqualFold(seven, five) {
		hosts = append(hosts, five)
	}
	return hosts
}

func powerShellTestSession(root string, full bool) *session.Session {
	enabled := map[string]bool{"powershell_session": true, "powershell_session:full": full}
	return &session.Session{ID: "chat", AgentID: "fixture", Workspace: root, ToolsEnabled: enabled, ToolCalls: map[string]int{}}
}

func TestPersistentPowerShellKeepsStateBootstrapAndOnePID(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "powershell"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "powershell", "named.ps1"), []byte("$y = 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := powerShellToolForTest(t, powerShellHostsForTest(t)[0], root, &powerShellTokenFixture{})
	item := powerShellTestSession(root, true)
	if _, err := tool.Call(context.Background(), item, map[string]any{"command": "$x = 1"}); err != nil {
		t.Fatal(err)
	}
	first := tool.sessions[tool.key(item.ID, item.AgentID, "default")].process.(interface{ PID() int }).PID()
	got, err := tool.Call(context.Background(), item, map[string]any{"command": "$x"})
	if err != nil || strings.TrimSpace(got) != "1" {
		t.Fatalf("state result=%q err=%v", got, err)
	}
	second := tool.sessions[tool.key(item.ID, item.AgentID, "default")].process.(interface{ PID() int }).PID()
	if first != second {
		t.Fatalf("PID changed: %d then %d", first, second)
	}
	otherProfile := powerShellTestSession(root, true)
	otherProfile.AgentID = "other-fixture"
	got, err = tool.Call(context.Background(), otherProfile, map[string]any{"command": "if ($null -eq $x) { 'profile-isolated' }"})
	if err != nil || strings.TrimSpace(got) != "profile-isolated" {
		t.Fatalf("profile isolation result=%q err=%v", got, err)
	}
	otherPID := tool.sessions[tool.key(otherProfile.ID, otherProfile.AgentID, "default")].process.(interface{ PID() int }).PID()
	if otherPID == first {
		t.Fatalf("agent profiles shared PID %d", first)
	}
	got, err = tool.Call(context.Background(), item, map[string]any{"session": "named", "command": "$y; if ($null -eq $x) { 'isolated' }"})
	if err != nil || !strings.Contains(got, "7") || !strings.Contains(got, "isolated") {
		t.Fatalf("bootstrap result=%q err=%v", got, err)
	}
}

func TestPersistentPowerShellReadOnlyMatrixOnBothHosts(t *testing.T) {
	for _, host := range powerShellHostsForTest(t) {
		host := host
		t.Run(filepath.Base(filepath.Dir(host)), func(t *testing.T) {
			root := t.TempDir()
			tool := powerShellToolForTest(t, host, root, &powerShellTokenFixture{})
			read := powerShellTestSession(root, false)
			cases := []string{
				"Get-Mailbox | Remove-Mailbox",
				"ForEach-Object { Remove-Item $_ }",
				"rm x",
				"[IO.File]::Delete('x')",
				"Get-Content a > b",
				"& $cmd",
				"cmd /c del x",
				"Invoke-RestMethod http://127.0.0.1:1",
			}
			for _, command := range cases {
				if result, err := tool.Call(context.Background(), read, map[string]any{"command": command}); err == nil || !strings.HasPrefix(err.Error(), "read-only profile:") || result != "" {
					t.Fatalf("%q result=%q err=%v", command, result, err)
				}
			}
			allowed := "Get-Process | Where-Object CPU -gt 1 | Select-Object Name | ConvertTo-Json"
			if _, err := tool.Call(context.Background(), read, map[string]any{"command": allowed}); err != nil {
				t.Fatalf("allowed pipeline: %v", err)
			}
			full := powerShellTestSession(root, true)
			for _, command := range cases {
				if _, err := tool.Call(context.Background(), full, map[string]any{"command": command}); err != nil && strings.Contains(err.Error(), "read-only profile") {
					t.Fatalf("full mode refused %q: %v", command, err)
				}
			}
			if _, err := tool.Call(context.Background(), full, map[string]any{"command": allowed}); err != nil {
				t.Fatalf("full mode allowed pipeline: %v", err)
			}
		})
	}
}

func TestPersistentPowerShellCredentialHandoffExpiryLimitsAndClose(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "powershell"), 0o700); err != nil {
		t.Fatal(err)
	}
	bootstrap := "# credentials: graph\n$bootstrapSaw = $AgentB.graph\n"
	if err := os.WriteFile(filepath.Join(root, "powershell", "graph.ps1"), []byte(bootstrap), 0o600); err != nil {
		t.Fatal(err)
	}
	vault := credential.NewVault(root)
	if err := vault.PutEntra("graph", "https://example.test", "tenant-placeholder", "client-placeholder", []string{"scope-placeholder"}); err != nil {
		t.Fatal(err)
	}
	tokens := &powerShellTokenFixture{values: []string{"token-one", "token-two"}}
	cfg := config.Defaults(root)
	cfg.Shell.ServiceAccount.Enabled = false
	shell := NewShell(cfg.Shell)
	shell.Configure(cfg)
	tool := NewPowerShellSession(shell, vault, tokens, func() string { return root })
	if tool == nil {
		t.Fatal("tool unavailable")
	}
	t.Cleanup(tool.Close)
	item := powerShellTestSession(root, true)
	got, err := tool.Call(context.Background(), item, map[string]any{"session": "graph", "command": "if ($bootstrapSaw -eq 'token-one') { 'handoff-ok' }"})
	if err != nil || strings.TrimSpace(got) != "handoff-ok" || strings.Contains(got, "token-one") {
		t.Fatalf("handoff result=%q err=%v", got, err)
	}
	firstPID := tool.sessions[tool.key(item.ID, item.AgentID, "graph")].process.(interface{ PID() int }).PID()
	got, err = tool.Call(context.Background(), item, map[string]any{"session": "graph", "command": "if ($AgentB.graph -eq 'token-one') { 'expired token' } else { 'refreshed' }"})
	if err != nil || strings.TrimSpace(got) != "refreshed" || tokens.calls != 2 {
		t.Fatalf("refresh result=%q calls=%d err=%v", got, tokens.calls, err)
	}
	secondPID := tool.sessions[tool.key(item.ID, item.AgentID, "graph")].process.(interface{ PID() int }).PID()
	if firstPID == secondPID {
		t.Fatalf("expiry kept PID %d", firstPID)
	}
	if _, err := tool.Call(context.Background(), item, map[string]any{"session": "graph", "command": "'invalid token'"}); err == nil || !strings.Contains(err.Error(), "after one refresh") || tokens.calls != 3 {
		t.Fatalf("second expiry calls=%d err=%v", tokens.calls, err)
	}
	if err := os.WriteFile(filepath.Join(root, "powershell", "missing.ps1"), []byte("# credentials: unsigned-in\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Call(context.Background(), item, map[string]any{"session": "missing", "command": "'never'"}); err == nil || !strings.Contains(err.Error(), "auth_error: sign in in Settings → Security → Credentials") {
		t.Fatalf("missing credential err=%v", err)
	}
	if err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return walkErr
		}
		content, readErr := os.ReadFile(path)
		if readErr == nil && strings.Contains(string(content), "token-one") {
			t.Errorf("credential appeared in %s", filepath.Base(path))
		}
		return readErr
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := tool.Call(context.Background(), item, map[string]any{"command": "'ready'"}); err != nil {
		t.Fatal(err)
	}
	tool.commandLimit = 5 * time.Second
	if _, err := tool.Call(context.Background(), item, map[string]any{"command": "Start-Sleep -Seconds 10"}); err == nil || !strings.Contains(err.Error(), "5s command limit") {
		t.Fatalf("limit err=%v", err)
	}
	if _, err := tool.Call(context.Background(), item, map[string]any{"command": "'reopened'"}); err != nil {
		t.Fatal(err)
	}
	tool.idleLimit = 30 * time.Millisecond
	defaultPID := tool.sessions[tool.key(item.ID, item.AgentID, "default")].process.(interface{ PID() int }).PID()
	time.Sleep(80 * time.Millisecond)
	tool.closeIdle(time.Now())
	if tool.sessions[tool.key(item.ID, item.AgentID, "default")] != nil {
		t.Fatal("idle session stayed open")
	}
	if _, err := tool.Call(context.Background(), item, map[string]any{"command": "'again'"}); err != nil {
		t.Fatal(err)
	}
	newDefaultPID := tool.sessions[tool.key(item.ID, item.AgentID, "default")].process.(interface{ PID() int }).PID()
	if newDefaultPID == defaultPID {
		t.Fatalf("idle close reused PID %d", defaultPID)
	}
	tool.CloseSession(item.ID)
	if len(tool.sessions) != 0 {
		t.Fatalf("close left %d session(s)", len(tool.sessions))
	}
}
