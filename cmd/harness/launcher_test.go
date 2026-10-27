package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestMain lets this test binary stand in for Agent_b.exe behind the real launcher (item 2x1 (b)):
// "window" writes the line a -window server writes when it shows its failure window and exits when
// that window would be closed; "held" keeps the window open past the launcher's wait; "none" dies
// before it starts at all.
func TestMain(m *testing.M) {
	if mode := os.Getenv("AGENTB_LAUNCH_STAND_IN"); mode != "" {
		for index, value := range os.Args {
			if value == "-data-root" && index+1 < len(os.Args) && mode != "none" {
				appendLauncherMessage(os.Args[index+1], "Agent_b startup error: stand-in refused its configuration")
			}
		}
		if mode == "held" {
			time.Sleep(10 * time.Second)
		}
		os.Exit(1)
	}
	os.Exit(m.Run())
}

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
		filepath.Join("scripts", "launch-Agent_b.ps1"):  {"[switch]$Console", "Start-Process -FilePath $executable", "-WindowStyle Hidden", "-NoNewWindow", "$process.WaitForExit()", "launcher-errors.log", "-ShowFailure", "Press any key to close.", "[ValidateRange(5, 300)]", "$state -eq 'exited'", "$process.ExitCode", "$state -eq 'timeout'", "within $StartupTimeoutSeconds seconds"},
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

// Item 2x1 (b): a -window server that fails shows its own window and writes its own line; the
// launcher then adds nothing. A server that never started is the launcher's to report, once.
func TestLauncherLeavesAWindowFailureToTheServer2x1(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the launcher is PowerShell on Windows")
	}
	if processIsElevated() {
		t.Skip("the launcher refuses an elevated token before it starts anything")
	}
	_, file, _, _ := runtime.Caller(0)
	launcher := filepath.Join(filepath.Dir(file), "..", "..", "scripts", "launch-Agent_b.ps1")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"window", "held", "none"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			app, data := filepath.Join(root, "app"), filepath.Join(root, "data")
			for _, dir := range []string{app, filepath.Join(data, "logs")} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(app, "Agent_b.exe"), body, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(app, "WebView2Loader.dll"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			config := filepath.Join(data, "harness.json")
			if err := os.WriteFile(config, []byte(fmt.Sprintf(`{"listen":"127.0.0.1:%d"}`, port)), 0o600); err != nil {
				t.Fatal(err)
			}
			powershell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
			command := exec.Command(powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", launcher,
				"-ApplicationDirectory", app, "-DataDirectory", data, "-ConfigPath", config, "-NoPause", "-StartupTimeoutSeconds", "5")
			command.Env = append(os.Environ(), "AGENTB_LAUNCH_STAND_IN="+mode)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("the launcher reported success: %s", output)
			}
			log, _ := os.ReadFile(filepath.Join(data, "logs", launcherLogName))
			lines := strings.Split(strings.TrimSpace(string(log)), "\n")
			if record, _ := os.ReadFile(filepath.Join(data, "logs", "launcher.log")); strings.Contains(string(record), "HOST WINDOW: unavailable") {
				t.Skipf("no host window on this machine: %s", record)
			}
			for deadline := time.Now().Add(20 * time.Second); os.Remove(filepath.Join(app, "Agent_b.exe")) != nil && time.Now().Before(deadline); {
				time.Sleep(250 * time.Millisecond) // the held stand-in outlives the launcher's wait
			}
			want := "Agent_b startup error: stand-in refused its configuration"
			if mode == "none" {
				want = "Agent_b failed to start"
			}
			if len(lines) != 1 || !strings.Contains(lines[0], want) {
				t.Fatalf("launcher-errors.log holds %d line(s), want one naming %q:\n%s\nlauncher: %s", len(lines), want, log, output)
			}
		})
	}
}
