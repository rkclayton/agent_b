package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/hermes"
	"harness/internal/session"
	"harness/internal/skills"
)

type skillRequest struct {
	Action, Name, Path string
	AgentID            string `json:"agent_id"`
	Enabled            bool
	Include            []string
}

func (s *Server) skillState() []skills.Skill {
	list, _ := s.skillCatalog(s.ConfigSnapshot().DefaultAgentID())
	return list
}

func (s *Server) skillCatalog(agentID string) ([]skills.Skill, string) {
	s.mu.Lock()
	settings := maps.Clone(s.agentSkillSettingsLocked(agentID))
	s.mu.Unlock()
	return skills.Scan(filepath.Join(s.profileRoot(), "skills"), filepath.Join(s.profileRoot(), "skill-state"), settings)
}

func (s *Server) Index(agentID string) string { _, block := s.skillCatalog(agentID); return block }

func (s *Server) agentSkillSettingsLocked(agentID string) map[string]config.SkillSetting {
	if s.cfg.Skills == nil {
		s.cfg.Skills = map[string]config.SkillSetting{}
	}
	if agentID == "" {
		agentID = s.cfg.DefaultAgentID()
	}
	for index := range s.cfg.Agents {
		if config.AgentID(s.cfg.Agents[index].Name) == agentID {
			if s.cfg.Agents[index].Skills == nil {
				s.cfg.Agents[index].Skills = maps.Clone(s.cfg.Skills)
				if s.cfg.Agents[index].Skills == nil {
					s.cfg.Agents[index].Skills = map[string]config.SkillSetting{}
				}
			}
			return s.cfg.Agents[index].Skills
		}
	}
	return s.cfg.Skills
}

