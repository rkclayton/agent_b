package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/credential"
	"harness/internal/events"
	"harness/internal/session"
)

// Item 2ss: deny entries identify commands, never matching lookalike text in
// an option or quoted argument.
func TestDenyListMatchesCommandPositions2ss(t *testing.T) {
	entries := []string{"rm -rf /", "format ", "diskpart", "shutdown", `Remove-Item -Recurse -Force C:\`}
	allowed := []string{`git branch --sort=-committerdate --format '%(refname)'`, `git log --format=oneline`, `Get-Date -Format o`, `Write-Output 'shutdown'`, `echo "format D:"`, `echo diskpart`, `Write-Output "Remove-Item -Recurse -Force C:\\"`, `rm -rf /tmp`, `Write-Output --format value`}
	for _, source := range allowed {
		if got := deniedCommand(source, entries); got != "" {
			t.Errorf("%q matched %q", source, got)
		}
	}
	blocked := []string{`format D:`, `diskpart`, `shutdown /s`, `shutdown.exe /s`, `rm -rf /`, `Remove-Item -Recurse -Force C:\`, `echo ok | shutdown`, `echo ok; diskpart`, `echo ok && format D:`, `& shutdown`}
	for _, source := range blocked {
		if got := deniedCommand(source, entries); got == "" {
			t.Errorf("%q was allowed", source)
		}
	}
}

func TestShellDescriptionOnlyClaimsServiceNetworkBoundaryWhenSplitIsOn(t *testing.T) {
	cfg := config.Defaults(t.TempDir())
	cfg.Shell.ServiceAccount.Enabled = false
	off := NewShell(cfg.Shell).Description()
	if strings.Contains(strings.ToLower(off), "no public network") || strings.Contains(strings.ToLower(off), "service context") {
		t.Fatalf("split-off description=%q", off)
	}
	cfg.Shell.ServiceAccount.Enabled = true
	on := NewShell(cfg.Shell).Description()
	if !strings.Contains(on, "no public network (enforced outside the tool layer)") || strings.Contains(on, "service context") {
		t.Fatalf("split-on description=%q", on)
	}
}

type missingShellCredential struct{}

func (missingShellCredential) Read() ([]byte, error) { return nil, credential.ErrNotStored }

type presentShellCredential struct{}

func (presentShellCredential) Read() ([]byte, error) { return []byte{1, 2, 3}, nil }

type completedShellProcess struct{}

func (completedShellProcess) Wait() (int, error) { return 0, nil }
func (completedShellProcess) KillTree()          {}

type exitedShellProcess struct{ code int }

func (p exitedShellProcess) Wait() (int, error) { return p.code, nil }
func (exitedShellProcess) KillTree()            {}

func TestShellDescriptionNamesConfiguredDialect(t *testing.T) {
	shell := NewShell(config.Shell{Command: []string{`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`}})
	// Item 2gb (v1.0.1): a configured Windows PowerShell resolves to PowerShell 7
	// where the host has one, and the description names whichever it runs.
	want := "Windows PowerShell 5.1: `&&` and `||` are rewritten to `; if ($?) { … }` at the top level, so they work; everything else is 5.1 syntax."
	if powerShell7() != "" {
		want = "PowerShell 7: `&&` and `||` work."
	}
	if got := shell.Description(); !strings.HasSuffix(got, want) {
		t.Fatalf("PowerShell description = %q", got)
	}

	shell.Configure(config.Config{Shell: config.Shell{Command: []string{"bash", "-c"}}})
	if got := shell.Description(); !strings.HasSuffix(got, "Use bash syntax.") {
		t.Fatalf("configured description = %q", got)
	}
}

func TestOperatorOnlyInterpreterReturnsRunAsYouReason(t *testing.T) {
	lookup := func(name string) (string, error) {
		return filepath.Join(`C:\Users\someone`, "AppData", "Local", "Programs", name+".exe"), nil
	}
	if got := operatorOnlyInterpreter("python task.py", lookup, `C:\Users\someone`); got != "python" {
		t.Fatalf("interpreter=%q", got)
	}
	if got := operatorOnlyInterpreter("node task.js", func(string) (string, error) { return `C:\Program Files\nodejs\node.exe`, nil }, `C:\Users\someone`); got != "" {
		t.Fatalf("all-users interpreter=%q", got)
	}
}

func TestShellDescriptionOperatorClauseOnlyWhenSplitEnabled(t *testing.T) {
	cfg := config.Defaults(t.TempDir())
	cfg.Shell.ServiceAccount.Enabled = false
	shell := NewShell(cfg.Shell)
	shell.Configure(cfg)
	without := shell.Description()
	cfg.Shell.ServiceAccount.Enabled = true
	shell.Configure(cfg)
	with := shell.Description()
	clause := " Git and other configured commands need one decision per run; expect one prompt, not one per call."
	if !strings.HasSuffix(with, clause) || !strings.Contains(with, "no public network (enforced outside the tool layer)") || strings.Contains(with, "service context") {
		t.Fatalf("split-on description=%q", with)
	}
	cfg.Tools.Shell.OperatorCommands = []string{}
	shell.Configure(cfg)
	if got := shell.Description(); got != strings.TrimSuffix(with, clause) {
		t.Fatalf("explicit empty operator list retained clause: %q", got)
	}
	if strings.Contains(without, "service context") {
		t.Fatalf("split-off description retained boundary: %q", without)
	}
}

func TestOperatorCommandMatchesResolvedFirstExecutableOnly(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	cfg := config.Defaults(t.TempDir())
	cfg.Tools.Shell.OperatorCommands = []string{"git"}
	shell := NewShell(cfg.Shell)
	shell.Configure(cfg)
	match, ok := shell.OperatorCommand(map[string]any{"command": "git status"})
	if !ok || !strings.EqualFold(match.Executable, canonicalExecutable(t, gitPath)) || match.Name != "git" {
		t.Fatalf("match=%+v ok=%t", match, ok)
	}
	if _, ok := shell.OperatorCommand(map[string]any{"command": "Write-Output git status"}); ok {
		t.Fatal("git outside first argv matched")
	}
	fakeDir := t.TempDir()
	fake := filepath.Join(fakeDir, filepath.Base(gitPath))
	if err := os.WriteFile(fake, []byte("fake"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, ok := shell.OperatorCommand(map[string]any{"command": fmt.Sprintf("& %q status", fake)}); ok {
		t.Fatal("same-basename workspace executable matched configured git")
	}
}

func canonicalExecutable(t *testing.T, value string) string {
	t.Helper()
	absolute, err := filepath.Abs(value)
	if err != nil {
		t.Fatal(err)
	}
	if evaluated, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = evaluated
	}
	return filepath.Clean(absolute)
}

func TestShellServiceAccountSpawnFailureRequiresOperatorApproval(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root).Shell
	cfg.ServiceAccount.Enabled = true
	shell := NewShell(cfg)
	shell.Configure(config.Config{Workspace: root, Shell: cfg})
	shell.SetCredentialStore(presentShellCredential{})
	shell.startService = func(string, []string, string, []string, config.ShellServiceAccount, []byte, *lockedBuffer) (runningShellProcess, error) {
		return nil, &serviceSpawnError{kind: "service-account authentication failed"}
	}
	var reported ShellIdentityStatus
	shell.SetIdentityReporter(func(status ShellIdentityStatus) { reported = status })
	command := "printf must-not-run"
	if runtime.GOOS == "windows" {
		command = "Write-Output must-not-run"
	}
	detail := shell.CallDetailed(context.Background(), &session.Session{ID: "test", Workspace: root}, map[string]any{"command": command})
	if detail.Err != nil || detail.OperatorOverrideReason == "" || strings.Contains(detail.Content, "must-not-run") {
		t.Fatalf("detail=%+v, want an unexecuted operator-approval request", detail)
	}
	if reported.Fallback || !reported.OperatorApprovalRequired || !strings.Contains(reported.Reason, "authentication failed") || reported.Since == "" {
		t.Fatalf("identity report = %+v", reported)
	}
	if _, err := time.Parse(time.RFC3339, reported.Since); err != nil {
		t.Fatalf("alarm timestamp = %q: %v", reported.Since, err)
	}
}

func TestShellServiceAccountDisabledDoesNotRaiseIdentityAlarm(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root).Shell
	cfg.ServiceAccount.Enabled = false
	shell := NewShell(cfg)
	called := false
	shell.SetIdentityReporter(func(status ShellIdentityStatus) { called = true })
	command := "printf disabled-ok"
	if runtime.GOOS == "windows" {
		command = "Write-Output disabled-ok"
	}
	result, err := shell.Call(context.Background(), &session.Session{ID: "test", Workspace: root}, map[string]any{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "disabled-ok") || called || shell.IdentityStatus().Fallback || shell.IdentityStatus().OperatorApprovalRequired {
		t.Fatalf("result=%q called=%v status=%+v", result, called, shell.IdentityStatus())
	}
}

func TestShellNonzeroExitIsFailureAndIncludesStderr(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root).Shell
	cfg.ServiceAccount.Enabled = false
	command := "printf stderr-marker >&2; exit 7"
	if runtime.GOOS == "windows" {
		command = "[Console]::Error.WriteLine('stderr-marker'); exit 7"
	} else {
		cfg.Command = []string{"sh", "-c"}
	}
	registry := New(NewShell(cfg))
	item := &session.Session{ID: "test", Workspace: root, ToolsEnabled: map[string]bool{"shell": true}}
	outcome := registry.CallDetailed(context.Background(), item, "shell", map[string]any{"command": command})
	if outcome.OK || !strings.HasPrefix(outcome.Content, "error: command failed\nexit=7") || !strings.Contains(outcome.Content, "stderr-marker") {
		t.Fatalf("nonzero shell outcome=%+v", outcome)
	}
}

func TestShellStreamsLatestOutputLine2qf(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root).Shell
	cfg.ServiceAccount.Enabled = false
	command := "for i in $(seq 1 12); do printf 'line-%s\\n' \"$i\"; sleep 5; done"
	if runtime.GOOS == "windows" {
		command = "1..12 | ForEach-Object { Write-Output ('line-' + $_); Start-Sleep -Seconds 5 }"
	}
	type observed struct {
		line string
		at   time.Time
	}
	var mu sync.Mutex
	seen := []observed{}
	ctx := WithOutputObserver(context.Background(), func(line string) { mu.Lock(); seen = append(seen, observed{line: line, at: time.Now()}); mu.Unlock() })
	if _, err := NewShell(cfg).Call(ctx, &session.Session{ID: "test", Workspace: root}, map[string]any{"command": command, "timeout_s": 90}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	full := []observed{}
	for _, item := range seen {
		if strings.HasPrefix(item.line, "line-") && (len(full) == 0 || item.line != full[len(full)-1].line) {
			full = append(full, item)
		}
	}
	if len(full) != 12 || full[len(full)-1].line != "line-12" {
		t.Fatalf("streamed lines=%v", seen)
	}
	for index := 1; index < len(full); index++ {
		if gap := full[index].at.Sub(full[index-1].at); gap < 4*time.Second || gap > 6*time.Second {
			t.Fatalf("line %d delivery gap=%s", index+1, gap)
		}
	}
}

func TestShellOperatorContextBypassesServiceIdentityAndRaisesPersistentWarning(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root).Shell
	cfg.OperatorContext = true
	cfg.ServiceAccount.Enabled = true
	shell := NewShell(cfg)
	serviceStarted := false
	shell.startService = func(string, []string, string, []string, config.ShellServiceAccount, []byte, *lockedBuffer) (runningShellProcess, error) {
		serviceStarted = true
		return completedShellProcess{}, nil
	}
	var reported ShellIdentityStatus
	shell.SetIdentityReporter(func(status ShellIdentityStatus) { reported = status })
	shell.Configure(config.Config{Workspace: root, Shell: cfg})
	command := "printf operator-ok"
	if runtime.GOOS == "windows" {
		command = "Write-Output operator-ok"
	}
	detail := shell.CallDetailed(context.Background(), &session.Session{ID: "test", Workspace: root}, map[string]any{"command": command})
	if detail.Err != nil || !detail.OperatorContext || !strings.Contains(detail.Content, "operator-ok") {
		t.Fatalf("detail=%+v", detail)
	}
	if serviceStarted || !reported.OperatorContext || reported.OperatorApprovalRequired || reported.Since == "" {
		t.Fatalf("serviceStarted=%v identity=%+v", serviceStarted, reported)
	}
}

func TestShellServiceAccountSuccessClearsPersistentIdentityAlarm(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root).Shell
	cfg.ServiceAccount.Enabled = true
	shell := NewShell(cfg)
	shell.Configure(config.Config{Workspace: root, Shell: cfg})
	shell.SetCredentialStore(presentShellCredential{})
	shell.identity = ShellIdentityStatus{OperatorApprovalRequired: true, Reason: "earlier failure", Since: time.Now().UTC().Format(time.RFC3339)}
	shell.startService = func(string, []string, string, []string, config.ShellServiceAccount, []byte, *lockedBuffer) (runningShellProcess, error) {
		return completedShellProcess{}, nil
	}
	message, err := shell.TestServiceAccount(context.Background())
	if err != nil || !strings.Contains(message, "succeeded") {
		t.Fatalf("message=%q err=%v", message, err)
	}
	if status := shell.IdentityStatus(); status.Fallback || status.OperatorApprovalRequired || status.Reason != "" || status.Since != "" {
		t.Fatalf("identity status was not cleared: %+v", status)
	}
}

func TestShellServiceAccountTestFailureRaisesPersistentIdentityAlarm(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root).Shell
	cfg.ServiceAccount.Enabled = true
	shell := NewShell(cfg)
	shell.Configure(config.Config{Workspace: root, Shell: cfg})
	shell.SetCredentialStore(presentShellCredential{})
	shell.startService = func(string, []string, string, []string, config.ShellServiceAccount, []byte, *lockedBuffer) (runningShellProcess, error) {
		return nil, &serviceSpawnError{kind: "service account cannot access the configured workspace"}
	}
	message, err := shell.TestServiceAccount(context.Background())
	status := shell.IdentityStatus()
	if err == nil || !strings.Contains(message, "workspace") || !status.OperatorApprovalRequired || status.Reason != message || status.Since == "" {
		t.Fatalf("message=%q err=%v status=%+v", message, err, status)
	}
}

func TestShellServiceAccountTestResolvesRelativeWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	cfg := config.Defaults(workspace).Shell
	cfg.ServiceAccount.Enabled = true
	shell := NewShell(cfg)
	shell.Configure(config.Config{Workspace: "./workspace", Shell: cfg})
	shell.SetCredentialStore(presentShellCredential{})
	var received string
	shell.startService = func(_ string, _ []string, got string, _ []string, _ config.ShellServiceAccount, _ []byte, _ *lockedBuffer) (runningShellProcess, error) {
		received = got
		return completedShellProcess{}, nil
	}
	message, err := shell.TestServiceAccount(context.Background())
	if err != nil || !strings.Contains(message, "succeeded") {
		t.Fatalf("message=%q err=%v", message, err)
	}
	if received != workspace || !filepath.IsAbs(received) {
		t.Fatalf("service-account working directory = %q, want absolute %q", received, workspace)
	}
}

func TestShellServicePermissionDenialOffersOperatorOverride(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root).Shell
	cfg.ServiceAccount.Enabled = true
	shell := NewShell(cfg)
	shell.Configure(config.Config{Workspace: root, Shell: cfg})
	shell.SetCredentialStore(presentShellCredential{})
	shell.startService = func(_ string, _ []string, _ string, _ []string, _ config.ShellServiceAccount, _ []byte, output *lockedBuffer) (runningShellProcess, error) {
		_, _ = output.Write([]byte("Set-Content: Access to the path is denied.\nUnauthorizedAccessException\n"))
		return exitedShellProcess{code: 1}, nil
	}
	detail := shell.CallDetailed(context.Background(), &session.Session{ID: "test", Workspace: root}, map[string]any{"command": "Set-Content protected.txt value"})
	if detail.Err != nil {
		t.Fatal(detail.Err)
	}
	if detail.OperatorOverrideReason == "" || !strings.Contains(detail.Content, "exit=1") {
		t.Fatalf("detail=%+v, want permission override", detail)
	}
}

func TestShellSpawnFailureDoesNotExecuteOperatorCommand(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root).Shell
	cfg.ServiceAccount.Enabled = true
	shell := NewShell(cfg)
	shell.Configure(config.Config{Workspace: root, Shell: cfg})
	shell.SetCredentialStore(presentShellCredential{})
	shell.startService = func(string, []string, string, []string, config.ShellServiceAccount, []byte, *lockedBuffer) (runningShellProcess, error) {
		return nil, &serviceSpawnError{kind: "service-account authentication failed"}
	}
	marker := filepath.Join(root, "must-not-exist")
	command := "touch must-not-exist"
	if runtime.GOOS == "windows" {
		command = "New-Item must-not-exist"
	}
	detail := shell.CallDetailed(context.Background(), &session.Session{ID: "test", Workspace: root}, map[string]any{"command": command})
	if detail.Err != nil || detail.OperatorOverrideReason == "" {
		t.Fatalf("detail=%+v", detail)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Run as you command ran without approval: %v", err)
	}
}

func TestShellOperatorOverrideBypassesServiceSpawner(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root).Shell
	cfg.ServiceAccount.Enabled = true
	shell := NewShell(cfg)
	shell.Configure(config.Config{Workspace: root, Shell: cfg})
	shell.startService = func(string, []string, string, []string, config.ShellServiceAccount, []byte, *lockedBuffer) (runningShellProcess, error) {
		t.Fatal("operator override called service-account process starter")
		return nil, nil
	}
	command := "printf operator-ok"
	if runtime.GOOS == "windows" {
		command = "Write-Output operator-ok"
	}
	result, err := shell.CallAsOperator(context.Background(), &session.Session{ID: "test", Workspace: root}, map[string]any{"command": command})
	if err != nil || !strings.Contains(result, "operator-ok") {
		t.Fatalf("result=%q err=%v", result, err)
	}
}

func TestRemovedBuiltInCommandRulesReachExecution2tc(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	cfg.Shell.ServiceAccount.Enabled = true
	shell := NewShell(cfg.Shell)
	shell.Configure(cfg)
	shell.SetCredentialStore(presentShellCredential{})
	coordinator := NewFileCoordinator(session.NewWorkspaceRegistry(), func(string) string { return "test" }, events.NewBus())
	item := &session.Session{ID: "removed-rules", Workspace: root, LastSeen: map[string]time.Time{}}
	if _, err := NewWriteFile(coordinator).Call(context.Background(), item, map[string]any{"path": "generated.ps1", "content": "Write-Output generated"}); err != nil {
		t.Fatal(err)
	}
	shell.SetFileCoordinator(coordinator)
	starts := 0
	shell.startService = func(string, []string, string, []string, config.ShellServiceAccount, []byte, *lockedBuffer) (runningShellProcess, error) {
		starts++
		return completedShellProcess{}, nil
	}
	commands := []string{
		`powershell -ExecutionPolicy Bypass -Command "Write-Output ok"`,
		`powershell -File generated.ps1`,
		`cmd /c generated.cmd`,
		`wscript generated.vbs`,
		`mshta generated.hta`,
		`regsvr32 /s payload.dll`,
		`certutil -decode payload.txt payload.exe`,
		`Set-AuthenticodeSignature x.exe`,
		`certutil -addstore Root x.cer`,
	}
	for _, command := range commands {
		detail := shell.CallDetailed(context.Background(), item, map[string]any{"command": command})
		if detail.Err != nil || detail.OperatorOverrideReason != "" {
			t.Errorf("command %q did not reach execution: %+v", command, detail)
		}
	}
	if starts != len(commands) {
		t.Fatalf("started=%d want=%d", starts, len(commands))
	}
}

func TestPermissionDeniedOutput(t *testing.T) {
	for _, value := range []string{
		"Access is denied.",
		"Access to the path 'x' is denied.",
		"permission denied",
		"CategoryInfo: PermissionDenied: (:) [], UnauthorizedAccessException",
		"The requested operation requires elevation.",
		"operation not permitted",
	} {
		if !permissionDeniedOutput(value) {
			t.Errorf("did not recognize %q", value)
		}
	}
	if permissionDeniedOutput("the file is locked by another process") {
		t.Fatal("ordinary command failure was classified as a permission denial")
	}
}

func TestServiceBoundaryReasonOffersOverrideOnlyForOperatorVisibleAbsoluteExecutable(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "python.exe")
	if err := os.WriteFile(executable, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	output := "The term '" + executable + "' is not recognized as the name of a \r\n cmdlet"
	if reason := serviceBoundaryReason("& "+quotePowerShellTest(executable)+" task.py", output); !strings.Contains(reason, "user-visible executable") {
		t.Fatalf("reason=%q", reason)
	}
	if reason := serviceBoundaryReason("missing-command task.py", "missing-command is not recognized as the name of a cmdlet"); reason != "" {
		t.Fatalf("a typo must not offer an operator retry: %q", reason)
	}
}

func quotePowerShellTest(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func TestShellFileRoutingCompoundAllowed(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "route.txt"), []byte("ROUTE_OK\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults(root).Shell
	command := "find . | wc -l"
	if runtime.GOOS == "windows" {
		command = "Get-ChildItem . | ForEach-Object { $_.Name }"
	}
	got, err := NewShell(cfg).Call(context.Background(), &session.Session{ID: "test", Workspace: root}, map[string]any{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, `"refused":true`) {
		t.Fatalf("compound command was refused: %s", got)
	}
}
