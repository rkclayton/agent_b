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

// Item 2ly, acceptance line one: TWO PROFILES DIFFER IN A PER-PROFILE SETTING,
// AGREE ON A MACHINE-WIDE ONE, AND BOTH SURVIVE A RESTART.
//
// The config package proves the extract/apply rules in isolation. What it cannot
// prove is the round trip through disk and back into a NEW manager, which is the
// only form the operator ever meets: the difference has to be in the profile's
// own file, the shared value has to stay in the machine's, and a manager opened
// fresh has to put them back the same way.
func TestTwoProfilesDifferPerProfileAndAgreeMachineWideAcrossARestart2ly(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(t.TempDir())
	configPath := filepath.Join(root, "harness.json")
	cfg.Listen = "127.0.0.1:9123" // machine-wide: one port, one machine
	if err := cfg.Save(configPath); err != nil {
		t.Fatal(err)
	}
	manager, _, err := Open(root, configPath, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	first := manager.Active()

	// A per-profile setting, from 2lj (d)'s typography.
	cfg.Chat.TextSize = "large"
	if err := manager.SaveActive(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Create("Second"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Switch("Second"); err != nil {
		t.Fatal(err)
	}
	// The new profile INHERITED rather than copied, so it reads the machine's
	// value and not the first profile's.
	if cfg.Chat.TextSize == "large" {
		t.Fatalf("the second profile copied the first profile's text size")
	}
	cfg.Chat.TextSize = "small"
	if err := manager.SaveActive(); err != nil {
		t.Fatal(err)
	}

	// THE RESTART: a second manager over the same roots, with a configuration
	// loaded from the machine file the way startup loads it.
	restarted, _, _, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	restarted.Profiles.Active = "Second"
	reopened, _, err := Open(root, configPath, restarted)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Chat.TextSize != "small" {
		t.Errorf("after a restart the second profile reads text size %q, want small", restarted.Chat.TextSize)
	}
	if restarted.Listen != "127.0.0.1:9123" {
		t.Errorf("the machine-wide port did not survive: %q", restarted.Listen)
	}
	if err := reopened.Switch(first); err != nil {
		t.Fatal(err)
	}
	if restarted.Chat.TextSize != "large" {
		t.Errorf("after a restart the first profile reads text size %q, want large", restarted.Chat.TextSize)
	}
	if restarted.Listen != "127.0.0.1:9123" {
		t.Errorf("switching profiles moved a machine-wide setting: %q", restarted.Listen)
	}
	// And the difference is stored as a DISAGREEMENT, not a copy of everything:
	// the profile file names chat and nothing it never expressed an opinion about.
	settings, err := reopened.readSettings("Second")
	if err != nil {
		t.Fatal(err)
	}
	if _, present := settings.Overlay["chat"]; !present {
		t.Error("the profile's own file does not carry the setting it differs in")
	}
	for _, key := range []string{"listen", "workspace", "run", "context", "memory"} {
		if _, present := settings.Overlay[key]; present {
			t.Errorf("the profile froze %q although it never disagreed about it", key)
		}
	}
}