func (s *Server) skillsEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var request skillRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, 400, "invalid skill request", "skills")
		return
	}
	request.Action = strings.ToLower(strings.TrimSpace(request.Action))
	if request.AgentID == "" {
		request.AgentID = s.cfg.DefaultAgentID()
	}
	s.mu.Lock()
	settings := s.agentSkillSettingsLocked(request.AgentID)
	var err error
	var response any
	var hermesTelemetry map[string]any
	persist, publish := true, true
	switch request.Action {
	case "enable":
		value := settings[request.Name]
		value.Enabled = request.Enabled
		settings[request.Name] = value
	case "import":
		var value config.SkillSetting
		request.Path = normalizeSkillImportPath(request.Path)
		switch {
		case request.Path == "":
			err = errors.New(`skill path "(empty)" was not imported: enter a folder or ZIP path`)
		case pathInfoMissing(request.Path):
			err = fmt.Errorf("skill path %s was not imported: the path does not exist", request.Path)
		default:
			value, err = skills.Import(filepath.Join(s.profileRoot(), "skills"), request.Path, s.cfg.Tools.Attachments.MaxBytes)
			if errors.Is(err, os.ErrNotExist) {
				err = fmt.Errorf("skill path %s was not imported: SKILL.md is missing", request.Path)
			}
		}
		if err == nil {
			value.Enabled = true
			settings[value.Name] = value
			for index := range s.cfg.Agents {
				if config.AgentID(s.cfg.Agents[index].Name) != request.AgentID {
					if s.cfg.Agents[index].Skills == nil {
						s.cfg.Agents[index].Skills = maps.Clone(s.cfg.Skills)
					}
					other := s.cfg.Agents[index].Skills[value.Name]
					other.Enabled = false
					s.cfg.Agents[index].Skills[value.Name] = other
				}
			}
		}
	case "replace":
		if _, statErr := os.Stat(filepath.Join(s.profileRoot(), "skills", ".included", request.Name)); statErr == nil {
			err = fmt.Errorf("included skill %q cannot be replaced", request.Name)
			break
		}
		was := settings[request.Name]
		var value config.SkillSetting
		value, err = replaceSkill(filepath.Join(s.profileRoot(), "skills"), normalizeSkillImportPath(request.Path), request.Name, s.cfg.Tools.Attachments.MaxBytes)
		if err == nil {
			value.Enabled, value.Source = was.Enabled, was.Source
			settings[request.Name] = value
		}
	case "remove":
		included := filepath.Join(s.profileRoot(), "skills", ".included", request.Name)
		if _, statErr := os.Stat(included); statErr == nil {
			err = fmt.Errorf("included skill %q cannot be removed", request.Name)
			break
		}
		target := filepath.Join(s.profileRoot(), "skills", request.Name)
		if filepath.Base(target) != request.Name || !skills.ValidName(request.Name) {
			err = fmt.Errorf("invalid skill name %q", request.Name)
			break
		}
		err = os.RemoveAll(target)
		if err == nil {
			for index := range s.cfg.Agents {
				delete(s.cfg.Agents[index].Skills, request.Name)
			}
			delete(s.cfg.Skills, request.Name)
		}
	case "rescan":
	case "preview-folder":
		persist, publish = false, false
		response, err = s.discoverSkillFolder(normalizeSkillImportPath(request.Path))
	case "import-many":
		request.Path = normalizeSkillImportPath(request.Path)
		var preview skillFolderPreview
		preview, err = s.discoverSkillFolder(request.Path)
		if err == nil {
			wanted := map[string]bool{}
			for _, id := range request.Include {
				wanted[id] = true
			}
			if hermesPreview, previewErr := hermes.PreviewHome(request.Path); previewErr == nil {
				ids := []string{}
				for _, row := range hermesPreview.Rows {
					if wanted[row.ID] && (row.Kind == "memory" || row.Kind == "secret") {
						ids = append(ids, row.ID)
					}
				}
				if len(ids) > 0 {
					var report hermes.Report
					report, err = hermes.Import(request.Path, s.profileRoot(), request.AgentID, s.cfg.Memory.MaxTokens, ids)
					response = report
				}
			}
			for _, item := range preview.Rows {
				if err != nil || item.Kind != "skill" || !wanted[item.ID] {
					continue
				}
				var value config.SkillSetting
				if item.Existing {
					was := settings[item.Name]
					value, err = replaceSkill(filepath.Join(s.profileRoot(), "skills"), item.Path, item.Name, s.cfg.Tools.Attachments.MaxBytes)
					value.Enabled = was.Enabled
				} else {
					value, err = skills.Import(filepath.Join(s.profileRoot(), "skills"), item.Path, s.cfg.Tools.Attachments.MaxBytes)
					value.Enabled = true
				}
				if err == nil {
					settings[item.Name] = value
					if !item.Existing {
						s.disableSkillForOtherAgentsLocked(request.AgentID, item.Name)
					}
				}
			}
		}
	case "hermes-preview":
		persist, publish = false, false
		request.Path = normalizeHermesPath(request.Path)
		response, err = hermes.PreviewHome(request.Path)
	case "hermes-import":
		request.Path = normalizeHermesPath(request.Path)
		var report hermes.Report
		report, err = hermes.Import(request.Path, s.profileRoot(), s.cfg.DefaultAgentID(), s.cfg.Memory.MaxTokens, request.Include)
		result := hermesImportResult(err)
		hermesTelemetry = map[string]any{"skills": len(report.Skills), "memory_files": report.MemoryFiles, "secret_names": report.SecretNames, "result": result}
		if err == nil {
			for _, item := range report.Skills {
				if item.Changed {
					value := settings[item.Name]
					value.Enabled, value.Source = item.Enabled, "imported from folder"
					settings[item.Name] = value
				}
			}
			response = report
		}
	default:
		err = errors.New("skill action must be enable, import, replace, remove, rescan, preview-folder, import-many, hermes-preview or hermes-import")
	}
	if err == nil && persist {
		err = s.saveProfileConfig(*s.cfg)
	}
	masked := s.cfg.Masked()
	s.mu.Unlock()
	if hermesTelemetry != nil {
		if err != nil && hermesTelemetry["result"] == "ok" {
			hermesTelemetry["result"] = "failed"
		}
		s.queueRunTelemetry(events.ImportHermes, hermesTelemetry)
	}
	if err != nil {
		writeError(w, 400, err.Error(), "skills")
		return
	}
	state, _ := s.skillCatalog(request.AgentID)
	if publish {
		s.bus.Publish(events.New(events.ConfigChanged, "", "", map[string]any{"config": masked, "skills": state}))
	}
	writeJSON(w, 200, map[string]any{"ok": true, "skills": state, "hermes": response})
}

func hermesImportResult(err error) string {
	if err == nil {
		return "ok"
	}
	for _, marker := range []string{"was not found", "does not look like", "exceeds", "symbolic link", "outside the profile"} {
		if strings.Contains(err.Error(), marker) {
			return "refused"
		}
	}
	return "failed"
}

