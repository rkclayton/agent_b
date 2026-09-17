//go:build windows

package tools

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/credential"
	"harness/internal/session"
)

// windowProbeScript is what an attacker inside a tool process would run against
// production (item 2eq): find its hidden window, post and send it WM_CLOSE and a
// session end, and try to set its graceful-stop event.
const windowProbeScript = `$ErrorActionPreference = 'Continue'
Add-Type -Name Probe -Namespace AgentB -MemberDefinition '[DllImport("user32.dll", CharSet=CharSet.Unicode, SetLastError=true)] public static extern System.IntPtr FindWindowW(string c, System.IntPtr w); [DllImport("user32.dll", SetLastError=true)] public static extern bool PostMessageW(System.IntPtr h, uint m, System.IntPtr w, System.IntPtr l); [DllImport("user32.dll", SetLastError=true)] public static extern System.IntPtr SendMessageTimeoutW(System.IntPtr h, uint m, System.IntPtr w, System.IntPtr l, uint f, uint t, out System.IntPtr r);'
$hwnd = [AgentB.Probe]::FindWindowW('Agent_b-session-end-__PID__', [IntPtr]::Zero)
"find=$([int64]$hwnd) error=$([Runtime.InteropServices.Marshal]::GetLastWin32Error())"
$r = [IntPtr]::Zero
if ($hwnd -eq [IntPtr]::Zero) { 'post-close=no-handle'; 'send-query=no-handle'; 'send-end=no-handle' } else { "post-close=$([AgentB.Probe]::PostMessageW($hwnd, 0x10, [IntPtr]::Zero, [IntPtr]::Zero))"; "send-query=$([int64][AgentB.Probe]::SendMessageTimeoutW($hwnd, 0x11, [IntPtr]::Zero, [IntPtr]0x80000000, 2, 2000, [ref]$r))"; "send-end=$([int64][AgentB.Probe]::SendMessageTimeoutW($hwnd, 0x16, [IntPtr]1, [IntPtr]0x80000000, 2, 2000, [ref]$r))" }
try { $e = [System.Threading.EventWaitHandle]::OpenExisting('Local\Agent_b-stop-__PID__'); "event=opened set=$($e.Set())" } catch { "event=refused $($_.Exception.GetType().Name)" }
`

// TestServiceAccountToolCannotReachProductionWindow runs the probe as a real
// service-account tool process against a running disposable Agent_b whose PID
// is AGENTB_WINDOW_PROBE_PID. It is opt-in: it needs the capability suite's
// disposable roots and a running target. scripts/test-window-probe.ps1 drives it
// and then checks that the target is still running with nothing in its log.
func TestServiceAccountToolCannotReachProductionWindow(t *testing.T) {
	pid := os.Getenv("AGENTB_WINDOW_PROBE_PID")
	if pid == "" {
		t.Skip("set AGENTB_WINDOW_PROBE_PID with the capability suite's disposable roots")
	}
	workspaceParent, dataRoot := os.Getenv("AGENTB_CAPABILITY_WORKSPACE"), os.Getenv("AGENTB_CAPABILITY_DATA")
	if workspaceParent == "" || dataRoot == "" {
		t.Fatal("AGENTB_CAPABILITY_WORKSPACE and _DATA are required")
	}
	workspace, err := os.MkdirTemp(workspaceParent, "agentb-window-probe-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := removeCapabilityFixture(workspace); err != nil {
			t.Errorf("remove probe fixture: %v", err)
		}
	})
	cfg := config.Defaults(workspace)
	cfg.Shell.ServiceAccount.Enabled = true
	guard := false
	cfg.Shell.FileRoutingGuard = &guard
	shell := NewShell(cfg.Shell)
	shell.Configure(cfg)
	shell.SetCredentialStore(credential.New(dataRoot))
	item := &session.Session{ID: "window-probe", Workspace: workspace, LastSeen: map[string]time.Time{}}
	// run_script is how a model runs a multi-line PowerShell body as the service
	// account; the probe is exactly such a body.
	script := strings.ReplaceAll(windowProbeScript, "__PID__", pid)
	find := script[:strings.Index(script, "if ($hwnd")]
	runner := NewRunScript(shell)

	// The control only looks: an operator-identity process can still send a real
	// session end, which is outside this item.
	control, controlErr := runner.CallAsOperator(context.Background(), item, map[string]any{"language": "powershell", "source": find})
	t.Logf("operator control:\n%s", control)
	if controlErr != nil || !strings.Contains(control, "find=") || strings.Contains(control, "find=0 ") {
		t.Fatalf("the operator control could not find the window, so the probe proves nothing: err=%v", controlErr)
	}

	detail := runner.CallDetailed(context.Background(), item, map[string]any{"language": "powershell", "source": script})
	t.Logf("service account (override reason %q):\n%s", detail.OperatorOverrideReason, detail.Content)
	if detail.OperatorOverrideReason != "" || detail.Err != nil || !strings.Contains(detail.Content, "event=") {
		t.Fatalf("the probe did not run as the service account: reason=%q err=%v", detail.OperatorOverrideReason, detail.Err)
	}
	out := detail.Content
	// Either the window cannot be found at all, or every message to it fails.
	reached := strings.Contains(out, "post-close=True") || !(strings.Contains(out, "send-query=no-handle") || strings.Contains(out, "send-query=0")) || !(strings.Contains(out, "send-end=no-handle") || strings.Contains(out, "send-end=0"))
	if reached {
		t.Error("a service-account tool process reached production's window")
	}
	if !strings.Contains(out, "event=refused") {
		t.Error("a service-account tool process could open production's stop event")
	}
}
