package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
)

// With no service split, file tools use the operator's OS reach directly;
// command tools retain their separately governed outside-path card.
func TestOutsideReadIdentityChoiceWithNoServiceIdentity(t *testing.T) {
	workspace, outsideDir := t.TempDir(), t.TempDir()
	outside := filepath.Join(outsideDir, "win.ini")
	if err := os.WriteFile(outside, []byte("; for 16-bit app support\n[fonts]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults(workspace)
	cfg.Approval.Mode = config.ApprovalModeBoundaryOnly
	cfg.Shell.ServiceAccount.Enabled = false
	identity := tools.NewFileIdentity(nil)
	identity.Configure(cfg)
	shell := tools.NewShell(cfg.Shell)
	shell.Configure(cfg)
	registry := tools.New(identity.Wrap(tools.NewReadFile(cfg.Tools.ReadFile)), shell, tools.NewRunScript(shell))
	bus := events.NewBus()
	runner := &Runner{bus: bus, tools: registry, cfg: func() config.Config { return cfg }}
	runner.gate = NewGate(bus, runner.cfg)
	s := &session.Session{ID: "walk", Workspace: workspace, Run: session.RunState{Status: "running"}, ToolsEnabled: map[string]bool{"read_file": true, "shell": true, "run_script": true}, LastSeen: map[string]time.Time{}}
	read := runner.executeTool(context.Background(), s, "run", "read", "read_file", map[string]any{"path": outside})
	if !read.OK || read.OperatorOverrideAvailable || !strings.Contains(read.Content, "16-bit app support") || strings.Contains(read.Content, "operator-identity") {
		t.Fatalf("read_file should use the running identity directly: %+v", read)
	}

	calls := []struct {
		name string
		args map[string]any
	}{
		{"run_script", map[string]any{"language": "powershell", "source": `[System.IO.File]::ReadAllLines("` + outside + `")[0]`}},
		{"shell", map[string]any{"command": `[System.IO.File]::ReadAllText('` + outside + `')`}},
	}
	for index, call := range calls {
		for _, decision := range []string{"deny", "approve"} {
			eventCh, unsubscribe := bus.Subscribe()
			callID := call.name + "-" + decision
			done := make(chan string, 1)
			go func() {
				outcome := runner.executeTool(context.Background(), s, "run", callID, call.name, call.args)
				done <- outcome.Content
			}()
			var asked map[string]any
			for asked == nil {
				select {
				case event := <-eventCh:
					if data, ok := event.Data.(map[string]any); ok && data["name"] == call.name+".operator_override" {
						asked = data
					}
				case <-time.After(10 * time.Second):
					t.Fatalf("%d %s: no card was raised for a read outside the folder", index, call.name)
				}
			}
			unsubscribe()
			if err := runner.gate.Decide(s.ID, callID+":operator", decision); err != nil {
				t.Fatal(err)
			}
			content := <-done
			read := strings.Contains(content, "16-bit app support")
			if decision == "deny" && read {
				t.Fatalf("%s: declining the card still read the file: %q", call.name, content)
			}
			if decision == "approve" && !read {
				t.Fatalf("%s: approving the card did not read the file once: %q", call.name, content)
			}
		}
	}
}

// v0.69.0/W12 cold review. The run_script card shows the script and says why;
// "Yes, for this chat" with no service identity holds for the chat's later
// outside reads and stores no identity grant that would wake up once the
// service identity is turned on.
func TestTheOutsideReadCardShowsTheScriptAndItsChatGrantStaysInItsPosture(t *testing.T) {
	workspace, outsideDir := t.TempDir(), t.TempDir()
	outside := filepath.Join(outsideDir, "win.ini")
	if err := os.WriteFile(outside, []byte("; for 16-bit app support\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults(workspace)
	cfg.Approval.Mode = config.ApprovalModeBoundaryOnly
	cfg.Shell.ServiceAccount.Enabled = false
	shell := tools.NewShell(cfg.Shell)
	shell.Configure(cfg)
	bus := events.NewBus()
	runner := &Runner{bus: bus, tools: tools.New(shell, tools.NewRunScript(shell)), cfg: func() config.Config { return cfg }}
	runner.gate = NewGate(bus, runner.cfg)
	s := &session.Session{ID: "review", Workspace: workspace, Run: session.RunState{Status: "running"}, ToolsEnabled: map[string]bool{"shell": true, "run_script": true}, LastSeen: map[string]time.Time{}}
	source := `[System.IO.File]::ReadAllLines("` + outside + `")[0]`

	eventCh, unsubscribe := bus.Subscribe()
	done := make(chan string, 1)
	go func() {
		done <- runner.executeTool(context.Background(), s, "run", "first", "run_script", map[string]any{"language": "powershell", "source": source}).Content
	}()
	var asked map[string]any
	for asked == nil {
		select {
		case event := <-eventCh:
			if data, ok := event.Data.(map[string]any); ok && data["name"] == "run_script.operator_override" {
				asked = data
			}
		case <-time.After(10 * time.Second):
			t.Fatal("no card for the script")
		}
	}
	unsubscribe()
	args, _ := asked["args"].(map[string]any)
	human, _ := asked["human"].(events.HumanNotice)
	if args["source"] != source || !strings.Contains(human.Happened, "outside the folder") || !strings.Contains(human.Happened, outside) {
		t.Fatalf("the card must carry the script and say why: args=%v human=%+v", args, human)
	}
	if err := runner.gate.Decide(s.ID, "first:operator", "session"); err != nil {
		t.Fatal(err)
	}
	if content := <-done; !strings.Contains(content, "16-bit app support") {
		t.Fatalf("approving did not read the file: %q", content)
	}
	if runner.hasIdentityChatGrant(s.ID) || runner.hasFileGrant(s.ID, "run") {
		t.Fatal("a no-identity chat grant stored an identity grant")
	}

	// The chat's next outside read runs without a card.
	eventCh, unsubscribe = bus.Subscribe()
	defer unsubscribe()
	second := make(chan string, 1)
	go func() {
		second <- runner.executeTool(context.Background(), s, "run", "second", "shell", map[string]any{"command": `[System.IO.File]::ReadAllText('` + outside + `')`}).Content
	}()
	select {
	case content := <-second:
		if !strings.Contains(content, "16-bit app support") {
			t.Fatalf("the chat grant did not hold: %q", content)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the second outside read waited on a card after Yes, for this chat")
	}
	for len(eventCh) > 0 {
		if data, ok := (<-eventCh).Data.(map[string]any); ok && data["type"] == events.ApprovalRequired {
			t.Fatalf("a card was raised: %v", data)
		}
	}
}
