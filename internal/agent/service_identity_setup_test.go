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

func TestUnprovisionedServiceIdentityAllowsScratchAndOperatorWindow2kc(t *testing.T) {
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
	item.Scratch = true
	scratch := runner.executeTool(context.Background(), item, "run", "scratch", "write_file", map[string]any{"path": "x", "content": "x"})
	if !scratch.OK || scratch.Content != "normal" || write.normal != 1 || write.override != 0 {
		t.Fatalf("scratch=%+v normal=%d override=%d", scratch, write.normal, write.override)
	}
	item.Scratch = false
	cfg.Shell.OperatorContext = true
	operator := runner.executeTool(context.Background(), item, "run", "operator", "write_file", map[string]any{"path": "x", "content": "x"})
	if !operator.OK || operator.Content != "normal" || write.normal != 2 || write.override != 0 {
		t.Fatalf("operator=%+v normal=%d override=%d", operator, write.normal, write.override)
	}
	cfg.Shell.OperatorContext = false
	allowed := runner.executeTool(context.Background(), item, "run", "read", "read_file", map[string]any{"path": "x"})
	if !allowed.OK || allowed.Content != "readable" || read.normal != 0 || read.override != 1 {
		t.Fatalf("allowed=%+v normal=%d override=%d", allowed, read.normal, read.override)
	}
}

func TestServiceIdentityUnavailableCarriesBothActions2kc(t *testing.T) {
	data := serviceIdentityUnavailableData("credential is not stored")
	actions, ok := data["actions"].([]string)
	if !ok || len(actions) != 2 || actions[0] != "provision" || actions[1] != "operator_mode" {
		t.Fatalf("actions=%#v", data["actions"])
	}
}

func TestDisabledServiceIdentityRunsMutatingToolsWithoutRefusal2ks(t *testing.T) {
	cfg := config.Defaults(t.TempDir())
	cfg.Shell.ServiceAccount.Enabled = false
	write := &unprovisionedTool{name: "write_file"}
	shell := &unprovisionedTool{name: "shell"}
	runner := &Runner{bus: events.NewBus(), tools: tools.New(write, shell), cfg: func() config.Config { return cfg }}
	runner.gate = NewGate(runner.bus, runner.cfg)
	item := &session.Session{ID: "disabled", Workspace: t.TempDir(), ToolsEnabled: map[string]bool{"write_file": true, "shell": true}}
	for _, name := range []string{"write_file", "shell"} {
		outcome := runner.executeTool(context.Background(), item, "run", name, name, map[string]any{})
		if !outcome.OK || outcome.Content != "normal" {
			t.Fatalf("%s outcome=%+v", name, outcome)
		}
	}
}

func TestServiceIdentityInvitationPublishesOncePerRunner2ks(t *testing.T) {
	bus := events.NewBus()
	runner := &Runner{bus: bus}
	eventsSeen, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	publish := func() {
		if runner.identityInvitation.CompareAndSwap(false, true) {
			bus.Publish(events.New(events.ServiceIdentityUnavailable, "s", "r", serviceIdentityUnavailableData("missing")))
		}
	}
	publish()
	publish()
	select {
	case event := <-eventsSeen:
		if event.Type != events.ServiceIdentityUnavailable {
			t.Fatalf("event=%s", event.Type)
		}
	default:
		t.Fatal("invitation was not published")
	}
	select {
	case event := <-eventsSeen:
		t.Fatalf("second invitation=%+v", event)
	default:
	}
}
