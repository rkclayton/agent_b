package tools

import (
	"context"
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
