package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
)

// Item 2fy, v0.71.0/W1. read_file on a path outside the folder that does not
// exist answers with a tool error naming the boundary and raises no card; the
// run goes on. The eval's two miss tapes (v0.70.2/W10) were shell commands
// changing directory to invented paths; since item 2fz they raise no card.
func TestAMissingOutsideReadRaisesNoCardAndNeitherDoTheTapes(t *testing.T) {
	workspace := t.TempDir()
	cfg := config.Defaults(workspace)
	cfg.Approval.Mode = config.ApprovalModeBoundaryOnly
	cfg.Shell.ServiceAccount.Enabled = false
	identity := tools.NewFileIdentity(nil)
	identity.Configure(cfg)
	shell := tools.NewShell(cfg.Shell)
	shell.Configure(cfg)
	registry := tools.New(identity.Wrap(tools.NewReadFile(cfg.Tools.ReadFile)), shell)
	bus := events.NewBus()
	runner := &Runner{bus: bus, tools: registry, cfg: func() config.Config { return cfg }}
	runner.gate = NewGate(bus, runner.cfg)
	s := &session.Session{ID: "missing", Workspace: workspace, Run: session.RunState{Status: "running"}, ToolsEnabled: map[string]bool{"read_file": true, "shell": true}, LastSeen: map[string]time.Time{}}

	cardFor := func(callID, name string, args map[string]any) (bool, string) {
		eventCh, unsubscribe := bus.Subscribe()
		defer unsubscribe()
		done := make(chan string, 1)
		go func() { done <- runner.executeTool(context.Background(), s, "run", callID, name, args).Content }()
		for {
			select {
			case event := <-eventCh:
				if data, ok := event.Data.(map[string]any); ok && event.Type == events.ApprovalRequired && data["name"] == name+".operator_override" {
					_ = runner.gate.Decide(s.ID, callID+":operator", "deny")
					return true, <-done
				}
			case content := <-done:
				return false, content
			case <-time.After(10 * time.Second):
				t.Fatalf("%s: neither a card nor a result", callID)
			}
		}
	}
	missing := filepath.Join(t.TempDir(), "definitely", "not", "here.txt")
	if carded, content := cardFor("read-missing", "read_file", map[string]any{"path": missing}); carded || !strings.Contains(content, "no such file or directory") || !strings.Contains(content, "outside the folder") {
		t.Fatalf("a missing outside read: card=%t content=%q", carded, content)
	}
	for _, tape := range []string{
		`cd C:\Users\Public\Documents\GitHub\eval-20260310-110411-1120 && C:\Go\bin\go.exe test ./logic -run ^TestTouchedPlanRoots$ -v`,
		`cd /d "C:\Users\..." 2>nul; C:\Go\bin\go.exe test ./logic -run ^TestRetentionPreservesNestedEvidence$ -v`,
	} {
		// v1.0.0/W1 (item 2fz): the tape's `cd` to an invented folder is a
		// directory change and runs under the OS boundary; no card is raised,
		// and any failure is the shell's own, which the model reads and recovers
		// from (on Windows PowerShell 5.1 the first tape's `&&` is itself refused).
		if carded, content := cardFor("tape", "shell", map[string]any{"command": tape}); carded {
			t.Fatalf("the shell tape %q still raises the card: %q", tape, content)
		}
	}
}
