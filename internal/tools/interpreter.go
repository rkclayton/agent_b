package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Item 2la: an interpreter is resolved to a real executable, never launched by
// bare name and never through something that answers by opening a window.
//
// On the operator's own host `python` resolves, in PATH order, to three things:
// a ZERO-BYTE `C:\Windows\System32\python` with no extension, the real
// `…\Programs\Python\Python312\python.exe`, and an App Execution Alias in
// `…\Microsoft\WindowsApps` that is a stub for AppInstallerPythonRedirector.exe.
// The stub opens "how do you want to open this file?" on his desktop and waits,
// so the tool call returned neither success nor a clean failure; the zero-byte
// entry is the other face of the same problem. The operator's desktop is not part
// of the agent's execution environment.
const appExecutionAliasFolder = "windowsapps"

// usableInterpreter returns a resolved interpreter path, or a reason naming what
// is missing and what to do about it. A language with no usable interpreter is
// refused BEFORE launch rather than attempted.
func usableInterpreter(name string) (string, string) {
	candidates := interpreterCandidates(name)
	for _, candidate := range candidates {
		if reason := unusableInterpreter(candidate); reason == "" {
			return candidate, ""
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Sprintf("%s is not installed on this host, or is not on PATH. Install it and restart Agent_b, or use powershell.", name)
	}
	return "", fmt.Sprintf("no usable %s interpreter: %s. Install %s properly (a Microsoft Store alias stub cannot be used, because it opens a dialog on the desktop) and restart Agent_b.", name, unusableInterpreter(candidates[0]), name)
}

// interpreterCandidates is every place PATH offers this name, best first.
func interpreterCandidates(name string) []string {
	seen := map[string]bool{}
	candidates := make([]string, 0, 4)
	add := func(path string) {
		if path == "" || seen[strings.ToLower(path)] {
			return
		}
		seen[strings.ToLower(path)] = true
		candidates = append(candidates, path)
	}
	if path, err := exec.LookPath(name); err == nil {
		add(path)
	}
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		if directory == "" {
			continue
		}
		for _, extension := range interpreterExtensions() {
			full := filepath.Join(directory, name+extension)
			if info, err := os.Stat(full); err == nil && !info.IsDir() {
				add(full)
			}
		}
	}
	return candidates
}

// unusableInterpreter says why this path cannot be launched, or "" when it can.
func unusableInterpreter(path string) string {
	info, err := os.Stat(path)
	switch {
	case err != nil:
		return fmt.Sprintf("%s cannot be read (%v)", path, err)
	case info.IsDir():
		return fmt.Sprintf("%s is a directory", path)
	case info.Size() == 0:
		// The zero-byte C:\Windows\System32\python on the operator's host.
		return fmt.Sprintf("%s is an empty file, not an interpreter", path)
	case isAppExecutionAlias(path):
		return fmt.Sprintf("%s is a Microsoft Store app-execution alias, which opens a dialog on the desktop instead of running", path)
	}
	return ""
}

func isAppExecutionAlias(path string) bool {
	for _, part := range strings.Split(strings.ToLower(filepath.ToSlash(path)), "/") {
		if part == appExecutionAliasFolder {
			return true
		}
	}
	return false
}
