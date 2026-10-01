package tools

import (
	"context"
	"os"
	"os/exec"
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
		// Item 2fz: an existing outside read raises the card; a missing one is a
		// plain error. Either way the script does not run around the card.
		carded := strings.Contains(detail.OperatorOverrideReason, "outside the folder") && strings.HasPrefix(detail.Content, "script was not started")
		refused := detail.Err != nil && strings.Contains(detail.Err.Error(), "no such file or directory outside the folder")
		if !carded && !refused {
			t.Errorf("a script naming an outside path must raise the card or be refused: %q → %+v", source, detail)
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

func TestSkillFolderWritesAreNeverSilent2pe(t *testing.T) {
	workspace, profile := t.TempDir(), t.TempDir()
	skillsRoot := filepath.Join(profile, "skills")
	target := filepath.Join(skillsRoot, "fixture", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	item := &session.Session{ID: "skill-fence", Role: "b", Workspace: workspace, SkillsRoot: skillsRoot}
	cfg := config.Defaults(workspace)
	cfg.Shell.ServiceAccount.Enabled = false
	shell := NewShell(cfg.Shell)
	shell.Configure(cfg)
	check := func(name string, detail CallDetail) {
		t.Helper()
		text := detail.Content
		if detail.Err != nil {
			text += detail.Err.Error()
		}
		if !strings.Contains(strings.ToLower(text+" "+detail.OperatorOverrideReason), strings.ToLower(skillsRoot)) {
			t.Errorf("%s silently reached skills: %+v", name, detail)
		}
	}
	_, writeErr := NewWriteFile(nil).Call(context.Background(), item, map[string]any{"path": target, "content": "changed"})
	check("write_file", CallDetail{Err: writeErr})
	_, editErr := NewEditFile(nil).Call(context.Background(), item, map[string]any{"path": target, "old_string": "original", "new_string": "changed"})
	check("edit_file", CallDetail{Err: editErr})
	check("shell", shell.CallDetailed(context.Background(), item, map[string]any{"command": `Set-Content -LiteralPath "` + target + `" -Value changed`}))
	check("run_script", NewRunScript(shell).CallDetailed(context.Background(), item, map[string]any{"language": "powershell", "source": `Set-Content -LiteralPath "` + target + `" -Value changed`}))
	if data, _ := os.ReadFile(target); string(data) != "original" {
		t.Fatalf("skills file changed to %q", data)
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

// Item 2fz, v1.0.0/W1: the routing guard classifies each literal outside path
// by its statement. Directory changes and listings run; an existing outside
// read raises the card; a missing one is a plain error; a change of directory
// followed by a read in the same command is a read.
func TestTheRoutingGuardInterceptsReadsNotDirectoryChanges(t *testing.T) {
	root := t.TempDir()
	item := &session.Session{ID: "2fz", Workspace: root}
	for _, c := range []struct {
		source, want string
	}{
		{`cd /d C:\`, "run"},
		{`cd /d C:\ && dir`, "run"},
		{`Set-Location C:\Windows; Get-ChildItem`, "run"},
		{`Get-ChildItem -Path C:\Windows`, "run"},
		{`dir C:\Windows\System32`, "run"},
		{`pushd C:\Windows`, "run"},
		{`cd C:\Users\someone\Documents\GitHub\eval-20260310-110411-1120 && C:\Go\bin\go.exe test ./logic -run ^TestTouchedPlanRoots$ -v`, "run"},
		{`cd /d "C:\work\acme" 2>nul; C:\Go\bin\go.exe test ./logic -run ^TestRetentionPreservesNestedEvidence$ -v`, "run"},
		{`type C:\Windows\win.ini`, "card"},
		{`Get-Content -Path C:\Windows\win.ini`, "card"},
		{`'C:\Windows\win.ini' | Get-Content`, "card"},
		{`cd C:\Windows; type win.ini`, "card"},
		{`Set-Location C:\Windows; Get-Content .\win.ini`, "card"},
		{`[System.IO.File]::ReadAllText('C:\Windows\win.ini')`, "card"},
		// v1.0.0/W4 cold review: cmd's bare & joins a read to a cd, and
		// relative reads after a cd through verbs the first list missed.
		{`cd /d C:\ & type C:\Windows\win.ini`, "card"},
		{`cd /d C:\Windows & more win.ini`, "card"},
		{`Set-Location C:\Windows; certutil -encode win.ini CON`, "card"},
		{`cd C:\Windows; robocopy . C:\elsewhere win.ini`, "card"},
		{`cd C:\Windows; [System.IO.StreamReader]::new('win.ini').ReadToEnd()`, "card"},
		{`cd C:\Windows; iex (Get-Item win.ini)`, "card"},
		{`pushd C:\Windows && cmd /c type win.ini`, "card"},
		{`type C:\nope-2fz\missing.txt`, "missing"},
		{`Get-Content C:\nope-2fz\missing.txt`, "missing"},
	} {
		decision := outsideCommandDecision(c.source, item, nil)
		got := "run"
		if decision.card != "" {
			got = "card"
		} else if decision.missing != "" {
			got = "missing"
		}
		if got != c.want {
			t.Errorf("%q: %s, want %s (%+v)", c.source, got, c.want, decision)
		}
	}
}

func TestTrustedFoldersUseResolvedPaths(t *testing.T) {
	workspace, trusted := t.TempDir(), t.TempDir()
	file := filepath.Join(trusted, "child", "note.txt")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("trusted"), 0600); err != nil {
		t.Fatal(err)
	}
	item := &session.Session{Workspace: workspace}
	entries := []config.TrustedFolder{{Path: trusted}}
	if decision := outsideCommandDecision(`Get-Content "`+file+`"`, item, entries); decision.card != "" || !decision.trusted {
		t.Fatalf("trusted descendant asked: %+v", decision)
	}
	other := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(other, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	if decision := outsideCommandDecision(`Get-Content "`+other+`"`, item, entries); decision.card == "" {
		t.Fatalf("sibling folder did not ask: %+v", decision)
	}
	link := filepath.Join(trusted, "escape")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, filepath.Dir(other)).CombinedOutput(); err != nil {
		t.Fatalf("junction: %v %s", err, out)
	}
	if decision := outsideCommandDecision(`Get-Content "`+filepath.Join(link, filepath.Base(other))+`"`, item, entries); decision.card == "" {
		t.Fatalf("junction escaping the trusted folder did not ask: %+v", decision)
	}
}
