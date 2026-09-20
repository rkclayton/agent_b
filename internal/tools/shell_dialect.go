package tools

import (
	"os"
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

// pwshCandidates are the machine-wide install paths, and only those. The
// v1.0.1/W4 cold review: resolving through PATH or %LOCALAPPDATA%\Microsoft\
// WindowsApps would let anything a non-admin can write become the interpreter
// every shell call runs — including the operator-identity path — which is the
// class operatorOnlyInterpreter already refuses for python and node. A host
// whose pwsh lives elsewhere keeps Windows PowerShell and the rewrite.
func pwshCandidates() []string {
	paths := []string{}
	for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramW6432"), os.Getenv("ProgramFiles(x86)")} {
		if root != "" {
			paths = append(paths, filepath.Join(root, "PowerShell", "7", "pwsh.exe"))
		}
	}
	return paths
}

// findPowerShell7 is the host lookup, done once per process.
func findPowerShell7() string {
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
// a here-string, a comment, a subexpression or a script block, and not escaped
// by a backtick. It returns the index and the operator, or -1. The second
// result of scanned() says whether the scan ended cleanly; a command whose
// quotes or brackets do not balance is not understood, and is never rewritten
// (v1.0.1/W4 cold review).
func chainSplit(command string) (int, string) {
	index, operator, balanced := scanChain(command)
	if !balanced {
		return -1, ""
	}
	return index, operator
}

func scanChain(command string) (int, string, bool) {
	var quote byte
	depth, found, operator := 0, -1, ""
	for i := 0; i < len(command); i++ {
		c := command[i]
		if quote != 0 {
			if c == '`' && quote == '"' {
				i++
				continue
			}
			if c == quote {
				// '' and "" inside a string of that quote are escapes.
				if i+1 < len(command) && command[i+1] == c {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		// A here-string runs to a terminator at the start of a line; quotes
		// and operators inside its body are literal text.
		if c == '@' && i+1 < len(command) && (command[i+1] == '\'' || command[i+1] == '"') {
			end := hereStringEnd(command, i)
			if end < 0 {
				return -1, "", false
			}
			i = end
			continue
		}
		// Comments run to the end of the line; <# … #> to its close.
		if c == '#' {
			if i > 0 && command[i-1] == '<' {
				close := strings.Index(command[i:], "#>")
				if close < 0 {
					return -1, "", false
				}
				i += close + 1
				continue
			}
			newline := strings.IndexAny(command[i:], "\r\n")
			if newline < 0 {
				break
			}
			i += newline
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
			depth--
			if depth < 0 {
				return -1, "", false
			}
		case '-':
			// --% stops PowerShell parsing: everything after it is passed to
			// the native command verbatim, so nothing after it is an operator.
			if strings.HasPrefix(command[i:], "--%") && (i == 0 || command[i-1] == ' ' || command[i-1] == '\t') {
				return found, operator, found >= 0 && depth == 0
			}
		case '&', '|':
			if depth == 0 && found < 0 && i+1 < len(command) && command[i+1] == c {
				found, operator = i, command[i:i+2]
			}
		}
	}
	return found, operator, quote == 0 && depth == 0
}

// hereStringEnd is the index of the last character of the here-string opening
// at start, or -1 when it never closes.
func hereStringEnd(command string, start int) int {
	terminator := "\n'@"
	if command[start+1] == '"' {
		terminator = "\n\"@"
	}
	at := strings.Index(command[start+2:], terminator)
	if at < 0 {
		return -1
	}
	return start + 2 + at + len(terminator) - 1
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
