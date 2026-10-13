package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

// migrateAgentProfiles carries schema 14's per-agent connection choices into
// the per-profile connection records introduced by schema 15.
func migrateAgentProfiles(cfg *Config) {
	byConnection := map[string][]int{}
	for index := range cfg.Agents {
		if cfg.Agents[index].B != "" {
			byConnection[cfg.Agents[index].B] = append(byConnection[cfg.Agents[index].B], index)
		}
	}
	used := map[string]bool{}
	for _, connection := range cfg.Connections {
		used[connection.ID] = true
	}
	for connectionID, agentIndexes := range byConnection {
		baseIndex := -1
		for index := range cfg.Connections {
			if cfg.Connections[index].ID == connectionID {
				baseIndex = index
				break
			}
		}
		if baseIndex < 0 {
			continue
		}
		owner := agentIndexes[0]
		for _, index := range agentIndexes {
			if AgentID(cfg.Agents[index].Name) == "agent_b" {
				owner = index
				break
			}
		}
		base := cfg.Connections[baseIndex]
		assign := func(connection *Connection, agent *Agent) {
			profile := AgentID(agent.Name)
			if profile == "agent_b" {
				profile = ""
			}
			connection.AgentProfile = profile
			if strings.TrimSpace(agent.Model) != "" {
				connection.SelectModel(agent.Model, 0)
			}
		}
		assign(&cfg.Connections[baseIndex], &cfg.Agents[owner])
		for _, index := range agentIndexes {
			if index == owner {
				continue
			}
			duplicate := base
			duplicate.Models = append([]ConnectionModel(nil), base.Models...)
			profile := AgentID(cfg.Agents[index].Name)
			id := connectionID + "-" + profile
			for suffix := 2; used[id]; suffix++ {
				id = fmt.Sprintf("%s-%s-%d", connectionID, profile, suffix)
			}
			used[id] = true
			duplicate.ID = id
			duplicate.Label = base.Label + " · " + cfg.Agents[index].Name
			assign(&duplicate, &cfg.Agents[index])
			cfg.Connections = append(cfg.Connections, duplicate)
		}
	}
	for index := range cfg.Agents {
		cfg.Agents[index].B, cfg.Agents[index].Model = "", ""
	}
}

// migrateConnectionAPIKeys moves an inline key into its credential reference.
// Schema 14 could still write the runtime-only field before Save removed it.
func migrateConnectionAPIKeys(data []byte) (bool, []byte, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false, nil, err
	}
	var connections []map[string]json.RawMessage
	if value := raw["connections"]; value != nil {
		if err := json.Unmarshal(value, &connections); err != nil {
			return false, nil, fmt.Errorf("migrate connections: %w", err)
		}
	}
	changed := false
	for _, connection := range connections {
		key, present := connection["api_key"]
		if !present {
			continue
		}
		var value string
		if err := json.Unmarshal(key, &value); err != nil {
			return false, nil, fmt.Errorf("migrate connection api_key: %w", err)
		}
		if value != "" && connection["credential"] == nil {
			var id string
			if err := json.Unmarshal(connection["id"], &id); err != nil {
				return false, nil, fmt.Errorf("migrate connection id: %w", err)
			}
			connection["credential"], _ = json.Marshal(id)
		}
		delete(connection, "api_key")
		changed = true
	}
	if !changed {
		return false, data, nil
	}
	raw["connections"], _ = json.Marshal(connections)
	out, err := json.Marshal(raw)
	return true, out, err
}

func migrateSchemaVersion(data []byte, version int) (bool, []byte, error) {
	if version == CurrentConfigVersion {
		return false, data, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false, nil, err
	}
	raw["config_version"], _ = json.Marshal(CurrentConfigVersion)
	out, err := json.Marshal(raw)
	return true, out, err
}
