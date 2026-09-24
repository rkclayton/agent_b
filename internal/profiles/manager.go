package profiles

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"harness/internal/config"
)

const settingsFile = "profile.json"

type Settings struct {
	Name          string               `json:"name"`
	Default       bool                 `json:"default"`
	Agents        []config.Agent       `json:"agents"`
	Deliver       config.Deliver       `json:"deliver"`
	Notifications config.Notifications `json:"notifications"`
}

type Manager struct {
	mu         sync.Mutex
	dataRoot   string
	configPath string
	cfg        *config.Config
}

var profileDirectories = []string{"attachments", "chats", "logs", "memory", "plans", "reflection", "scratch", "stats"}
var profileFiles = []string{"INBOX.md", "OUTBOX.md", "STATE.md", "workspace-state.json"}

func Open(dataRoot, configPath string, cfg *config.Config) (*Manager, bool, error) {
	manager := &Manager{dataRoot: dataRoot, configPath: configPath, cfg: cfg}
	if cfg.Profiles.Active != "" {
		if err := manager.loadLocked(cfg.Profiles.Active); err != nil {
			return nil, false, err
		}
		return manager, false, nil
	}
	name, err := currentUsername()
	if err != nil {
		return nil, false, err
	}
	if err := ValidateName(name); err != nil {
		return nil, false, fmt.Errorf("default profile name: %w", err)
	}
	root := manager.Root(name)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, false, err
	}
	if err := migrateExistingRoot(dataRoot, root); err != nil {
		return nil, false, err
	}
	cfg.LogDir = profileRelativePath(dataRoot, cfg.LogDir)
	cfg.Memory.Dir = profileRelativePath(dataRoot, cfg.Memory.Dir)
	cfg.Workspace = profileRelativePath(dataRoot, cfg.Workspace)
	cfg.Profiles = config.ProfileCatalog{Active: name, Names: []string{name}}
	settings := Settings{Name: name, Default: true, Agents: cloneAgents(cfg.Agents), Deliver: cfg.Deliver, Notifications: cfg.Notifications}
	if err := manager.writeSettings(settings); err != nil {
		return nil, false, err
	}
	if err := cfg.Save(configPath); err != nil {
		return nil, false, err
	}
	return manager, true, nil
}

func profileRelativePath(dataRoot, value string) string {
	if !filepath.IsAbs(value) {
		return value
	}
	relative, err := filepath.Rel(filepath.Clean(dataRoot), filepath.Clean(value))
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return value
	}
	return relative
}

func (manager *Manager) Root(name string) string {
	return filepath.Join(manager.dataRoot, "profiles", name)
}

func (manager *Manager) Active() string {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.cfg.Profiles.Active
}

func (manager *Manager) Names() []string {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return append([]string(nil), manager.cfg.Profiles.Names...)
}

