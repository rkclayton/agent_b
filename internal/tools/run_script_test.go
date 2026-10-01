package tools

import (
	"context"
	"reflect"
	"runtime"
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
	// Item 2gb (v1.0.1): powershell source runs in the interpreter the shell
	// tool resolves — PowerShell 7 where the host has one — with the same
	// stdin contract.
	wantArgv := []string{"-NoProfile", "-NonInteractive", "-Command", `$source = [Console]::In.ReadToEnd(); & ([scriptblock]::Create($source))`}
	if executable != shellHostFor(cfg.Shell).Executable || !reflect.DeepEqual(argv, wantArgv) || string(input) != source {
		t.Fatalf("process=%q argv=%v input=%q", executable, argv, input)
	}
	for _, forbidden := range []string{"-File", "-EncodedCommand"} {
		if strings.Contains(strings.Join(argv, " "), forbidden) {
			t.Fatalf("argv contains %s: %v", forbidden, argv)
		}
	}
}

func TestRunScriptPowerShellRunsWholeMultilineSource2pa(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell contract is Windows-specific")
	}
	root := t.TempDir()
	cfg := config.Defaults(root)
	cfg.Shell.OperatorContext = true
	shell := NewShell(cfg.Shell)
	shell.Configure(cfg)
	tool := NewRunScript(shell)
	item := &session.Session{ID: "test", Workspace: root}
	fixtures := []struct {
		name, source, want string
	}{
		{"foreach block", "$sum = 0\nforeach ($n in 1..3) {\n  $sum += $n\n}\nWrite-Output \"sum=$sum\"", "sum=6"},
		{"array expression", "$values = @(\n  'north'\n  'south'\n)\n$values | ForEach-Object { Write-Output $_ }", "north\nsouth"},
		{"hashtable", "$value = @{\n  bird = 'heron'\n  count = 2\n}\nWrite-Output \"$($value.bird):$($value.count)\"", "heron:2"},
		{"here string", "$text = @'\nfirst\nsecond\n'@\nWrite-Output $text", "first\nsecond"},
		{"continued pipeline", "1..3 |\n  ForEach-Object { $_ * 2 } |\n  ForEach-Object { Write-Output \"n=$_\" }", "n=2\nn=4\nn=6"},
		{"function then call", "function Get-FixtureValue {\n  return 'called'\n}\nWrite-Output (Get-FixtureValue)", "called"},
		{"statement after block", "if ($true) {\n  Write-Output 'inside'\n}\nWrite-Output 'after'", "inside\nafter"},
		{"empty result", "$value = 42", ""},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			detail := tool.CallDetailed(context.Background(), item, map[string]any{"language": "powershell", "source": fixture.source})
			if detail.Err != nil {
				t.Fatal(detail.Err)
			}
			want := "exit=0\n" + fixture.want
			if fixture.want == "" {
				want = "exit=0, no output"
			}
			if got := strings.ReplaceAll(detail.Content, "\r\n", "\n"); got != want {
				t.Fatalf("result=%q, want %q", got, want)
			}
		})
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
