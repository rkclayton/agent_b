package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/session"
)

// Item 2fi, the walk's step 7: with no service identity, run_script and shell
// cannot read a file outside the folder around the card — a literal outside
// path raises the same operator decision read_file raises, and holds.
func TestOutsideReadsInScriptsAndCommandsRaiseTheCardWithNoServiceIdentity(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	cfg := config.Defaults(root)
	cfg.Shell.ServiceAccount.Enabled = false
	shell := NewShell(cfg.Shell)
	shell.Configure(cfg)
	item := &session.Session{ID: "walk", Workspace: root, PlanRepos: func() []string { return []string{repo} }}
	script := NewRunScript(shell)
	for _, source := range []string{
		`[System.IO.File]::ReadAllLines("C:\Windows\win.ini")[0..39] -join "` + "`" + `n"`,
		`print(open(r'C:\Windows\win.ini').read())`,
		`Get-Content \\fileserver\share\notes.txt`,
	} {
		detail := script.CallDetailed(context.Background(), item, map[string]any{"language": "powershell", "source": source})
		if !strings.Contains(detail.OperatorOverrideReason, "outside the folder") || !strings.HasPrefix(detail.Content, "script was not started") {
			t.Errorf("a script naming an outside path must raise the card: %q → %+v", source, detail)
		}
	}
	detail := shell.CallDetailed(context.Background(), item, map[string]any{"command": `[System.IO.File]::ReadAllText('C:\Windows\win.ini')`})
	if !strings.Contains(detail.OperatorOverrideReason, `C:\Windows\win.ini`) {
		t.Fatalf("a shell command naming an outside path must raise the card: %+v", detail)
	}
	for _, inside := range []string{
		filepath.Join(root, "data.csv"),
		filepath.Join(repo, "src", "main.go"),
		`C:\Program Files\Git\cmd\git.exe`,
	} {
		if paths := outsideLiteralPaths(`Get-Content "`+inside+`"`, item); len(paths) != 0 {
			t.Errorf("%s is inside the chat's roots or an executable, yet was flagged: %v", inside, paths)
		}
	}
}

// With the service identity on, the OS decides; the literal-path card is not
// raised (behaviour unchanged).
func TestOutsidePathCardIsNotRaisedWithTheServiceIdentityOn(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	cfg.Shell.ServiceAccount.Enabled = true
	shell := NewShell(cfg.Shell)
	shell.Configure(cfg)
	shell.SetCredentialStore(presentShellCredential{})
	started := false
	shell.startServiceInput = func(string, []string, string, []string, config.ShellServiceAccount, []byte, []byte, *lockedBuffer) (runningShellProcess, error) {
		started = true
		return completedShellProcess{}, nil
	}
	detail := NewRunScript(shell).CallDetailed(context.Background(), &session.Session{ID: "svc", Workspace: root}, map[string]any{"language": "powershell", "source": `[System.IO.File]::ReadAllLines("C:\Windows\win.ini")`})
	if detail.OperatorOverrideReason != "" || !started {
		t.Fatalf("with the service identity on the script runs under that identity: %+v started=%t", detail, started)
	}
}

// v0.69.0/W12 cold review: a climb written as an absolute path, a text file
// dressed as an executable, a .bat file's text, and a fourth path behind three
// all reach the card; a real executable's path still does not.
func TestOutsidePathsSeeClimbsFakeExecutablesAndEveryPath(t *testing.T) {
	root := t.TempDir()
	item := &session.Session{ID: "review", Workspace: root}
	for _, source := range []string{
		`Get-ChildItem ` + root + `\..`,
		`Get-Content ` + root + `\x\..\..\..`,
		`Get-Content 'C:\Windows\win.ini .exe'`,
		`open('C:/Users/someone/.ssh/id_rsa#.exe'.split('#')[0])`,
		`Get-Content C:\Users\someone\secrets.bat`,
	} {
		if reason := outsideReadReason(source, item); reason == "" {
			t.Errorf("no card for %q", source)
		}
	}
	if reason := outsideReadReason(`& C:\Windows\System32\where.exe notepad`, item); reason != "" {
		t.Errorf("a real executable's path is not a read: %q", reason)
	}
	reason := outsideReadReason(`Get-Content C:\a C:\b C:\c C:\Users\someone\.ssh\id_rsa`, item)
	if !strings.Contains(reason, `id_rsa`) {
		t.Fatalf("the fourth path hid behind three: %q", reason)
	}
}
