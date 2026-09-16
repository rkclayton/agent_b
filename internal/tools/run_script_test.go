package tools

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/session"
)

func TestRunScriptCapabilitySuiteUsesStdinAndExactPowerShellContract(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	cfg.Shell.ServiceAccount.Enabled = true
	shell := NewShell(cfg.Shell)
	shell.Configure(cfg)
	shell.SetCredentialStore(presentShellCredential{})
	var executable string
	var argv []string
	var input []byte
	shell.startServiceInput = func(gotExecutable string, gotArgv []string, _ string, _ []string, _ config.ShellServiceAccount, _ []byte, gotInput []byte, _ *lockedBuffer) (runningShellProcess, error) {
		executable, argv, input = gotExecutable, append([]string(nil), gotArgv...), append([]byte(nil), gotInput...)
		return completedShellProcess{}, nil
	}
	tool := NewRunScript(shell)
	source := "$values = 1..3\n$values | ForEach-Object { $_ * 2 }"
	detail := tool.CallDetailed(context.Background(), &session.Session{ID: "test", Workspace: root}, map[string]any{"language": "powershell", "source": source})
	if detail.Err != nil {
		t.Fatal(detail.Err)
	}
	if executable != cfg.Shell.Command[0] || !reflect.DeepEqual(argv, []string{"-NoProfile", "-NonInteractive", "-Command", "-"}) || string(input) != source {
		t.Fatalf("process=%q argv=%v input=%q", executable, argv, input)
	}
	for _, forbidden := range []string{"-File", "-EncodedCommand"} {
		if strings.Contains(strings.Join(argv, " "), forbidden) {
			t.Fatalf("argv contains %s: %v", forbidden, argv)
		}
	}
}

func TestRunScriptCapabilitySuiteSelectsInterpreterStdin(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	cfg.Shell.ServiceAccount.Enabled = true
	shell := NewShell(cfg.Shell)
	shell.Configure(cfg)
	shell.SetCredentialStore(presentShellCredential{})
	var calls []string
	shell.startServiceInput = func(executable string, argv []string, _ string, _ []string, _ config.ShellServiceAccount, _ []byte, input []byte, _ *lockedBuffer) (runningShellProcess, error) {
		calls = append(calls, executable+" "+strings.Join(argv, " ")+" :: "+string(input))
		return completedShellProcess{}, nil
	}
	tool := NewRunScript(shell)
	item := &session.Session{ID: "test", Workspace: root}
	for language, source := range map[string]string{"python": "print(6 * 7)", "node": "console.log(6 * 7)"} {
		if detail := tool.CallDetailed(context.Background(), item, map[string]any{"language": language, "source": source}); detail.Err != nil {
			t.Errorf("%s: %v", language, detail.Err)
		}
	}
	for _, call := range calls {
		if !strings.Contains(call, " - :: ") {
			t.Fatalf("interpreter did not receive stdin marker: %q", call)
		}
	}
}
