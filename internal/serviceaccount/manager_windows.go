//go:build windows

package serviceaccount

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
)

const statusMarker = "AGENTB_ACCOUNT_STATUS="

type windowsManager struct {
	scriptPath string
	powershell string
	// Item 2kk (a) and (d): where the child writes its result, and where the
	// launcher's streams are kept. Both live beside the scripts unless a data
	// root is set, which is what the running product does.
	workDir string
	// Item 2kk (f): the seam the fixtures use. Production elevates; a fixture
	// runs its child directly, because a test must never reach
	// Start-Process -Verb RunAs -- that raises a Windows credential prompt on the
	// operator own desktop, which is a hard stop, not a test.
	runLauncher func(context.Context, []string) ([]byte, error)
}

func New(scriptPath string) Manager {
	powershell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if _, err := os.Stat(powershell); err != nil {
		powershell = "powershell.exe"
	}
	manager := &windowsManager{scriptPath: scriptPath, powershell: powershell, workDir: os.TempDir()}
	manager.runLauncher = manager.elevate
	return manager
}

func (m *windowsManager) Status(ctx context.Context, account string) (Status, error) {
	output, err := exec.CommandContext(ctx, m.powershell,
		"-NoLogo", "-NoProfile", "-NonInteractive",
		"-File", m.scriptPath, "-Inspect", "-AccountName", account,
	).CombinedOutput()
	if err != nil {
		return Status{}, fmt.Errorf("inspect local service account: %s", safePowerShellError(output, err))
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n") {
		if !strings.HasPrefix(line, statusMarker) {
			continue
		}
		var status Status
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, statusMarker)), &status); err != nil {
			return Status{}, fmt.Errorf("decode local service-account status: %w", err)
		}
		return status, nil
	}
	return Status{}, fmt.Errorf("inspect local service account: status was not returned")
}

