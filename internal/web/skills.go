package web

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
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

func (s *Server) skillState() []skills.Skill {
	list, _ := s.skillCatalog()
	return list
}

func (s *Server) skillCatalog() ([]skills.Skill, string) {
	s.mu.RLock()
	settings := maps.Clone(s.cfg.Skills)
	s.mu.RUnlock()
	return skills.Scan(filepath.Join(s.profileRoot(), "skills"), settings)
}

func (s *Server) Index() string {
	_, block := s.skillCatalog()
	return block
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
		var item skills.Skill
		item, _, _, err = skills.Inspect(request.Path)
		if err == nil {
			s.cfg.Skills[item.Name], err = skills.Import(filepath.Join(s.profileRoot(), "skills"), request.Path)
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

func (s *Server) Proposal(ctx context.Context, chat *session.Session, runID string) (string, error) {
	root := filepath.Join(chat.Workspace, "skill-proposals")
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		source := filepath.Join(root, entry.Name())
		item, body, files, err := skills.Inspect(source)
		if err != nil {
			continue
		}
		args := map[string]any{"skill_md": body, "files": files}
		if !item.Valid {
			args["validation_error"] = item.Reason
		}
		approved, gateErr := s.runner.Gate().WaitPolicyRequired(ctx, chat, runID, "skill-proposal-"+entry.Name(), "Add skill "+item.Name+"?", args)
		if gateErr != nil {
			return "", gateErr
		}
		if !approved {
			return "skill proposal declined", nil
		}
		if !item.Valid {
			return "skill proposal refused: " + item.Reason, nil
		}
		setting, err := skills.Import(filepath.Join(s.profileRoot(), "skills"), source)
		if err != nil {
			return "", err
		}
		setting.Enabled = true
		setting.Source = "proposed in chat " + chat.ID
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
	return "", nil
}

func (s *Server) Read(path string) {
	abs, err := filepath.Abs(filepath.FromSlash(path))
	if err != nil {
		return
	}
	root := filepath.Join(s.profileRoot(), "skills")
	rel, err := filepath.Rel(root, abs)
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