func normalizeHermesPath(value string) string {
	value = normalizeSkillImportPath(value)
	if value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			value = filepath.Join(home, strings.TrimLeft(value[1:], `/\`))
		}
	}
	return filepath.Clean(value)
}

func normalizeSkillImportPath(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
		value = value[1 : len(value)-1]
	}
	return value
}

func displaySkillPath(value string) string {
	value = normalizeSkillImportPath(value)
	if value == "" {
		return "(empty)"
	}
	return value
}

func pathInfoMissing(path string) bool {
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

type skillFolderRow struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Reason      string `json:"reason"`
	Files       int    `json:"files"`
	Valid       bool   `json:"valid"`
	Existing    bool   `json:"existing"`
	Shipped     bool   `json:"shipped"`
	Included    bool   `json:"included"`
}
type skillFolderPreview struct {
	Rows []skillFolderRow `json:"rows"`
}

func (s *Server) disableSkillForOtherAgentsLocked(agentID, name string) {
	if agentID == "" {
		agentID = s.cfg.DefaultAgentID()
	}
	for index := range s.cfg.Agents {
		if config.AgentID(s.cfg.Agents[index].Name) == agentID {
			continue
		}
		settings := s.agentSkillSettingsLocked(config.AgentID(s.cfg.Agents[index].Name))
		value := settings[name]
		value.Enabled = false
		settings[name] = value
	}
}

func (s *Server) discoverSkillFolder(root string) (skillFolderPreview, error) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return skillFolderPreview{}, fmt.Errorf("skill folder was not found")
	}
	result := skillFolderPreview{Rows: []skillFolderRow{}}
	seen := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry == nil {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !strings.EqualFold(entry.Name(), "SKILL.md") {
			return nil
		}
		dir := filepath.Dir(path)
		item, _, files, inspectErr := skills.InspectSource(dir, s.cfg.Tools.Attachments.MaxBytes)
		row := skillFolderRow{Kind: "skill", Path: dir, Files: len(files), Valid: inspectErr == nil && item.Valid, Included: inspectErr == nil && item.Valid}
		if inspectErr != nil {
			row.Reason = inspectErr.Error()
		} else {
			row.Name, row.Description, row.Reason = item.Name, item.Description, item.Reason
		}
		if row.Name == "" {
			row.Name = filepath.Base(dir)
		}
		row.ID = "skill:" + row.Name
		if seen[row.ID] {
			return nil
		}
		seen[row.ID] = true
		_, userErr := os.Stat(filepath.Join(s.profileRoot(), "skills", row.Name))
		row.Existing = userErr == nil
		_, shippedErr := os.Stat(filepath.Join(s.profileRoot(), "skills", ".included", row.Name))
		row.Shipped = shippedErr == nil
		result.Rows = append(result.Rows, row)
		return nil
	})
	if err != nil {
		return skillFolderPreview{}, err
	}
	if preview, previewErr := hermes.PreviewHome(root); previewErr == nil {
		for _, row := range preview.Rows {
			if seen[row.ID] || (row.Kind != "memory" && row.Kind != "secret") {
				continue
			}
			result.Rows = append(result.Rows, skillFolderRow{ID: row.ID, Kind: row.Kind, Name: row.Name, Description: row.Detail, Valid: row.Selectable, Included: row.Included})
		}
	}
	if len(result.Rows) == 0 {
		result.Rows = append(result.Rows, skillFolderRow{ID: "invalid", Kind: "invalid", Name: filepath.Base(root), Reason: "SKILL.md is missing"})
	}
	return result, nil
}

func (s *Server) Proposal(ctx context.Context, chat *session.Session, runID string) (string, error) {
	if role := chat.Snapshot().Role; role == "c" || role == "e" {
		return "", nil
	}
	messages := chat.MessagesCopy()
	if len(messages) > 0 && messages[len(messages)-1].Role == "assistant" {
		if source, ok := requestedSkillPath(messages[len(messages)-1].Content); ok {
			return s.proposeSkill(ctx, chat, runID, source)
		}
	}
	root := filepath.Join(chat.Workspace, "skill-proposals")
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		return s.proposeSkill(ctx, chat, runID, filepath.Join(root, entry.Name()))
	}
	return "", nil
}

var addSkillPattern = regexp.MustCompile(`(?i)^\s*add\s+(.+?)\s+as\s+a\s+skill[.!]?\s*$`)

func requestedSkillPath(text string) (string, bool) {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	match := addSkillPattern.FindStringSubmatch(strings.TrimSpace(lines[len(lines)-1]))
	if len(match) != 2 {
		return "", false
	}
	value := strings.Trim(strings.TrimSpace(match[1]), `"'`)
	if !filepath.IsAbs(filepath.FromSlash(value)) {
		return "", false
	}
	return filepath.Clean(filepath.FromSlash(value)), true
}

func (s *Server) proposeSkill(ctx context.Context, chat *session.Session, runID, source string) (string, error) {
	item, body, files, err := skills.InspectSource(source, s.cfg.Tools.Attachments.MaxBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("skill proposal refused: %s: SKILL.md is missing", displaySkillPath(source)), nil
		}
		return fmt.Sprintf("skill proposal refused: %s: %s", displaySkillPath(source), err), nil
	}
	if !item.Valid {
		return fmt.Sprintf("skill proposal refused: %s: %s", displaySkillPath(source), item.Reason), nil
	}
	root := filepath.Join(s.profileRoot(), "skills")
	if _, err := os.Stat(filepath.Join(root, ".included", item.Name)); !os.IsNotExist(err) {
		if err != nil {
			return fmt.Sprintf("skill proposal refused: included skill %q could not be checked: %s", item.Name, err), nil
		}
		return fmt.Sprintf("skill proposal refused: included skill %q cannot be replaced", item.Name), nil
	}
	_, statErr := os.Stat(filepath.Join(root, item.Name))
	replacing := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return "skill proposal refused: " + statErr.Error(), nil
	}
	args := map[string]any{"skill_md": body, "files": files, "warnings": item.Warnings}
	cardName := "Add skill " + item.Name + "?"
	if replacing {
		cardName = "Replace skill " + item.Name + "?"
	}
	approved, err := s.runner.Gate().WaitPolicyRequired(ctx, chat, runID, "skill-proposal-"+item.Name, cardName, args)
	if err != nil || !approved {
		if err != nil {
			return "", err
		}
		return "skill proposal declined", nil
	}
	wasEnabled := true
	s.mu.Lock()
	if configured, ok := s.agentSkillSettingsLocked(chat.AgentID)[item.Name]; ok {
		wasEnabled = configured.Enabled
	}
	s.mu.Unlock()
	var setting skills.Setting
	if replacing {
		setting, err = replaceSkill(root, source, item.Name, s.cfg.Tools.Attachments.MaxBytes)
	} else {
		setting, err = skills.Import(root, source, s.cfg.Tools.Attachments.MaxBytes)
	}
	if err != nil {
		return "", err
	}
	setting.Enabled, setting.Source = wasEnabled, "added in chat from "+source
	s.mu.Lock()
	settings := s.agentSkillSettingsLocked(chat.AgentID)
	settings[item.Name] = setting
	if !replacing {
		s.disableSkillForOtherAgentsLocked(chat.AgentID, item.Name)
	}
	err = s.saveProfileConfig(*s.cfg)
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	if replacing {
		return "skill " + item.Name + " replaced", nil
	}
	return "skill " + item.Name + " added", nil
}

