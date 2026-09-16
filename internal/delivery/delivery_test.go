package delivery

import (
	"os"
	"path/filepath"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
)

func TestExchangeCopyCollisionAndSHA256Dedupe(t *testing.T) {
	workspace := t.TempDir()
	exchange := filepath.Join(t.TempDir(), "exchange")
	if err := os.MkdirAll(filepath.Join(workspace, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(exchange, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(workspace, "reports", "final.txt")
	if err := os.WriteFile(source, []byte("new result"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(exchange, "final.txt")
	if err := os.WriteFile(base, []byte("existing result"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults(workspace)
	cfg.Deliver.Mode = config.DeliverModeBoth
	cfg.Deliver.ExchangeFolder = exchange
	bus := events.NewBus()
	stream, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	manager := New(bus, func() config.Config { return cfg })
	s := &session.Session{ID: "main", Workspace: workspace}

	first := manager.Deliver(s, "r1", []Source{{Path: "reports/final.txt", Bytes: 10}})
	if len(first.Items) != 1 || first.Items[0].Status != "copied" || first.Items[0].ExchangePath != "final (2).txt" {
		t.Fatalf("first delivery=%+v", first)
	}
	copyPath := filepath.Join(exchange, "final (2).txt")
	if got, err := os.ReadFile(copyPath); err != nil || string(got) != "new result" {
		t.Fatalf("collision copy=%q err=%v", got, err)
	}
	if got, _ := os.ReadFile(base); string(got) != "existing result" {
		t.Fatalf("base overwritten: %q", got)
	}

	second := manager.Deliver(s, "r2", []Source{{Path: "reports/final.txt", Bytes: 10}})
	if len(second.Items) != 1 || second.Items[0].Status != "identical" || second.Items[0].ExchangePath != "final (2).txt" {
		t.Fatalf("second delivery=%+v", second)
	}
	if _, err := os.Stat(filepath.Join(exchange, "final (3).txt")); !os.IsNotExist(err) {
		t.Fatalf("dedupe created an extra copy: %v", err)
	}
	firstEvent, secondEvent := <-stream, <-stream
	if firstEvent.Type != events.FilesDelivered || secondEvent.RunID != "r2" {
		t.Fatalf("delivery events=%+v %+v", firstEvent, secondEvent)
	}
}

func TestChipsModeLogsRunWithoutCreatingExchangeFolder(t *testing.T) {
	workspace := t.TempDir()
	exchange := filepath.Join(t.TempDir(), "not-created")
	cfg := config.Defaults(workspace)
	cfg.Deliver.Mode = config.DeliverModeChips
	cfg.Deliver.ExchangeFolder = exchange
	bus := events.NewBus()
	stream, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	result := New(bus, func() config.Config { return cfg }).Deliver(&session.Session{ID: "main", Workspace: workspace}, "r1", nil)
	if result.Mode != config.DeliverModeChips || len(result.Items) != 0 {
		t.Fatalf("result=%+v", result)
	}
	if _, err := os.Stat(exchange); !os.IsNotExist(err) {
		t.Fatalf("chips mode created exchange folder: %v", err)
	}
	if event := <-stream; event.Type != events.FilesDelivered || event.RunID != "r1" {
		t.Fatalf("event=%+v", event)
	}
}
