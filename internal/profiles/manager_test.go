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
	cfg.LogDir = filepath.Join(root, "logs")
	cfg.Memory.Dir = filepath.Join(root, "memory")
	cfg.Workspace = filepath.Join(root, "scratch")
	configPath := filepath.Join(root, "harness.json")
	if err := cfg.Save(configPath); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"chats/chat.jsonl", "logs/bootstrap.log", "memory/workspace.md", "plans/p1/plan.md", "stats/ledger.json", "attachments/a.txt", "INBOX.md"} {
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
	for _, relative := range []string{"chats/chat.jsonl", "logs/bootstrap.log", "memory/workspace.md", "plans/p1/plan.md", "stats/ledger.json", "attachments/a.txt", "INBOX.md", settingsFile} {
		if _, err := os.Stat(filepath.Join(manager.Root(manager.Active()), relative)); err != nil {
			t.Fatalf("profile path %s: %v", relative, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "logs", "bootstrap.log")); err != nil {
		t.Fatalf("bootstrap logs were not retained at the install root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "shared-marker.json")); err != nil {
		t.Fatalf("shared state moved: %v", err)
	}
	if cfg.Deliver.ExchangeFolder != filepath.Join(homeDirectory(t), "Agent_b") {
		t.Fatalf("default exchange moved: %q", cfg.Deliver.ExchangeFolder)
	}
	if cfg.Workspace != "scratch" || cfg.LogDir != "logs" || cfg.Memory.Dir != "memory" {
		t.Fatalf("profile paths were not rebased: workspace=%q log=%q memory=%q", cfg.Workspace, cfg.LogDir, cfg.Memory.Dir)
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
