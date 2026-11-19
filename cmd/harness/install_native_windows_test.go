//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"harness/internal/quietproc"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

func TestConcurrentSetupLeavesFirstProgressAlone2z3(t *testing.T) {
	if root := os.Getenv("AGENTB_SETUP_REPEAT_FIXTURE"); root != "" {
		os.Exit(runInstall(installOptions{quiet: true, dataRoot: root, sourceDir: filepath.Join(root, "source")}, []string{"-ApplicationDirectory", filepath.Join(root, "application")}))
	}
	root := t.TempDir()
	unlock, err := lockSetup(filepath.Join(root, "application"))
	if err != nil || unlock == nil {
		t.Fatalf("first setup lock: %v", err)
	}
	defer unlock()
	progress := []byte("first setup progress\n")
	if err := os.WriteFile(installProgressPath(root), progress, 0600); err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	children := make([]*exec.Cmd, 4)
	started := time.Now()
	for index := range children {
		child := quietproc.Quiet(exec.Command(self, "-test.run=^TestConcurrentSetupLeavesFirstProgressAlone2z3$"))
		child.Env = append(os.Environ(), "AGENTB_SETUP_REPEAT_FIXTURE="+root)
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		children[index] = child
	}
	for _, child := range children {
		if err := child.Wait(); err != nil {
			t.Errorf("repeated setup: %v", err)
		}
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("four repeated setups took %s", elapsed)
	}
	got, _ := os.ReadFile(installProgressPath(root))
	if string(got) != string(progress) {
		t.Errorf("repeated setup replaced first progress: %q", got)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "logs")); len(entries) != 0 {
		t.Error("repeated setup opened an install log")
	}
}

func TestNativeUninstallRemovesExactDisposableRegistration(t *testing.T) {
	root := t.TempDir()
	application, data := filepath.Join(root, "application"), filepath.Join(root, "data")
	start, send := filepath.Join(root, "start"), filepath.Join(root, "send")
	for _, path := range []string{application, data, start, send} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(application, "Agent_b.exe"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	registration := fmt.Sprintf(`HKCU:\Software\Agent_b-Installer-Test-native-uninstall-%d`, time.Now().UnixNano())
	if err := writeUninstallRegistration(registration, map[string]any{"DisplayName": "Agent_b"}); err != nil {
		t.Fatal(err)
	}
	if err := runNativeUninstall(application, data, start, send, registration, true, true, true, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.OpenKey(registry.CURRENT_USER, registration[len(`HKCU:\`):], registry.QUERY_VALUE); err != registry.ErrNotExist {
		t.Fatalf("registration still opens: %v", err)
	}
	for _, path := range []string{application, data} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists: %v", path, err)
		}
	}
}

func TestNativeStopProcess2y5(t *testing.T) {
	if os.Getenv("AGENTB_STOP_HELPER") != "" {
		root := os.Getenv("AGENTB_STOP_HELPER")
		life := newLifetime(root, time.Now)
		life.applicationRoot = filepath.Dir(os.Args[0])
		watchSessionEnd(life.applicationRoot, life.stopped, func() {
			if os.Getenv("AGENTB_STOP_EXIT") == "1" {
				os.Exit(0)
			}
		})
		life.begin()
		select {}
	}
	for _, mode := range []string{"ignores", "graceful", "wrong-root", "wrong-created", "missing-created"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			application := filepath.Join(root, "application")
			data := filepath.Join(root, "data")
			if err := os.MkdirAll(application, 0700); err != nil {
				t.Fatal(err)
			}
			self, _ := os.Executable()
			executable := filepath.Join(application, "Agent_b.exe")
			if err := copyNativeFile(self, executable); err != nil {
				t.Fatal(err)
			}
			child := quietproc.Quiet(exec.Command(executable, "-test.run=^TestNativeStopProcess2y5$"))
			child.Env = append(os.Environ(), "AGENTB_STOP_HELPER="+data)
			if mode == "graceful" {
				child.Env = append(child.Env, "AGENTB_STOP_EXIT=1")
			}
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { child.Process.Kill(); child.Wait() }()
			markerPath := filepath.Join(data, "agent_b-run.json")
			var marker runMarker
			for until := time.Now().Add(10 * time.Second); time.Now().Before(until); {
				encoded, _ := os.ReadFile(markerPath)
				if json.Unmarshal(encoded, &marker) == nil && marker.PID == child.Process.Pid {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if marker.PID != child.Process.Pid {
				t.Fatal("helper did not write marker")
			}
			switch mode {
			case "wrong-root":
				marker.Application = filepath.Join(root, "other")
			case "wrong-created":
				marker.Created++
			case "missing-created":
				marker.Created = 0
			}
			encoded, _ := json.Marshal(marker)
			if err := os.WriteFile(markerPath, encoded, 0600); err != nil {
				t.Fatal(err)
			}
			log := openInstallLog(data, true)
			defer log.close()
			stopped, err := stopNativeInstalledProcess(application, data, log)
			invalid := strings.HasPrefix(mode, "wrong-") || mode == "missing-created"
			if err != nil || stopped == invalid {
				t.Fatalf("stopped=%v err=%v", stopped, err)
			}
			contents, _ := os.ReadFile(log.path)
			ended := strings.Contains(string(contents), "STOP FORCED:")
			if ended != (mode == "ignores") {
				t.Fatalf("forced=%v: %s", ended, contents)
			}
			if invalid && !processRunning(child.Process.Pid, processCreated(child.Process.Pid)) {
				t.Fatal("unproven process was ended")
			}
		})
	}
}