func (m *windowsManager) Setup(ctx context.Context, account, credentialPath string, reset bool, protection *Protection) (SetupResult, error) {
	scriptPath, err := filepath.Abs(m.scriptPath)
	if err != nil {
		return SetupResult{}, fmt.Errorf("resolve service-account setup script: %w", err)
	}
	credentialPath, err = filepath.Abs(credentialPath)
	if err != nil {
		return SetupResult{}, fmt.Errorf("resolve service-account credential store: %w", err)
	}
	if _, err := os.Stat(scriptPath); err != nil {
		return SetupResult{}, fmt.Errorf("service-account setup script is unavailable: %w", err)
	}
	if _, err := os.Stat(credentialPath); err != nil {
		return SetupResult{}, fmt.Errorf("service-account credential store is unavailable: %w", err)
	}
	arguments := []string{
		"-NoLogo", "-NoProfile", "-NonInteractive",
		"-File", scriptPath, "-AccountName", account, "-CredentialStore", credentialPath,
		"-NoPrompt",
	}
	if protection != nil {
		scriptPath = filepath.Join(filepath.Dir(scriptPath), "provision-service-identity.ps1")
		if _, err := os.Stat(scriptPath); err != nil {
			return SetupResult{}, fmt.Errorf("service-identity provisioning script is unavailable: %w", err)
		}
		arguments = []string{
			"-NoLogo", "-NoProfile", "-NonInteractive", "-File", scriptPath,
			"-AccountName", account, "-CredentialStore", credentialPath,
			"-ApplicationDirectory", protection.ApplicationDirectory,
			"-DataDirectory", protection.DataDirectory,
			"-WorkspaceDirectory", protection.WorkspaceDirectory,
			"-ExchangeDirectory", protection.ExchangeDirectory,
			"-ModelAddress", protection.ModelAddress,
			"-ModelPort", fmt.Sprint(protection.ModelPort),
		}
		if protection.AllowLocalNetwork {
			arguments = append(arguments, "-AllowLocalNetwork")
		}
		if len(protection.LocalSubnets) > 0 {
			arguments = append(arguments, "-LocalSubnet", strings.Join(protection.LocalSubnets, ","))
		}
		if len(protection.AllowedModelRanges) > 0 {
			arguments = append(arguments, "-AllowedRange", strings.Join(protection.AllowedModelRanges, ","))
		}
	}
	if reset {
		arguments = append(arguments, "-ResetPassword")
	}
	// Item 2kk (a): the child is told where to write its result, and that path
	// is returned to the caller whatever happens.
	resultPath, logPath := m.resultPaths()
	_ = os.Remove(resultPath)
	arguments = append(arguments, "-ResultFile", resultPath)

	// Item 2ng (a): THE WRAPPER IS WHAT GETS ELEVATED. It runs the same script with the
	// same arguments and appends every stream to this run's log, so a failure ABOVE the
	// script's own result writer — a parameter that will not bind, a policy refusal, a
	// crash on load — lands in the file the message already names instead of vanishing
	// into a hidden console. When the wrapper is not installed the old direct call is
	// used, so an older tree still works and only loses the capture.
	wrapper := filepath.Join(filepath.Dir(scriptPath), "run-elevated-provision.ps1")
	if _, statErr := os.Stat(wrapper); statErr == nil {
		arguments = append([]string{
			"-NoLogo", "-NoProfile", "-NonInteractive", "-File", wrapper,
			"-Log", logPath, "-Script", scriptPath,
		}, stripLeadingHostArguments(arguments, scriptPath)...)
	}
	output, runErr := m.runLauncher(ctx, arguments)

	// (c) and (d): the streams are KEPT, never shown as the outcome. A PS 5.1
	// child serializes progress records to stderr as `#< CLIXML`, which is
	// ordinary noise; reading it as failure text is the defect this item exists
	// for, and it cost the operator a provisioning run on v1.11.0.
	m.writeLauncherLog(logPath, output)

	launch := LaunchStarted
	if strings.Contains(string(output), "AGENTB_ELEVATION_NOT_STARTED") || !strings.Contains(string(output), "AGENTB_ELEVATED_STARTED") {
		launch = LaunchDeclined
	}
	result := readElevatedResult(resultPath)

	// (e): a launch that never happened is its own message and its own state.
	if launch == LaunchDeclined {
		return SetupResult{Launch: LaunchDeclined, LogPath: logPath},
			fmt.Errorf("Windows elevation was declined or could not be started, so no account operation ran")
	}

	// (a): the result is the child's exit code and the file it wrote. Success is
	// a zero exit; the MESSAGE is the file's, complete and never truncated.
	if runErr != nil {
		message := "the elevated service-account setup did not complete"
		if result != nil && strings.TrimSpace(result.Message) != "" {
			message = result.Message
		} else if first := firstErrorLine(logPath); first != "" {
			// Item 2ng (b): NO RESULT FILE MEANS THE CHILD DIED ABOVE THE WRITER, and now
			// that the wrapper captures its streams the reason is in the log. The generic
			// sentence was all the operator could see on 2026-09-27 at 19:44; this is the
			// child's own first error line instead.
			message = first
		}
		return SetupResult{Attempted: true, Launch: LaunchStarted, Result: result, LogPath: logPath},
			fmt.Errorf("%s (full output: %s)", message, logPath)
	}
	if result != nil && !result.Ok {
		return SetupResult{Attempted: true, Launch: LaunchStarted, Result: result, LogPath: logPath},
			fmt.Errorf("%s (full output: %s)", result.Message, logPath)
	}
	return SetupResult{Attempted: true, Launch: LaunchStarted, Result: result, LogPath: logPath}, nil
}

// resultPaths names the two files this run writes: the child's result and the
// launcher's streams. Both are per-run, so a later run never reads an older
// one -- the bug that would replace the one this item fixes.
func (m *windowsManager) resultPaths() (string, string) {
	stamp := time.Now().UTC().Format("20060102-150405.000")
	dir := m.workDir
	if strings.TrimSpace(dir) == "" {
		dir = os.TempDir()
	}
	_ = os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, "service-identity-"+stamp+".result.json"),
		filepath.Join(dir, "service-identity-"+stamp+".log")
}

// stripLeadingHostArguments drops the PowerShell host flags and the -File <script> pair
// from an argument list, leaving only the script's OWN arguments. The wrapper supplies
// its own host flags and names the script itself, so passing these through would give the
// inner call two of each.
func stripLeadingHostArguments(arguments []string, scriptPath string) []string {
	for index := 0; index < len(arguments); index++ {
		if arguments[index] == "-File" && index+1 < len(arguments) && arguments[index+1] == scriptPath {
			return append([]string(nil), arguments[index+2:]...)
		}
	}
	return append([]string(nil), arguments...)
}

