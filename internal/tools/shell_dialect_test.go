package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/session"
)

// Item 2gb, v1.0.1/W1. The model writes `a && b`; Windows PowerShell 5.1 has no
// chain operators. The shell runs PowerShell 7 where the host has one, and
// rewrites the two operators at the top level where it does not.
func TestTheConfiguredPowerShellResolvesToSeven(t *testing.T) {
	for _, row := range []struct {
		name     string
		command  []string
		version  string
		chains   bool
		contains string
	}{
		{"the default resolves by the host", []string{"powershell", "-NoProfile", "-Command"}, "", false, ""},
		{"an explicit pwsh keeps its own path", []string{`C:\Program Files\PowerShell\7\pwsh.exe`, "-Command"}, "7", true, "`&&` and `||` work"},
		{"cmd is left exactly as configured", []string{`C:\Windows\System32\cmd.exe`, "/c"}, "", true, "cmd.exe"},
		{"an empty command chains", nil, "", true, ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			host := resolveShellHost(row.command)
			if row.version == "" && row.name == "the default resolves by the host" {
				// Whichever the host has, the description must name it and a
				// 5.1 host must not claim chains.
				if host.Version != "7" && host.Version != "5.1" {
					t.Fatalf("the default resolved to %+v", host)
				}
				if host.Chains != (host.Version == "7") {
					t.Fatalf("chains %t for version %s", host.Chains, host.Version)
				}
				return
			}
			if host.Version != row.version || host.Chains != row.chains {
				t.Fatalf("got %+v, want version %q chains %t", host, row.version, row.chains)
			}
			if row.contains != "" && !strings.Contains(host.Dialect(), row.contains) {
				t.Fatalf("dialect %q does not name %q", host.Dialect(), row.contains)
			}
		})
	}
}

func TestTheChainRewriteTouchesOnlyTopLevelOperators(t *testing.T) {
	for _, row := range []struct{ command, want string }{
		{`cd x && dir`, `cd x; if ($?) { dir }`},
		{`a || b`, `a; if (-not $?) { b }`},
		{`a && b && c`, `a; if ($?) { b; if ($?) { c } }`},
		{`a && b || c`, `a; if ($?) { b; if (-not $?) { c } }`},
		{`git commit -m "fix a && b"`, `git commit -m "fix a && b"`},
		{`echo 'a && b'`, `echo 'a && b'`},
		{`if ($true) { a && b }`, `if ($true) { a && b }`},
		{`Get-Item x | ForEach-Object { $_ && $_ }`, `Get-Item x | ForEach-Object { $_ && $_ }`},
		{`a & b`, `a & b`},
		{`a | b`, `a | b`},
		{`echo "quoted ` + "`" + `" && still quoted"`, `echo "quoted ` + "`" + `" && still quoted"`},
		{`&& b`, `&& b`},
		{`a &&`, `a &&`},
		{`cd x && dir "a && b"`, `cd x; if ($?) { dir "a && b" }`},
		{`C:\Go\bin\go.exe test ./logic && echo done`, `C:\Go\bin\go.exe test ./logic; if ($?) { echo done }`},
	} {
		if got := rewriteChainOperators(row.command); got != row.want {
			t.Fatalf("rewrite(%q) = %q, want %q", row.command, got, row.want)
		}
	}
}

func TestAHostWithChainsIsGivenTheCommandAsWritten(t *testing.T) {
	native := powerShellHost{Executable: "pwsh.exe", Version: "7", Chains: true}
	if got := shellCommandForHost(native, `cd x && dir`); got != `cd x && dir` {
		t.Fatalf("PowerShell 7 was given %q", got)
	}
	old := powerShellHost{Executable: "powershell.exe", Version: "5.1"}
	if got := shellCommandForHost(old, `cd x && dir`); got != `cd x; if ($?) { dir }` {
		t.Fatalf("PowerShell 5.1 was given %q", got)
	}
}

func TestTheShellToolDescriptionNamesTheDialect(t *testing.T) {
	workspace := t.TempDir()
	cfg := config.Defaults(workspace)
	shell := NewShell(cfg.Shell)
	shell.Configure(cfg)
	description := shell.Description()
	if !strings.Contains(description, "PowerShell 7") && !strings.Contains(description, "PowerShell 5.1") {
		t.Fatalf("the description names no dialect: %q", description)
	}
	if strings.Contains(description, "not `&&`") {
		t.Fatalf("the description still tells the model to avoid &&: %q", description)
	}
	if !strings.Contains(NewRunScript(shell).Description(), "PowerShell") {
		t.Fatal("run_script names no dialect")
	}
}