func replaceSkill(root, source, name string, max int64) (skills.Setting, error) {
	stage, err := os.MkdirTemp(root, ".replace-"+name+"-")
	if err != nil {
		return skills.Setting{}, err
	}
	defer os.RemoveAll(stage)
	setting, err := skills.Import(stage, source, max)
	if err != nil {
		return skills.Setting{}, err
	}
	destination := filepath.Join(root, name)
	backup := filepath.Join(stage, ".previous")
	if err = os.Rename(destination, backup); err != nil {
		return skills.Setting{}, err
	}
	if err = os.Rename(filepath.Join(stage, name), destination); err != nil {
		if restoreErr := os.Rename(backup, destination); restoreErr != nil {
			return skills.Setting{}, fmt.Errorf("replace failed: %v; restoring previous skill failed: %v", err, restoreErr)
		}
		return skills.Setting{}, err
	}
	return setting, nil
}

func (s *Server) Read(path, agentID string) {
	root := filepath.Join(s.profileRoot(), "skills")
	rel, err := filepath.Rel(root, filepath.FromSlash(path))
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	name := parts[0]
	if name == ".included" && len(parts) > 1 {
		name = parts[1]
	}
	s.mu.Lock()
	settings := s.agentSkillSettingsLocked(agentID)
	value, configured := settings[name]
	if !configured {
		value.Enabled = true
	}
	value.LastRead = time.Now().UTC().Format(time.RFC3339)
	settings[name] = value
	_ = s.saveProfileConfig(*s.cfg)
	s.mu.Unlock()
}