func (manager *Manager) Create(name string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if err := ValidateName(name); err != nil {
		return err
	}
	if manager.existsLocked(name) {
		return fmt.Errorf("profile %q already exists", name)
	}
	settings := Settings{
		Name:          name,
		Agents:        cloneAgents(manager.cfg.Agents),
		Deliver:       config.Deliver{Mode: config.DeliverModeBoth, ExchangeFolder: `%USERPROFILE%\Agent_b\` + name},
		Notifications: manager.cfg.Notifications,
	}
	if err := os.MkdirAll(manager.Root(name), 0o700); err != nil {
		return err
	}
	if err := manager.writeSettings(settings); err != nil {
		return err
	}
	manager.cfg.Profiles.Names = append(manager.cfg.Profiles.Names, name)
	sort.Strings(manager.cfg.Profiles.Names)
	return manager.cfg.Save(manager.configPath)
}

func (manager *Manager) Switch(name string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !manager.existsLocked(name) {
		return fmt.Errorf("profile %q not found", name)
	}
	if err := manager.saveActiveLocked(); err != nil {
		return err
	}
	manager.cfg.Profiles.Active = name
	if err := manager.loadLocked(name); err != nil {
		return err
	}
	return manager.cfg.Save(manager.configPath)
}

// SaveActive persists the profile-scoped parts of the live configuration.
// The server uses it when Settings changes agents, delivery, or notifications.
func (manager *Manager) SaveActive() error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.saveActiveLocked()
}

func (manager *Manager) Rename(oldName, newName string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if err := ValidateName(newName); err != nil {
		return err
	}
	if !manager.existsLocked(oldName) {
		return fmt.Errorf("profile %q not found", oldName)
	}
	if manager.existsLocked(newName) {
		return fmt.Errorf("profile %q already exists", newName)
	}
	if oldName == manager.cfg.Profiles.Active {
		if err := manager.saveActiveLocked(); err != nil {
			return err
		}
	}
	if err := os.Rename(manager.Root(oldName), manager.Root(newName)); err != nil {
		return err
	}
	settings, err := manager.readSettings(newName)
	if err != nil {
		return err
	}
	settings.Name = newName
	if !settings.Default && strings.HasSuffix(settings.Deliver.ExchangeFolder, `\`+oldName) {
		settings.Deliver.ExchangeFolder = strings.TrimSuffix(settings.Deliver.ExchangeFolder, oldName) + newName
	}
	if err := manager.writeSettings(settings); err != nil {
		return err
	}
	for index, value := range manager.cfg.Profiles.Names {
		if strings.EqualFold(value, oldName) {
			manager.cfg.Profiles.Names[index] = newName
		}
	}
	if oldName == manager.cfg.Profiles.Active {
		manager.cfg.Profiles.Active = newName
		manager.apply(settings)
	}
	sort.Strings(manager.cfg.Profiles.Names)
	return manager.cfg.Save(manager.configPath)
}

func ValidateName(name string) error {
	if name == "" || name == "." || name == ".." || len(name) > 64 || strings.TrimSpace(name) != name || strings.ContainsAny(name, `<>:"/\|?*`) {
		return fmt.Errorf("profile name must be 1-64 filename-safe characters")
	}
	if strings.HasSuffix(name, ".") {
		return fmt.Errorf("profile name cannot end with a period")
	}
	return nil
}

func (manager *Manager) existsLocked(name string) bool {
	for _, value := range manager.cfg.Profiles.Names {
		if strings.EqualFold(value, name) {
			return true
		}
	}
	return false
}

func (manager *Manager) saveActiveLocked() error {
	current, err := manager.readSettings(manager.cfg.Profiles.Active)
	if err != nil {
		return err
	}
	current.Agents = cloneAgents(manager.cfg.Agents)
	current.Deliver = manager.cfg.Deliver
	current.Notifications = manager.cfg.Notifications
	return manager.writeSettings(current)
}

func (manager *Manager) loadLocked(name string) error {
	settings, err := manager.readSettings(name)
	if err != nil {
		return err
	}
	manager.apply(settings)
	return nil
}

func (manager *Manager) apply(settings Settings) {
	manager.cfg.Agents = cloneAgents(settings.Agents)
	manager.cfg.Deliver = settings.Deliver
	manager.cfg.Notifications = settings.Notifications
}

func (manager *Manager) readSettings(name string) (Settings, error) {
	data, err := os.ReadFile(filepath.Join(manager.Root(name), settingsFile))
	if err != nil {
		return Settings{}, err
	}
	var value Settings
	if err := json.Unmarshal(data, &value); err != nil {
		return Settings{}, err
	}
	return value, nil
}

func (manager *Manager) writeSettings(settings Settings) error {
	if err := os.MkdirAll(manager.Root(settings.Name), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(manager.Root(settings.Name), settingsFile), append(data, '\n'), 0o600)
}

func migrateExistingRoot(dataRoot, profileRoot string) error {
	for _, name := range profileDirectories {
		// The installer and launcher intentionally keep their diagnostic files
		// under the install-wide data root. On Windows an open file prevents the
		// logs directory itself from being renamed while the first application
		// process starts. Copy the existing diagnostics into the username profile
		// and leave that bootstrap directory in place; all application logs after
		// profile selection use the copied profile directory.
		if name == "logs" {
			if err := copyDirectoryIfPresent(filepath.Join(dataRoot, name), filepath.Join(profileRoot, name)); err != nil {
				return err
			}
			continue
		}
		if err := moveIfPresent(filepath.Join(dataRoot, name), filepath.Join(profileRoot, name)); err != nil {
			return err
		}
	}
	for _, name := range profileFiles {
		if err := moveIfPresent(filepath.Join(dataRoot, name), filepath.Join(profileRoot, name)); err != nil {
			return err
		}
	}
	return nil
}

func copyDirectoryIfPresent(source, target string) error {
	info, err := os.Stat(source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("profile migration source is not a directory: %s", source)
	}
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("profile migration target already exists: %s", target)
	} else if !os.IsNotExist(err) {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("profile migration refuses log symlink: %s", path)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		inputCloseErr := input.Close()
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if inputCloseErr != nil {
			return inputCloseErr
		}
		return closeErr
	})
}

func moveIfPresent(source, target string) error {
	if _, err := os.Stat(source); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("profile migration target already exists: %s", target)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	return os.Rename(source, target)
}

func currentUsername() (string, error) {
	value, err := user.Current()
	if err != nil {
		return "", err
	}
	name := value.Username
	if index := strings.LastIndexAny(name, `\/`); index >= 0 {
		name = name[index+1:]
	}
	return name, nil
}

func cloneAgents(values []config.Agent) []config.Agent {
	result := make([]config.Agent, len(values))
	copy(result, values)
	for index := range result {
		result[index].Toolset = append([]string(nil), result[index].Toolset...)
	}
	return result
}
