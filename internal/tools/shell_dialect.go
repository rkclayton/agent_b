package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"harness/internal/config"
)

// Item 2gb: the model writes `a && b`. Windows PowerShell 5.1 has no pipeline
// chain operators and answers with a parse error, which cost the v1.0.0 eval 17
// of its 20 tool errors. So the shell runs PowerShell 7 when the host has it,
// and rewrites the two chain operators at the top level when it does not.

// powerShellHost is the interpreter a configured PowerShell command resolves
// to: PowerShell 7 where the host has it, else the configured Windows
// PowerShell. Chains is true when the interpreter understands && and ||.
type powerShellHost struct {
	Executable string
	Version    string
	Chains     bool
}

// Dialect is the one sentence the tool descriptions and the host finding use.
func (h powerShellHost) Dialect() string {
	switch h.Version {
	case "7":
		return "PowerShell 7: `&&` and `||` work."
	case "5.1":
		return "Windows PowerShell 5.1: `&&` and `||` are rewritten to `; if ($?) { … }` at the top level, so they work; everything else is 5.1 syntax."
	default:
		return "Use " + shellDialect(h.Executable) + " syntax."
	}
}

// pwshCandidates are the standard install paths, tried after PATH.
func pwshCandidates() []string {
	paths := []string{}
	for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramW6432"), os.Getenv("ProgramFiles(x86)")} {
		if root != "" {
			paths = append(paths, filepath.Join(root, "PowerShell", "7", "pwsh.exe"))
		}
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		paths = append(paths, filepath.Join(local, "Microsoft", "WindowsApps", "pwsh.exe"))
	}
	return paths
}

// findPowerShell7 is the host lookup, done once per process.
func findPowerShell7() string {
	if path, err := exec.LookPath("pwsh"); err == nil {
		return path
	}
	for _, candidate := range pwshCandidates() {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	return ""
}

var powerShell7 = sync.OnceValue(findPowerShell7)

// resolveShellHost answers what a configured command actually runs as. A
// command that is not PowerShell is left exactly as the operator configured it.
func resolveShellHost(command []string) powerShellHost {
	if len(command) == 0 {
		return powerShellHost{Chains: true}
	}
	switch shellCommandName(command[0]) {
	case "powershell":
		if path := powerShell7(); path != "" {
			return powerShellHost{Executable: path, Version: "7", Chains: true}
		}
		return powerShellHost{Executable: command[0], Version: "5.1"}
	case "pwsh":
		return powerShellHost{Executable: command[0], Version: "7", Chains: true}
	default:
		return powerShellHost{Executable: command[0], Chains: true}
	}
}

func shellHostFor(cfg config.Shell) powerShellHost { return resolveShellHost(cfg.Command) }

// chainSplit finds a top-level && or || : not inside single or double quotes,
// not inside (), {} or [], and not escaped by a backtick. It returns the index
// and the operator, or -1.
func chainSplit(command string) (int, string) {
	var quote byte
	depth := 0
	for i := 0; i < len(command); i++ {
		c := command[i]
		if quote != 0 {
			if c == '`' && quote == '"' {
				i++
				continue
			}
			if c == quote {
				// '' inside a single-quoted string is an escaped quote.
				if c == '\'' && i+1 < len(command) && command[i+1] == '\'' {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		switch c {
		case '`':
			i++
		case '\'', '"':
			quote = c
		case '(', '{', '[':
			depth++
		case ')', '}', ']':
			if depth > 0 {
				depth--
			}
		case '&', '|':
			if depth == 0 && i+1 < len(command) && command[i+1] == c {
				return i, command[i : i+2]
			}
		}
	}
	return -1, ""
}

// rewriteChainOperators turns the model's chains into 5.1 statements, left to
// right: `a && b` becomes `a; if ($?) { b }` and `a || b` becomes
// `a; if (-not $?) { b }`. Quoted text and script blocks are untouched.
func rewriteChainOperators(command string) string {
	index, operator := chainSplit(command)
	if index < 0 {
		return command
	}
	left := strings.TrimRight(command[:index], " \t")
	right := strings.TrimLeft(command[index+2:], " \t")
	if left == "" || right == "" {
		return command
	}
	condition := "$?"
	if operator == "||" {
		condition = "-not $?"
	}
	return left + "; if (" + condition + ") { " + rewriteChainOperators(right) + " }"
}

// shellCommandForHost is what the interpreter is given: the model's command as
// written when the host understands chains, the rewrite when it does not.
func shellCommandForHost(host powerShellHost, command string) string {
	if host.Chains {
		return command
	}
	return rewriteChainOperators(command)
}

// PowerShell7ForEvidence is the resolved PowerShell 7 path, or "" when the host
// has none. Item 2gb's replay script reports which interpreter it ran.
func PowerShell7ForEvidence() string { return powerShell7() }

// ShellHostFinding is the capability probe's host finding (item 2gb): which
// shell actually backs the shell tool on this host, and whether the model's
// chain operators run natively or through the rewrite.
func ShellHostFinding(cfg config.Shell) string {
	host := shellHostFor(cfg)
	switch host.Version {
	case "7":
		return "shell: PowerShell 7 (" + host.Executable + "); && and || run natively"
	case "5.1":
		return "shell: Windows PowerShell 5.1 (" + host.Executable + "); && and || rewritten at the top level"
	default:
		return "shell: " + shellDialect(host.Executable) + " (" + host.Executable + ")"
	}
}
