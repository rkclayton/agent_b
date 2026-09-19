//go:build windows

package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/events"
	"harness/internal/session"
)

// Item 2fq: a junction needs no privilege, and filepath.EvalSymlinks does not
// follow one, so a b-session's folder can hold a link whose text stays inside
// the folder while the file system lands in the plans folder. The write is
// refused with the service identity on and off; the d session bound to the
// plan still writes it through its real path.
func TestPlansFolderWriteThroughAJunctionIsRefusedInBothPostures(t *testing.T) {
	root := t.TempDir()
	plans := filepath.Join(root, "plans")
	planDir := filepath.Join(plans, "p1")
	workspace := filepath.Join(root, "scratch", "s1")
	for _, dir := range []string{planDir, workspace} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	planFile := filepath.Join(planDir, "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan\n- [ ] one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace, "link")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, planDir).CombinedOutput(); err != nil {
		t.Fatalf("the suite's account could not create a junction: %v %s", err, out)
	}
	if real, err := session.RealPath(filepath.Join(link, "plan.md")); err != nil || !strings.HasPrefix(strings.ToLower(real), strings.ToLower(plans)+string(filepath.Separator)) {
		t.Fatalf("the junction does not resolve into the plans folder: %q %v", real, err)
	}

	coordinator := NewFileCoordinator(session.NewWorkspaceRegistry(), func(string) string { return "b" }, events.NewBus())
	write, edit := NewWriteFile(coordinator), NewEditFile(coordinator)
	chat := &session.Session{ID: "s1", Role: "b", Workspace: workspace, PlansRoot: plans, LastSeen: map[string]time.Time{}, ToolsEnabled: map[string]bool{"write_file": true, "edit_file": true}}
	postures := map[string]context.Context{"service identity": context.Background(), "run as you": withOSPathPolicy(context.Background())}
	for name, ctx := range postures {
		for _, path := range []string{"link/plan.md", "link/new-item.md", filepath.Join(link, "plan.md")} {
			if _, err := write.Call(ctx, chat, map[string]any{"path": path, "content": "rewritten\n"}); err == nil {
				t.Fatalf("%s: write_file reached the plans folder through %q", name, path)
			}
			chat.Touch("link/plan.md")
			if _, err := edit.Call(ctx, chat, map[string]any{"path": path, "old_string": "- [ ] one", "new_string": "- [x] one"}); err == nil {
				t.Fatalf("%s: edit_file reached the plans folder through %q", name, path)
			}
		}
		if _, err := os.Stat(filepath.Join(planDir, "new-item.md")); err == nil {
			t.Fatalf("%s: a new file appeared in the plan", name)
		}
		if _, err := write.Call(ctx, chat, map[string]any{"path": "own.txt", "content": "ok\n"}); err != nil {
			t.Fatalf("%s: the chat lost its own folder: %v", name, err)
		}
	}
	if data, _ := os.ReadFile(planFile); string(data) != "# Plan\n- [ ] one\n" {
		t.Fatalf("the plan changed: %q", data)
	}

	planner := &session.Session{ID: "d1", Role: "d", PlanID: "p1", PlanDir: planDir, PlansRoot: plans, Workspace: workspace, LastSeen: map[string]time.Time{}, ToolsEnabled: map[string]bool{"write_file": true, "edit_file": true}}
	planner.Touch("plan.md")
	if _, err := edit.Call(context.Background(), planner, map[string]any{"path": planFile, "old_string": "- [ ] one", "new_string": "- [ ] one\n- [ ] two"}); err != nil {
		t.Fatalf("the bound d session could not write its own plan through its real path: %v", err)
	}
}
