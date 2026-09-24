package agent

import (
	"context"
	"errors"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
)

type unprovisionedTool struct {
	name     string
	normal   int
	override int
}

func (t *unprovisionedTool) Name() string         { return t.name }
func (*unprovisionedTool) Description() string    { return "test" }
func (*unprovisionedTool) Schema() map[string]any { return map[string]any{} }
func (*unprovisionedTool) PreflightServiceIdentity() error {
	return errors.New("credential is not stored")
}
func (t *unprovisionedTool) Call(context.Context, *session.Session, map[string]any) (string, error) {
	t.normal++
	return "normal", nil
}
func (t *unprovisionedTool) CallAsOperator(context.Context, *session.Session, map[string]any) (string, error) {
	t.override++
	return "readable", nil
}

func TestUnprovisionedServiceIdentityRefusesWritesButAllowsReads2jz(t *testing.T) {
	cfg := config.Defaults(t.TempDir())
	write := &unprovisionedTool{name: "write_file"}
	read := &unprovisionedTool{name: "read_file"}
	runner := &Runner{bus: events.NewBus(), tools: tools.New(write, read), cfg: func() config.Config { return cfg }}
	runner.gate = NewGate(runner.bus, runner.cfg)
	item := &session.Session{ID: "setup", Workspace: t.TempDir(), ToolsEnabled: map[string]bool{"write_file": true, "read_file": true}}

	blocked := runner.executeTool(context.Background(), item, "run", "write", "write_file", map[string]any{"path": "x", "content": "x"})
	if blocked.OK || blocked.Content != "error: service identity not set up" || write.normal != 0 || write.override != 0 {
		t.Fatalf("blocked=%+v normal=%d override=%d", blocked, write.normal, write.override)
	}
	allowed := runner.executeTool(context.Background(), item, "run", "read", "read_file", map[string]any{"path": "x"})
	if !allowed.OK || allowed.Content != "readable" || read.normal != 0 || read.override != 1 {
		t.Fatalf("allowed=%+v normal=%d override=%d", allowed, read.normal, read.override)
	}
}
