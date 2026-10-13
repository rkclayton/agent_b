package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	Enabled            bool
	Include            []string
}

func (s *Server) skillState() []skills.Skill { list, _ := s.skillCatalog(); return list }

func (s *Server) skillCatalog() ([]skills.Skill, string) {
	s.mu.RLock()
	settings := maps.Clone(s.cfg.Skills)
	s.mu.RUnlock()
	return skills.Scan(filepath.Join(s.profileRoot(), "skills"), filepath.Join(s.profileRoot(), "skill-state"), settings)
}

func (s *Server) Index() string { _, block := s.skillCatalog(); return block }

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
	s.mu.Lock()
	if s.cfg.Skills == nil {
		s.cfg.Skills = map[string]config.SkillSetting{}
	}
	var err error
	var response any
	var hermesTelemetry map[string]any
	persist, publish := true, true
	switch request.Action {
	case "enable":
		value := s.cfg.Skills[request.Name]
		value.Enabled = request.Enabled
		s.cfg.Skills[request.Name] = value
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
			s.cfg.Skills[value.Name] = value
		}
	case "rescan":
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
					s.cfg.Skills[item.Name] = config.SkillSetting{Enabled: item.Enabled, Source: "imported from Hermes"}
				}
			}
			response = report
		}
	default:
		err = errors.New("skill action must be enable, import, rescan, hermes-preview or hermes-import")
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
	state := s.skillState()
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
	s.mu.RLock()
	if configured, ok := s.cfg.Skills[item.Name]; ok {
		wasEnabled = configured.Enabled
	}
	s.mu.RUnlock()
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
	if s.cfg.Skills == nil {
		s.cfg.Skills = map[string]config.SkillSetting{}
	}
	s.cfg.Skills[item.Name] = setting
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

func (s *Server) Read(path string) {
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
	if s.cfg.Skills == nil {
		s.cfg.Skills = map[string]config.SkillSetting{}
	}
	value, configured := s.cfg.Skills[name]
	if !configured {
		value.Enabled = true
	}
	value.LastRead = time.Now().UTC().Format(time.RFC3339)
	s.cfg.Skills[name] = value
	_ = s.saveProfileConfig(*s.cfg)
	s.mu.Unlock()
}
