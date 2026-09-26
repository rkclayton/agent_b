//go:build windows

package updater

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func verifySetupSignature(ctx context.Context, path string) error {
	return readVerification(inspectAuthenticode(ctx, path))
}

// inspectAuthenticode asks Windows PowerShell what it thinks of the file and
// takes the answer from a report file, not from the console. Item 2lb: the
// console is where banners, warnings and module-load errors go, and the updater
// must be able to tell "the signature is bad" from "I could not look".
func inspectAuthenticode(ctx context.Context, path string) inspection {
	report, err := os.CreateTemp("", "agentb-authenticode-*.json")
	if err != nil {
		return inspection{Err: err}
	}
	reportPath := report.Name()
	_ = report.Close()
	defer func() { _ = os.Remove(reportPath) }()

	powershell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	script := authenticodeScript(path, reportPath)
	command := exec.CommandContext(ctx, powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
	command.Env = windowsPowerShellEnvironment(os.Environ(), powershell)
	var console bytes.Buffer
	command.Stdout = &console
	command.Stderr = &console
	runErr := command.Run()
	contents, readErr := os.ReadFile(reportPath)
	if readErr != nil {
		contents = nil
	}
	return inspection{Report: contents, Output: console.String(), Err: runErr}
}

// The script emits nothing of its own: progress, warnings and verbose records
// are silenced, and the only thing it produces is the report file. A failure to
// inspect at all is reported as `unavailable` in that same file, so the updater
// still gets a structured answer instead of console text.
func authenticodeScript(path, reportPath string) string {
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	return strings.Join([]string{
		"$ProgressPreference='SilentlyContinue'",
		"$WarningPreference='SilentlyContinue'",
		"$VerbosePreference='SilentlyContinue'",
		"$InformationPreference='SilentlyContinue'",
		"$ErrorActionPreference='Stop'",
		"$report=" + quote(reportPath),
		"try{",
		"$s=Get-AuthenticodeSignature -LiteralPath " + quote(path),
		"$result=[pscustomobject]@{status=[string]$s.Status;status_message=[string]$s.StatusMessage;signer=[bool]$s.SignerCertificate;timestamped=[bool]$s.TimeStamperCertificate}",
		"}catch{",
		"$result=[pscustomobject]@{unavailable=[string]$_.Exception.Message}",
		"}",
		"[IO.File]::WriteAllText($report,($result|ConvertTo-Json -Compress),[Text.UTF8Encoding]::new($false))",
	}, "; ")
}

// Windows PowerShell can inherit a PSModulePath headed by PowerShell 7's
// incompatible built-in modules, which makes its own core modules --
// Microsoft.PowerShell.Security, where Get-AuthenticodeSignature lives -- fail to
// autoload. internal/hardening solved this for its own children; the updater
// needs the same guarantee, and this is the environment the operator's failing
// install ran in.
func windowsPowerShellEnvironment(environment []string, powershell string) []string {
	systemModules := filepath.Clean(filepath.Join(filepath.Dir(powershell), "Modules"))
	result := append([]string(nil), environment...)
	found := false
	for index, entry := range result {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.EqualFold(key, "PSModulePath") {
			continue
		}
		found = true
		paths := []string{systemModules}
		for _, candidate := range filepath.SplitList(value) {
			if candidate != "" && !strings.EqualFold(filepath.Clean(candidate), systemModules) {
				paths = append(paths, candidate)
			}
		}
		result[index] = key + "=" + strings.Join(paths, string(os.PathListSeparator))
	}
	if !found {
		result = append(result, "PSModulePath="+systemModules)
	}
	return result
}
