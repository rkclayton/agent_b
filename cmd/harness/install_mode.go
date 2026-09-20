package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Item 2gl (v1.2.0/W2): `Agent_b.exe --install` — the app installs itself.
//
// The operator: "i'm sick of using .cmd and powershell windows i thought we had
// an actual installer in the plan." This is that entry point, shipped in the
// candidate as `Agent_b-setup.exe` (the same binary under another name).
//
// What it does NOT do is reimplement the install. `scripts/install-Agent_b.ps1`
// is the install — ACLs, the service account, the manifest-bound exe identity,
// verify-before-stop, restart-on-failure, the HKCU Add/Remove Programs entry
// and the data-preserving uninstall are all already there and already proved by
// the installer suite. W1 measured that and said so: verify and reuse, do not
// rebuild. This mode wraps it, so the operator gets a window instead of a
// console and an interrupted run is visible afterwards.
//
// Progress is a FILE the Setup page reads, not a loopback socket. W1's other
// correction: an elevated process opening a socket can raise a Windows Firewall
// prompt, and a prompt is exactly what this item is removing.

// installOptions is what --install accepts. Everything it does not name is
// left to the PowerShell installer's own defaults, so the two cannot drift.
type installOptions struct {
	quiet      bool
	sourceDir  string
	dataRoot   string
	passThough []string
}

// installProgress is one line of the progress file: the Setup page renders
// these in order and the last one is the result.
type installProgress struct {
	At    string `json:"at"`
	Phase string `json:"phase"`
	Text  string `json:"text"`
	Done  bool   `json:"done,omitempty"`
	OK    bool   `json:"ok,omitempty"`
}

const installProgressName = "install-progress.jsonl"

func installProgressPath(dataRoot string) string {
	return filepath.Join(dataRoot, installProgressName)
}

// appendProgress writes one line. A failure to write progress never fails the
// install: the install is the point, the readout is not.
func appendProgress(dataRoot string, entry installProgress) {
	entry.At = time.Now().UTC().Format(time.RFC3339)
	encoded, err := json.Marshal(entry)
	if err != nil {
		return
	}
	file, err := os.OpenFile(installProgressPath(dataRoot), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(encoded, '\n'))
}

// phaseFor reads the installer's own transcript lines and names the phase from
// them, so the phases the operator sees are the installer's, not a second
// vocabulary invented here.
func phaseFor(line string) string {
	switch {
	case strings.HasPrefix(line, "PREFLIGHT"):
		return "preflight"
	case strings.HasPrefix(line, "CANDIDATE:"):
		return "checking the candidate"
	case strings.HasPrefix(line, "STOPPING") || strings.HasPrefix(line, "STOPPED"):
		return "stopping the running application"
	case strings.HasPrefix(line, "CREATED") || strings.HasPrefix(line, "Application:"):
		return "copying the application"
	case strings.HasPrefix(line, "INSTALLATION COMPLETE"):
		return "finishing"
	case strings.HasPrefix(line, "INSTALLATION FAILED"):
		return "failed"
	}
	return ""
}

// windowsPowerShell is Windows PowerShell 5.1 by absolute path (item 2gc:
// never a bare name, so a PATH that reaches another shell first cannot change
// what the installer runs).
func windowsPowerShell() string {
	return filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
}

