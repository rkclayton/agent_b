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
		filepath.Join("scripts", "launch-Agent_b.ps1"): {"[switch]$Console", "$start.WindowStyle = 'Hidden'", "$start.NoNewWindow = $true", "$process.WaitForExit()", "launcher-errors.log"},
	}
	for relative, wanted := range checks {
		body, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil { t.Fatal(err) }
		text := string(body)
		for _, value := range wanted {
			if !strings.Contains(text, value) { t.Errorf("%s does not preserve %q", relative, value) }
		}
	}
}
