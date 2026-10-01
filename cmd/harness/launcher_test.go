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
		// Item 2p8: Agent_b is a GUI-subsystem executable, so ShellExecute can
		// again supply a detached Process object (including a reliable exit code)
		// without creating a console. Start-Process also avoids inherited output
		// handles, so a caller capturing the launcher does not wait on the server.
		// The foreground -Console path keeps NoNewWindow, because there the
		// window is the operator's and closing it is meant to stop the server.
		filepath.Join("scripts", "launch-Agent_b.ps1"):  {"[switch]$Console", "Start-Process -FilePath $executable", "-WindowStyle Hidden", "-NoNewWindow", "$process.WaitForExit()", "launcher-errors.log", "-ShowFailure", "Press any key to close."},
		filepath.Join("scripts", "agentb-stop.ps1"):     {"the operator's stop script", "AppendAllText", "ProcessRecordsReason"},
		filepath.Join("scripts", "install-Agent_b.ps1"): {"-ProcessRecordsReason"},
		filepath.Join("cmd", "harness", "main.go"):      {"the installer's graceful stop"},
	}
	for relative, wanted := range checks {
		body, err := os.ReadFile(filepath.Join(root, relative))
		if os.IsNotExist(err) && relative == "AGENTS.md" {
			// rel-1.24.0: AGENTS.md left the public repository at the operator's
			// request. It is still on his disk and still read by the product, but
			// a CI checkout does not have it, and a test that reads a file the
			// repository no longer carries passes locally and fails in CI —
			// which is exactly what it did.
			//
			// The discipline it guards is duplicated in docs/HARDENING.md, which
			// IS tracked and is checked below, so the rule is still gated. This
			// skips the untracked copy rather than pretending to check it.
			t.Logf("AGENTS.md is not in this checkout (untracked at rel-1.24.0); docs/HARDENING.md carries the same rule and is checked")
			continue
		}
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
		// rel-1.16.0 replaced the single-start recovery in both documents: a worker
		"AGENTS.md":                           {"record it", "Windows Error Reporting", "Then start it and continue", "never taking focus"},
		filepath.Join("docs", "HARDENING.md"): {"Application Error / Windows Error Reporting", "detached from the worker's shell", "report its PID, version and digest"},
	}
	for relative, wanted := range checks {
		body, err := os.ReadFile(filepath.Join(root, relative))
		if os.IsNotExist(err) && relative == "AGENTS.md" {
			// rel-1.24.0: AGENTS.md left the public repository at the operator's
			// request. It is still on his disk and still read by the product, but
			// a CI checkout does not have it, and a test that reads a file the
			// repository no longer carries passes locally and fails in CI —
			// which is exactly what it did.
			//
			// The discipline it guards is duplicated in docs/HARDENING.md, which
			// IS tracked and is checked below, so the rule is still gated. This
			// skips the untracked copy rather than pretending to check it.
			t.Logf("AGENTS.md is not in this checkout (untracked at rel-1.24.0); docs/HARDENING.md carries the same rule and is checked")
			continue
		}
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

func TestInstalledLauncherRefusesForeignEndpoint2kr(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	body, err := os.ReadFile(filepath.Join(root, "scripts", "launch-Agent_b.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, value := range []string{"Assert-AgentBEndpointOwner", "Get-NetTCPConnection -LocalPort", "$owner.ExecutablePath", "$commandLine.IndexOf($configPath", "$commandLine.IndexOf($applicationRoot", "$commandLine.IndexOf($dataRoot", "Foreign instance refused"} {
		if !strings.Contains(text, value) {
			t.Errorf("launcher does not preserve %q", value)
		}
	}
}
