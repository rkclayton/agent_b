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
	"harness/internal/session"
	"harness/internal/skills"
)

type skillRequest struct {
	Action, Name, Path string
	Enabled            bool
}

func (s *Server) skillState() []skills.Skill { list, _ := s.skillCatalog(); return list }

func (s *Server) skillCatalog() ([]skills.Skill, string) {
	s.mu.RLock()
	settings := maps.Clone(s.cfg.Skills)
	s.mu.RUnlock()
	return skills.Scan(filepath.Join(s.profileRoot(), "skills"), settings)
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
	default:
		err = errors.New("skill action must be enable, import or rescan")
	}
	if err == nil {
		err = s.saveProfileConfig(*s.cfg)
	}
	masked := s.cfg.Masked()
	s.mu.Unlock()
	if err != nil {
		writeError(w, 400, err.Error(), "skills")
		return
	}
	state := s.skillState()
	s.bus.Publish(events.New(events.ConfigChanged, "", "", map[string]any{"config": masked, "skills": state}))
	writeJSON(w, 200, map[string]any{"ok": true, "skills": state})
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
	match := addSkillPattern.FindStringSubmatch(strings.TrimSpace(text))
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
			return "skill proposal refused: SKILL.md is missing", nil
		}
		return "skill proposal refused: " + err.Error(), nil
	}
	if !item.Valid {
		return "skill proposal refused: " + item.Reason, nil
	}
	if _, err := os.Stat(filepath.Join(s.profileRoot(), "skills", item.Name)); !os.IsNotExist(err) {
		return fmt.Sprintf("skill proposal refused: skill %q already exists", item.Name), nil
	}
	args := map[string]any{"skill_md": body, "files": files, "warnings": item.Warnings}
	approved, err := s.runner.Gate().WaitPolicyRequired(ctx, chat, runID, "skill-proposal-"+item.Name, "Add skill "+item.Name+"?", args)
	if err != nil || !approved {
		if err != nil {
			return "", err
		}
		return "skill proposal declined", nil
	}
	setting, err := skills.Import(filepath.Join(s.profileRoot(), "skills"), source, s.cfg.Tools.Attachments.MaxBytes)
	if err != nil {
		return "", err
	}
	setting.Enabled, setting.Source = true, "added in chat from "+source
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
	return "skill " + item.Name + " added", nil
}

func (s *Server) Read(path string) {
	root := filepath.Join(s.profileRoot(), "skills")
	rel, err := filepath.Rel(root, filepath.FromSlash(path))
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return
	}
	name := strings.Split(filepath.ToSlash(rel), "/")[0]
	s.mu.Lock()
	if value, ok := s.cfg.Skills[name]; ok {
		value.LastRead = time.Now().UTC().Format(time.RFC3339)
		s.cfg.Skills[name] = value
		_ = s.saveProfileConfig(*s.cfg)
	}
	s.mu.Unlock()
}