// runInstall is `--install`. It returns the exit code.
func runInstall(options installOptions, args []string) int {
	// Item 2gv (v1.2.5): THE LOG IS THE FIRST THING. Before the source is
	// resolved, before the marker, before any check — because a failure before
	// the log is a failure nobody can read, which is exactly what the operator
	// met when a double-clicked setup did nothing at all.
	dataRoot := options.dataRoot
	if dataRoot == "" {
		dataRoot = filepath.Join(os.Getenv("LOCALAPPDATA"), "Agent_b")
	}
	log := openInstallLog(dataRoot, options.quiet)
	defer log.close()
	log.printf("install: starting; data root %s", dataRoot)

	source := options.sourceDir
	if source == "" {
		executable, err := os.Executable()
		if err != nil {
			return log.fail("cannot locate this executable: %v", err)
		}
		source = filepath.Dir(executable)
	}
	log.printf("install: source %s", source)
	script := filepath.Join(source, "scripts", "install-Agent_b.ps1")
	if _, err := os.Stat(script); err != nil {
		return log.fail("%s is missing; run this from the candidate folder", script)
	}

	// The marker goes down BEFORE anything is touched, so a failure from here
	// on is recorded no matter how the process ends — including a window the
	// operator closes.
	marker := InstallMarker{Phase: "starting", Version: currentDisplayVersion(source), Source: source, Quiet: options.quiet}
	if err := writeInstallMarker(dataRoot, marker); err != nil {
		return log.fail("could not record that the install began: %v", err)
	}
	_ = os.Remove(installProgressPath(dataRoot))
	appendProgress(dataRoot, installProgress{Phase: "starting", Text: "Installing Agent_b " + marker.Version})

	powershell := windowsPowerShell()
	scriptArgs := append([]string{"-NoLogo", "-NoProfile", "-File", script}, args...)
	command := exec.Command(powershell, scriptArgs...)
	command.Dir = source
	output, err := command.StdoutPipe()
	if err != nil {
		return log.fail("could not read the installer's output: %v", err)
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		return log.fail("could not start the installer: %v", err)
	}

	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lastPhase := "starting"
	for scanner.Scan() {
		line := scanner.Text()
		// --quiet is the suite's path and the only one that prints to a
		// console; the operator's path shows the same lines in the window.
		if options.quiet {
			fmt.Println(line)
		}
		if phase := phaseFor(line); phase != "" && phase != lastPhase {
			lastPhase = phase
			marker.Phase = phase
			_ = writeInstallMarker(dataRoot, marker)
		}
		if strings.TrimSpace(line) != "" {
			appendProgress(dataRoot, installProgress{Phase: lastPhase, Text: line})
		}
	}
	waitErr := command.Wait()
	code := 0
	if waitErr != nil {
		code = 1
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			code = exitErr.ExitCode()
		}
	}

	if code == 0 {
		appendProgress(dataRoot, installProgress{Phase: "finished", Text: "Agent_b " + marker.Version + " is installed.", Done: true, OK: true})
		// The marker is cleared ONLY on success. That is what makes its
		// presence at the next launch mean something.
		if err := clearInstallMarker(dataRoot); err != nil {
			log.printf("install: the install finished but its marker could not be cleared: %v", err)
		}
		return 0
	}
	appendProgress(dataRoot, installProgress{Phase: lastPhase, Text: fmt.Sprintf("The install stopped during %s (exit %d). It is safe to run again.", lastPhase, code), Done: true})
	marker.Phase = lastPhase
	_ = writeInstallMarker(dataRoot, marker)
	// Item 2gv: this exit is logged too. The Setup page shows the same thing
	// when it is up; from Explorer with no page yet, the box and the log are
	// the whole of the report.
	log.printf("install: the installer exited %d during %s", code, lastPhase)
	if !options.quiet {
		showInstallFailure("Agent_b install failed", fmt.Sprintf("The install stopped during %s (exit %d). It is safe to run again.\n\nLog: %s", lastPhase, code, log.location()))
	}
	return code
}

// currentDisplayVersion reads the version the installer will report, from the
// installer script itself, so the marker never names a different one.
func currentDisplayVersion(source string) string {
	content, err := os.ReadFile(filepath.Join(source, "scripts", "install-Agent_b.ps1"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "$displayVersion") {
			continue
		}
		if open := strings.Index(trimmed, "'"); open >= 0 {
			if close := strings.Index(trimmed[open+1:], "'"); close >= 0 {
				return "v" + trimmed[open+1:open+1+close]
			}
		}
	}
	return ""
}

// readInstallProgress is what the Setup page reads.
func readInstallProgress(dataRoot string) ([]installProgress, error) {
	file, err := os.Open(installProgressPath(dataRoot))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	entries := []installProgress{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var entry installProgress
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return entries, err
	}
	return entries, nil
}
