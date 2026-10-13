package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"harness/internal/credential"
	"os"
	"path/filepath"
	"strings"
)

func Load(path string) (*Config, bool, bool, error) {
	return LoadWithTemplate(path, filepath.Join(filepath.Dir(path), "harness.example.json"))
}

// LoadWithTemplate loads the live configuration from path and, on first run,
// materializes it from the explicitly supplied application template.
func LoadWithTemplate(path, examplePath string) (*Config, bool, bool, error) {
	return LoadWithRoots(path, examplePath, filepath.Dir(path))
}

// LoadWithRoots resolves connection credential references against the explicit
// operator data root rather than deriving their location from the config path.
func LoadWithRoots(path, examplePath, dataRoot string) (*Config, bool, bool, error) {
	data, err := os.ReadFile(path)
	created := false
	if os.IsNotExist(err) {
		data, err = os.ReadFile(examplePath)
		if err != nil {
			return nil, false, false, fmt.Errorf("%s is missing; read %s: %w", filepath.Base(path), filepath.Base(examplePath), err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, false, false, fmt.Errorf("create config directory: %w", err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, false, false, fmt.Errorf("create %s from %s: %w", filepath.Base(path), filepath.Base(examplePath), err)
		}
		created = true
	}
	if err != nil {
		return nil, false, false, err
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	var metadata struct {
		ConfigVersion *int `json:"config_version"`
		Run           struct {
			MaxTurns            *int `json:"max_turns"`
			MaxWallClockSeconds *int `json:"max_wall_clock_seconds"`
			MaxToolCalls        *int `json:"max_tool_calls"`
		} `json:"run"`
		Approval struct {
			Mode string `json:"mode"`
		} `json:"approval"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, false, created, err
	}
	if metadata.Run.MaxTurns != nil && *metadata.Run.MaxTurns == 0 {
		return nil, false, created, fmt.Errorf("run.max_turns: zero is not unlimited; omit it for the default %d or use a positive pathological-case backstop", DefaultMaxTurns)
	}
	if metadata.Run.MaxWallClockSeconds != nil && *metadata.Run.MaxWallClockSeconds == 0 {
		return nil, false, created, fmt.Errorf("run.max_wall_clock_seconds: zero is not unlimited; omit it for the default %d or use a positive backstop", DefaultMaxWallClockSeconds)
	}
	if metadata.Run.MaxToolCalls != nil && *metadata.Run.MaxToolCalls == 0 {
		return nil, false, created, fmt.Errorf("run.max_tool_calls: zero is not unlimited; omit it for the default %d or use a positive backstop", DefaultMaxToolCalls)
	}
	unstamped := metadata.ConfigVersion == nil
	if !unstamped && *metadata.ConfigVersion != 2 && *metadata.ConfigVersion != 3 && *metadata.ConfigVersion != 4 && *metadata.ConfigVersion != 5 && *metadata.ConfigVersion != 6 && *metadata.ConfigVersion != 7 && *metadata.ConfigVersion != 8 && *metadata.ConfigVersion != 9 && *metadata.ConfigVersion != 10 && *metadata.ConfigVersion != 11 && *metadata.ConfigVersion != 12 && *metadata.ConfigVersion != 13 && *metadata.ConfigVersion != 14 && *metadata.ConfigVersion != CurrentConfigVersion {
		return nil, false, created, fmt.Errorf("config_version: unsupported value %d (current %d)", *metadata.ConfigVersion, CurrentConfigVersion)
	}
	migrated, data, err := migrateV1(data)
	if err != nil {
		return nil, false, created, err
	}
	version := 0
	if metadata.ConfigVersion != nil {
		version = *metadata.ConfigVersion
	}
	connectionKeyMigrated, data, err := migrateConnectionKey(data)
	if err != nil {
		return nil, false, created, err
	}
	schemaMigrated, idleTimeoutRemapped, data, err := migrateOperatorIdleTimeout(data, version)
	if err != nil {
		return nil, false, created, err
	}
	byteWindowMigrated, data, err := migrateByteWindows(data, version)
	if err != nil {
		return nil, false, created, err
	}
	modelRolesMigrated, data, err := migrateModelConnections(data, version)
	if err != nil {
		return nil, false, created, err
	}
	agentsMigrated, data, err := migrateAgentObjects(data, version)
	if err != nil {
		return nil, false, created, err
	}
	connectionModelsMigrated, data, err := migrateConnectionModels(data, version)
	if err != nil {
		return nil, false, created, err
	}
	webSearchMigrated, data, err := migrateWebSearch(data, version)
	if err != nil {
		return nil, false, created, err
	}
	// THE PLACEHOLDER LEAVES DISK. Once, named, and recorded by the
	// config version, so it cannot come back from a load.
	placeholderCleared, data, err := migrateModelPlaceholder(data, version)
	if err != nil {
		return nil, false, created, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, false, created, err
	}
	textSizeMigrated := false
	if named, ok := cfg.Chat.TextSize.(string); ok {
		if named == "" {
			cfg.Chat.TextSize = nil
		} else if pixels, valid := ChatTextPixels(named); valid {
			cfg.Chat.TextSize = pixels
			textSizeMigrated = true
		}
	}
	retiredDenyDomainsMigrated := version < 12
	if retiredDenyDomainsMigrated {
		retired := map[string]bool{"ipinfo.io": true, "ipapi.co": true, "ip-api.com": true, "ifconfig.me": true, "ipify.org": true, "geojs.io": true, "ipgeolocation.io": true, "icanhazip.com": true}
		kept := cfg.Tools.Fetch.DenyDomains[:0]
		for _, domain := range cfg.Tools.Fetch.DenyDomains {
			if !retired[strings.ToLower(strings.TrimSpace(domain))] {
				kept = append(kept, domain)
			}
		}
		cfg.Tools.Fetch.DenyDomains = kept
	}
	// Operator context is a launch-scoped state, never a startup instruction.
	cfg.Approval.Unattended = false
	cfg.Shell.OperatorContext = false
	cfg.Shell.OperatorContextExpiresAt = ""
	applyDefaults(&cfg)
	skillsMigrated := false
	for index := range cfg.Agents {
		if cfg.Agents[index].Skills != nil {
			continue
		}
		cfg.Agents[index].Skills = cloneSkillSettings(cfg.Skills)
		// An empty legacy map has no state to carry and remains omitted on disk.
		// Only copying actual global choices is a persisted migration.
		if len(cfg.Skills) > 0 {
			skillsMigrated = true
		}
	}
	builtInAgentMigrated := version < 14 && len(cfg.Agents) > 0 && AgentID(cfg.Agents[0].Name) != "agent_b"
	if builtInAgentMigrated {
		cfg.Agents[0].Name = "agent_b"
	}
	profilesMigrated := version < 15 && len(cfg.Agents) > 0
	if profilesMigrated {
		migrateAgentProfiles(&cfg)
	}
	// a configuration written before the merge names the two
	// tools that became `search`. It meant "this agent may search", so it is
	// read that way rather than refused as unknown.
	for i := range cfg.Agents {
		cfg.Agents[i].Toolset = migrateToolset(cfg.Agents[i].Toolset)
	}
	approvalDefaultCorrected := unstamped && metadata.Approval.Mode == ApprovalModeMutating
	if approvalDefaultCorrected {
		cfg.Approval.Mode = ApprovalModeBoundaryOnly
	}
	if err := cfg.Validate(); err != nil {
		return nil, false, created, err
	}
	if err := ResolveConnectionCredentials(&cfg, dataRoot); err != nil {
		return nil, false, created, err
	}
	if migrated || connectionKeyMigrated || schemaMigrated || connectionModelsMigrated || byteWindowMigrated || modelRolesMigrated || agentsMigrated || builtInAgentMigrated || profilesMigrated || skillsMigrated || webSearchMigrated || len(placeholderCleared) > 0 || retiredDenyDomainsMigrated || textSizeMigrated || unstamped {
		if err := cfg.Save(path); err != nil {
			return nil, false, created, err
		}
	}
	if approvalDefaultCorrected {
		cfg.LoadNotices = append(cfg.LoadNotices, ApprovalDefaultMigrationNotice)
	}
	if idleTimeoutRemapped {
		cfg.LoadNotices = append(cfg.LoadNotices, OperatorIdleTimeoutMigrationNotice)
	}
	if byteWindowMigrated && !unstamped {
		cfg.LoadNotices = append(cfg.LoadNotices, ByteWindowMigrationNotice)
	}
	if modelRolesMigrated && !unstamped {
		cfg.LoadNotices = append(cfg.LoadNotices, ModelRolesMigrationNotice)
	}
	if agentsMigrated && !unstamped {
		cfg.LoadNotices = append(cfg.LoadNotices, AgentObjectsMigrationNotice)
	}
	// one line, naming the connections it cleared. It says which ones
	// because the user's question was which ones.
	if len(placeholderCleared) > 0 {
		cfg.LoadNotices = append(cfg.LoadNotices, ModelPlaceholderMigrationNotice+strings.Join(placeholderCleared, ", "))
	}
	return &cfg, migrated || connectionKeyMigrated || schemaMigrated || connectionModelsMigrated || byteWindowMigrated || modelRolesMigrated || agentsMigrated || profilesMigrated || skillsMigrated || webSearchMigrated || len(placeholderCleared) > 0 || retiredDenyDomainsMigrated || textSizeMigrated, created, nil
}

func cloneSkillSettings(source map[string]SkillSetting) map[string]SkillSetting {
	result := make(map[string]SkillSetting, len(source))
	for name, setting := range source {
		result[name] = setting
	}
	return result
}

func (c Config) Save(path string) error {
	persisted := c
	persisted.Agents = append([]Agent{}, c.Agents...)
	persisted.Connections = make([]Connection, len(c.Connections))
	copy(persisted.Connections, c.Connections)
	for _, agent := range persisted.Agents {
		if agent.B != "" || agent.Model != "" {
			migrateAgentProfiles(&persisted)
			break
		}
	}
	for i := range persisted.Connections {
		persisted.Connections[i].APIKey = ""
	}
	persisted.ConfigVersion = CurrentConfigVersion
	if err := persisted.Validate(); err != nil {
		return err
	}
	// A named value can only arrive from an older in-memory profile or test.
	// Normalize it, while preserving the legacy empty value used for an
	// intentionally unset machine-level profile default.
	if named, ok := persisted.Chat.TextSize.(string); ok && named != "" {
		persisted.Chat.TextSize, _ = ChatTextPixels(named)
	}
	// a retired auth mode is never written back. The migration rewrites
	// each one before this is reached, so getting here means something tried to keep it.
	for name, service := range persisted.Services {
		if err := ValidateNewServiceAuth(service.Auth); err != nil {
			return fmt.Errorf("services.%s.auth: %w", name, err)
		}
	}
	persisted.Shell.OperatorContext = false
	persisted.Shell.OperatorContextExpiresAt = ""
	persisted.Approval.Unattended = false
	data, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if current, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(current, data) {
		return nil
	}
	return os.WriteFile(path, data, 0o600)
}

func ResolveConnectionCredentials(cfg *Config, dataRoot string) error {
	for i := range cfg.Connections {
		connection := &cfg.Connections[i]
		if connection.APIKey == "" && connection.Credential == "" {
			continue
		}
		if connection.Credential == "" {
			connection.Credential = connection.ID
		}
		store, err := credential.NewNamed(dataRoot, connection.Credential)
		if err != nil {
			return fmt.Errorf("connections[%d].credential: %w", i, err)
		}
		if connection.APIKey != "" {
			if err := store.Write([]byte(connection.APIKey)); err != nil {
				return fmt.Errorf("store credential %q: %w", connection.Credential, err)
			}
			continue
		}
		value, err := store.Read()
		if err != nil {
			if errors.Is(err, credential.ErrNotStored) {
				return fmt.Errorf("connections[%d].credential: named credential %q is not stored", i, connection.Credential)
			}
			return fmt.Errorf("connections[%d].credential: %w", i, err)
		}
		connection.APIKey = string(value)
		clearBytes(value)
	}
	return nil
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