// writeLauncherLog appends the launcher's own streams. Item 2ng (a): it used to OVERWRITE
// this file, which would now erase what the elevated wrapper captured into it — the whole
// point of the wrapper. The launcher's own lines go at the end, because they are about
// starting the child rather than about what the child did.
func (m *windowsManager) writeLauncherLog(path string, output []byte) {
	if len(output) == 0 {
		output = []byte("(the launcher produced no output)\n")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		_ = os.WriteFile(path, output, 0o600)
		return
	}
	defer file.Close()
	_, _ = file.WriteString("AGENTB_LAUNCHER_OUTPUT\n")
	_, _ = file.Write(output)
}

// firstErrorLine is item 2ng (b): the child's own first complaint, out of the log the
// wrapper captured. A PowerShell error arrives as a block — the message, then At <file>:
// <line>, then the source line and the CategoryInfo — and the FIRST line is the one a
// person can act on. The marker lines the wrapper and the launcher write are skipped,
// and so is the CLIXML noise item 2kk documented, because neither is a reason.
func firstErrorLine(path string) string {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, raw := range strings.Split(strings.ReplaceAll(string(bytes), "\r\n", "\n"), "\n") {
		// A UTF-8 BOM rides the log's first line. Without trimming it the wrapper's own
		// marker stopped looking like a marker and was reported as the reason.
		line := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
		switch {
		case line == "":
		case strings.HasPrefix(line, "AGENTB_"):
		case strings.HasPrefix(line, "#< CLIXML"):
		case strings.HasPrefix(line, "<Objs"), strings.HasPrefix(line, "<S "):
			// none of these is a reason
		default:
			// A PowerShell error block's continuation lines are not the reason either.
			if strings.HasPrefix(line, "At ") || strings.HasPrefix(line, "+ ") || strings.HasPrefix(line, "~") {
				continue
			}
			if len(line) > 400 {
				line = line[:400]
			}
			return line
		}
	}
	return ""
}

// readElevatedResult reads what the child wrote. A missing or unreadable file
// is not an error in itself: the exit code still decides, and the caller falls
// back to its own words rather than to a stream.
func readElevatedResult(path string) *ElevatedResult {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var result ElevatedResult
	if err := json.Unmarshal(bytes, &result); err != nil {
		return nil
	}
	if strings.TrimSpace(result.Message) == "" {
		return nil
	}
	return &result
}

// elevate is the production launcher: a PowerShell that raises UAC once and
// waits for the elevated child, propagating its exit code.
func (m *windowsManager) elevate(ctx context.Context, arguments []string) ([]byte, error) {
	command := elevatedCommand(m.powershell, arguments)
	return exec.CommandContext(ctx, m.powershell,
		"-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", encodePowerShell(command),
	).CombinedOutput()
}

func elevatedCommand(executable string, arguments []string) string {
	quoted := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		quoted = append(quoted, `"`+strings.ReplaceAll(argument, `"`, `\"`)+`"`)
	}
	return fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
# Item 2kk (b): nothing in the launcher writes progress or verbose into a
# captured stream. PowerShell serializes those records as CLIXML on stderr, and
# the harness used to read them as failure text.
$ProgressPreference = 'SilentlyContinue'
$VerbosePreference = 'SilentlyContinue'
$InformationPreference = 'SilentlyContinue'
try {
  $process = Start-Process -FilePath '%s' -ArgumentList '%s' -Verb RunAs -WindowStyle Hidden -PassThru
  Write-Output 'AGENTB_ELEVATED_STARTED'
  $process.WaitForExit()
  if ($process.ExitCode -ne 0) { exit $process.ExitCode }
} catch {
  Write-Output 'AGENTB_ELEVATION_NOT_STARTED'
  exit 1
}
`, strings.ReplaceAll(executable, "'", "''"), strings.ReplaceAll(strings.Join(quoted, " "), "'", "''"))
}

func encodePowerShell(command string) string {
	encoded := utf16.Encode([]rune(command))
	bytes := make([]byte, len(encoded)*2)
	for index, value := range encoded {
		bytes[index*2] = byte(value)
		bytes[index*2+1] = byte(value >> 8)
	}
	return base64.StdEncoding.EncodeToString(bytes)
}

func safePowerShellError(output []byte, err error) string {
	text := strings.TrimSpace(strings.ReplaceAll(string(output), "\r\n", "\n"))
	if text == "" {
		return err.Error()
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 4 {
		lines = lines[len(lines)-4:]
	}
	return strings.Join(lines, " ")
}
