//go:build windows

package agent

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
)

func TestOperatorCommandUsesNativeExitCodeWhenPowerShellWrapsStderr(t *testing.T) {
	cmdPath, err := exec.LookPath("cmd.exe")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults(t.TempDir())
	cfg.Shell.ServiceAccount.Enabled = true
	cfg.Tools.Shell.OperatorCommands = []string{cmdPath}
	shell := tools.NewShell(cfg.Shell)
	shell.Configure(cfg)
	bus := events.NewBus()
	runner := &Runner{bus: bus, tools: tools.New(shell), cfg: func() config.Config { return cfg }}
	runner.gate = NewGate(bus, runner.cfg)
	s := &session.Session{ID: "session", Workspace: t.TempDir(), Run: session.RunState{Status: "running"}, ToolsEnabled: map[string]bool{"shell": true}}
	eventCh, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	command := fmt.Sprintf(`& %q /d /s /c "echo remote-info 1>&2 & exit /b 0"`, cmdPath)
	done := make(chan tools.CallOutcome, 1)
	go func() {
		done <- runner.executeTool(context.Background(), s, "run", "call", "shell", map[string]any{"command": command})
	}()
	nextApprovalEvent(t, eventCh)
	if err := runner.gate.Decide(s.ID, "call", "run"); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-done:
		if !outcome.OK || !outcome.OperatorContext || !strings.Contains(outcome.Content, "remote-info") {
			t.Fatalf("outcome=%+v", outcome)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("operator command did not complete")
	}
}
