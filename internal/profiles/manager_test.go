package profiles

import (
	"os"
	"path/filepath"
	"testing"

	"harness/internal/config"
)

func TestOpenMigratesProfileDataAndLeavesSharedState(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(t.TempDir())
	configPath := filepath.Join(root, "harness.json")
	if err := cfg.Save(configPath); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"chats/chat.jsonl", "memory/workspace.md", "plans/p1/plan.md", "stats/ledger.json", "attachments/a.txt", "INBOX.md"} {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(relative), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "shared-marker.json"), []byte("shared"), 0o600); err != nil {
		t.Fatal(err)
	}

	manager, migrated, err := Open(root, configPath, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !migrated || manager.Active() == "" {
		t.Fatalf("migrated=%t active=%q", migrated, manager.Active())
	}
	for _, relative := range []string{"chats/chat.jsonl", "memory/workspace.md", "plans/p1/plan.md", "stats/ledger.json", "attachments/a.txt", "INBOX.md", settingsFile} {
		if _, err := os.Stat(filepath.Join(manager.Root(manager.Active()), relative)); err != nil {
			t.Fatalf("profile path %s: %v", relative, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "shared-marker.json")); err != nil {
		t.Fatalf("shared state moved: %v", err)
	}
	if cfg.Deliver.ExchangeFolder != filepath.Join(homeDirectory(t), "Agent_b") {
		t.Fatalf("default exchange moved: %q", cfg.Deliver.ExchangeFolder)
	}
}

func homeDirectory(t *testing.T) string {
	t.Helper()
	value, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestCreateSwitchAndRenameKeepProfileSettingsIsolated(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(t.TempDir())
	configPath := filepath.Join(root, "harness.json")
	if err := cfg.Save(configPath); err != nil {
		t.Fatal(err)
	}
	manager, _, err := Open(root, configPath, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	first := manager.Active()
	cfg.Agents[0].Name = "First agent"
	if err := manager.Create("Second"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Switch("Second"); err != nil {
		t.Fatal(err)
	}
	if manager.Active() != "Second" || cfg.Deliver.ExchangeFolder != `%USERPROFILE%\Agent_b\Second` {
		t.Fatalf("active=%q exchange=%q", manager.Active(), cfg.Deliver.ExchangeFolder)
	}
	cfg.Agents[0].Name = "Second agent"
	if err := manager.Switch(first); err != nil {
		t.Fatal(err)
	}
	if cfg.Agents[0].Name != "First agent" {
		t.Fatalf("first settings lost: %+v", cfg.Agents)
	}
	if err := manager.Rename("Second", "Renamed"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(manager.Root("Renamed")); err != nil {
		t.Fatal(err)
	}
	if err := manager.Switch("Renamed"); err != nil {
		t.Fatal(err)
	}
	if cfg.Agents[0].Name != "Second agent" || cfg.Deliver.ExchangeFolder != `%USERPROFILE%\Agent_b\Renamed` {
		t.Fatalf("renamed settings=%+v exchange=%q", cfg.Agents, cfg.Deliver.ExchangeFolder)
	}
}