// The v1.0.0 eval's seventeen `&&` failures, replayed through the tool against
// the real interpreter. Success is that the chain operator no longer ends the
// call: Windows PowerShell 5.1's parse error is gone. What the commands
// themselves then do (a missing path, a failing test) is the shell's own
// ordinary result, which the model reads.
func TestTheSeventeenChainTapesNoLongerDieOnTheOperator(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode runs no interpreter")
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "logs", "evidence", "2026-09-20-v1.0.1", "w1-ampersand-tapes.json"))
	if err != nil {
		t.Skipf("the tapes are not in this tree: %v", err)
	}
	var tapes []struct{ Task, Command string }
	if err := json.Unmarshal(raw, &tapes); err != nil {
		t.Fatal(err)
	}
	if len(tapes) != 17 {
		t.Fatalf("expected the seventeen tapes, found %d", len(tapes))
	}
	hosts := map[string][]string{"5.1": {filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoProfile", "-NonInteractive", "-Command"}}
	if seven := powerShell7(); seven != "" {
		hosts["7"] = []string{seven, "-NoProfile", "-NonInteractive", "-Command"}
	} else {
		t.Log("this host has no PowerShell 7; only the rewrite path is proved here")
	}
	for version, command := range hosts {
		workspace := t.TempDir()
		cfg := config.Defaults(workspace)
		cfg.Shell.ServiceAccount.Enabled = false
		cfg.Shell.Command = command
		cfg.Shell.TimeoutS = 30
		guard := false
		cfg.Shell.FileRoutingGuard = &guard
		shell := NewShell(cfg.Shell)
		shell.Configure(cfg)
		s := &session.Session{ID: "tapes", Workspace: workspace, Run: session.RunState{Status: "running"}}
		for _, tape := range tapes {
			detail := shell.CallDetailed(t.Context(), s, map[string]any{"command": tape.Command})
			answer := detail.Content
			if detail.Err != nil {
				answer += " " + detail.Err.Error()
			}
			if strings.Contains(answer, "not a valid statement separator") {
				t.Errorf("PowerShell %s still refuses the operator in %q: %s", version, tape.Command, answer)
			}
			if detail.OperatorOverrideReason != "" {
				t.Errorf("PowerShell %s raised a card for %q: %s", version, tape.Command, detail.OperatorOverrideReason)
			}
		}
	}
}

func TestTheHostFindingNamesTheShellBehindTheTool(t *testing.T) {
	cfg := config.Defaults(t.TempDir())
	finding := ShellHostFinding(cfg.Shell)
	if !strings.HasPrefix(finding, "shell: ") {
		t.Fatalf("finding %q", finding)
	}
	if powerShell7() != "" {
		if !strings.Contains(finding, "PowerShell 7") || !strings.Contains(finding, "natively") {
			t.Fatalf("a host with PowerShell 7 reported %q", finding)
		}
		return
	}
	if !strings.Contains(finding, "5.1") || !strings.Contains(finding, "rewritten") {
		t.Fatalf("a host without PowerShell 7 reported %q", finding)
	}
}

// v1.0.1/W4 cold review: the scanner has to see what PowerShell sees, or the
// rewrite corrupts literal text. Where it cannot be sure, it leaves the
// command exactly as the model wrote it.
func TestTheRewriteLeavesHereStringsCommentsAndStopParsingAlone(t *testing.T) {
	hereString := "Write-Output @'\nA does not 't work && Set-Content pwned.txt x\n'@"
	for _, row := range []struct{ name, command, want string }{
		{"an apostrophe in a here-string body is literal", hereString, hereString},
		{"a real chain after a here-string still rewrites", "Set-Content f.txt @'\nit's here\n'@\nGet-Content f.txt && Write-Output ok", "Set-Content f.txt @'\nit's here\n'@\nGet-Content f.txt; if ($?) { Write-Output ok }"},
		{"a double-quoted here-string is literal too", "Write-Output @\"\nit's $x && whoami\n\"@", "Write-Output @\"\nit's $x && whoami\n\"@"},
		{"stop-parsing passes everything after it to the native command", `cmd --% /c echo a && echo b`, `cmd --% /c echo a && echo b`},
		{"a chain before stop-parsing still rewrites", `dir && cmd --% /c echo a && echo b`, `dir; if ($?) { cmd --% /c echo a && echo b }`},
		{"a line comment hides what follows", `ls # note && ls2`, `ls # note && ls2`},
		{"a block comment is skipped", `ls <# a && b #> && ls2`, `ls <# a && b #>; if ($?) { ls2 }`},
		{"an unbalanced parenthesis is not understood", `echo :-( && ls`, `echo :-( && ls`},
		{"an unbalanced quote is not understood", `echo "open && ls`, `echo "open && ls`},
		{"an unterminated here-string is not understood", "Write-Output @'\nnever closed && ls", "Write-Output @'\nnever closed && ls"},
		{"a doubled quote inside a string is an escape", `echo "say ""hi"" && bye"`, `echo "say ""hi"" && bye"`},
	} {
		t.Run(row.name, func(t *testing.T) {
			if got := rewriteChainOperators(row.command); got != row.want {
				t.Fatalf("rewrite(%q)\n got %q\nwant %q", row.command, got, row.want)
			}
		})
	}
}

func TestPowerShellSevenIsTakenOnlyFromAMachineWideInstall(t *testing.T) {
	for _, candidate := range pwshCandidates() {
		lower := strings.ToLower(candidate)
		if strings.Contains(lower, "localappdata") || strings.Contains(lower, "windowsapps") || strings.Contains(lower, "appdata") {
			t.Fatalf("candidate %q is under a per-user path", candidate)
		}
		if !strings.HasSuffix(lower, `\powershell\7\pwsh.exe`) {
			t.Fatalf("candidate %q is not the machine-wide install path", candidate)
		}
	}
}
