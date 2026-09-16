package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"harness/internal/events"
	"harness/internal/session"
)

// A worker the operator has granted file tools as themselves still cannot write
// plan text: a relative path that climbs out of its repository into the plans
// folder, or an absolute one, is refused, while its repository stays writable.
// Without this the worker could rewrite its own item file's verify: line.
func TestWorkerUnderOperatorGrantCannotWritePlanText(t *testing.T) {
	root := t.TempDir()
	plans := filepath.Join(root, "plans")
	itemDir := filepath.Join(plans, "p1", "plan", "items")
	repo := filepath.Join(root, "repo")
	for _, dir := range []string{itemDir, repo} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	item := filepath.Join(itemDir, "2aa.md")
	if err := os.WriteFile(item, []byte("state: live\nverify: go test ./...\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worker := &session.Session{ID: "c1", Role: "c", PlanID: "p1", PlanDir: filepath.Join(plans, "p1"), PlansRoot: plans, PlanRepo: repo, Workspace: repo, LastSeen: map[string]time.Time{}, ToolsEnabled: map[string]bool{"write_file": true, "edit_file": true}}
	coordinator := NewFileCoordinator(session.NewWorkspaceRegistry(), func(string) string { return "worker" }, events.NewBus())
	write, edit := NewWriteFile(coordinator), NewEditFile(coordinator)
	operator := withOSPathPolicy(context.Background())

	climb := filepath.Join("..", "plans", "p1", "plan", "items", "2aa.md")
	for _, path := range []string{climb, item} {
		if _, err := write.Call(operator, worker, map[string]any{"path": path, "content": "state: live\nverify: exit 0\n"}); err == nil {
			t.Fatalf("write_file as operator reached plan text through %q", path)
		}
		if _, err := edit.Call(operator, worker, map[string]any{"path": path, "old_string": "go test ./...", "new_string": "exit 0"}); err == nil {
			t.Fatalf("edit_file as operator reached plan text through %q", path)
		}
	}
	if data, _ := os.ReadFile(item); string(data) != "state: live\nverify: go test ./...\n" {
		t.Fatalf("the item file changed: %q", data)
	}
	if _, err := write.Call(operator, worker, map[string]any{"path": "NOTICE", "content": "ok\n"}); err != nil {
		t.Fatalf("the worker lost its own repository under the grant: %v", err)
	}
}
