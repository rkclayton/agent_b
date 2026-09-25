package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLaunchersAreHiddenByDefaultWithConsoleOptIn(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	checks := map[string][]string{
		"start-Agent_b.cmd": {"AGENTB_HIDDEN_REENTRY", "launch-hidden.vbs", "-Console"},
		filepath.Join("scripts", "launch-installed.cmd"): {"AGENTB_HIDDEN_REENTRY", `%~dp0scripts\launch-hidden.vbs`, "-Console"},
		// Item 2hg (v1.3.0/W6): a background server is started with
		// CreateNoWindow, NOT -WindowStyle Hidden. Hidden still gives a console
		// application a console window, and taskkill without /F posts WM_CLOSE
		// to it, which Windows turns into CTRL_CLOSE_EVENT and Go into SIGTERM
		// -- the server then stopped despite 2eq. The foreground -Console path
		// keeps NoNewWindow, because there the window is the operator's and
		// closing it is meant to stop the server.
		// Item 2hg (v1.3.0/W6): a background server is started through
		// CreateProcess with CREATE_NO_WINDOW and bInheritHandles FALSE.
		// -WindowStyle Hidden still gives a console application a console
		// window, and a taskkill WM_CLOSE reaching it became CTRL_CLOSE_EVENT
		// and then SIGTERM. The obvious replacement, UseShellExecute=$false,
		// hands the child the parent's stdout instead, so a detached server
		// held its launcher's output pipe open and anything capturing that
		// output blocked until the server exited. CreateProcess avoids both.
		// The foreground -Console path keeps NoNewWindow, because there the
		// window is the operator's and closing it is meant to stop the server.
		filepath.Join("scripts", "launch-Agent_b.ps1"):  {"[switch]$Console", "CREATE_NO_WINDOW", "$false, $CREATE_NO_WINDOW", "-NoNewWindow", "$process.WaitForExit()", "launcher-errors.log"},
		filepath.Join("scripts", "agentb-stop.ps1"):     {"the operator's stop script", "AppendAllText", "ProcessRecordsReason"},
		filepath.Join("scripts", "install-Agent_b.ps1"): {"-ProcessRecordsReason"},
		filepath.Join("cmd", "harness", "main.go"):      {"the installer's graceful stop"},
	}
	for relative, wanted := range checks {
		body, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		for _, value := range wanted {
			if !strings.Contains(text, value) {
				t.Errorf("%s does not preserve %q", relative, value)
			}
		}
	}
}

func TestAbsentBaselineRecoveryStaysOneShotAndEvidenceFirst(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	checks := map[string][]string{
		"AGENTS.md":                           {"preserve and inspect diagnostics before starting anything", "permit exactly one start", "never start it a second time", "successful start does not explain"},
		filepath.Join("docs", "HARDENING.md"): {"Application Error / Windows Error Reporting", "routine but unexplained absence permits one start", "never start it again automatically"},
	}
	for relative, wanted := range checks {
		body, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		for _, value := range wanted {
			if !strings.Contains(text, value) {
				t.Errorf("%s does not preserve %q", relative, value)
			}
		}
	}
}
