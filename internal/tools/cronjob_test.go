package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/cron"
	"harness/internal/session"
)

func TestCronjobHermesCallShape2qs(t *testing.T) {
	m := cron.New(t.TempDir(), func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.Local) }, nil)
	tool := NewCronjob(m)
	item := &session.Session{ToolsEnabled: map[string]bool{"cronjob": true}}
	result, err := tool.Call(context.Background(), item, map[string]any{"action": "create", "schedule": "every 6h", "prompt": "check it", "deliver": "origin"})
	if err != nil || !strings.Contains(result, "created") || len(m.Jobs()) != 1 || m.Jobs()[0].Deliver != "origin" {
		t.Fatalf("result=%q jobs=%+v err=%v", result, m.Jobs(), err)
	}
	if got := tool.Name(); got != "cronjob" {
		t.Fatalf("name=%s", got)
	}
}

func TestScheduledContextAloneRefusesCronjob2zc(t *testing.T) {
	ctx := context.Background()
	manager := cron.New(t.TempDir(), time.Now, nil)
	created, err := manager.Apply(ctx, cron.Args{Action: "create", Schedule: "in 1h", Prompt: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	tool := NewCronjob(manager)
	args := map[string]any{"action": "remove", "job_id": created.Job.ID}
	for _, item := range []*session.Session{{Origin: "scheduled"}, {Origin: "delegate"}} {
		if _, err := tool.Call(session.WithScheduledRun(ctx), item, args); err == nil {
			t.Fatal("scheduled context changed a job")
		}
	}
	if _, err := tool.Call(ctx, &session.Session{Origin: "scheduled"}, args); err != nil {
		t.Fatal(err)
	}
	if len(cron.New(filepath.Dir(manager.Root()), time.Now, nil).Jobs()) != 0 {
		t.Fatal("removed job remains")
	}
}
