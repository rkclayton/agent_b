package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"harness/internal/events"
)

type profileRequest struct {
	Action string `json:"action"`
	Name   string `json:"name"`
	New    string `json:"new_name"`
}

func (s *Server) profileState() map[string]any {
	if s.profiles == nil {
		return map[string]any{"active": "", "names": []string{}}
	}
	return map[string]any{"active": s.profiles.Active(), "names": s.profiles.Names()}
}

func (s *Server) profileEndpoint(w http.ResponseWriter, r *http.Request) {
	if s.profiles == nil {
		writeError(w, http.StatusNotImplemented, "profiles are unavailable", "profiles")
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, s.profileState())
		return
	}
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var request profileRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid profile request", "profiles")
		return
	}
	request.Action = strings.ToLower(strings.TrimSpace(request.Action))
	if request.Action == "switch" && s.profileRunActive() {
		writeError(w, http.StatusConflict, "stop the run first", "profiles")
		return
	}

	var err error
	switch request.Action {
	case "create":
		err = s.profiles.Create(request.Name)
	case "switch":
		previous := s.profiles.Active()
		previousRoot := s.roots.Profile
		err = s.profiles.Switch(request.Name)
		if err == nil && s.profileChanged != nil {
			s.roots.Profile = s.profiles.Root(s.profiles.Active())
			err = s.profileChanged(s.profiles.Root(s.profiles.Active()))
			if err != nil {
				_ = s.profiles.Switch(previous)
				s.roots.Profile = previousRoot
			}
		}
		if err == nil {
			s.roots.Profile = s.profiles.Root(s.profiles.Active())
		}
	case "rename":
		err = s.profiles.Rename(request.Name, request.New)
		if err == nil {
			s.roots.Profile = s.profiles.Root(s.profiles.Active())
		}
	default:
		writeError(w, http.StatusBadRequest, "profile action must be create, switch or rename", "profiles")
		return
	}
	s.mu.RLock()
	masked := s.cfg.Masked()
	s.mu.RUnlock()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "profiles")
		return
	}
	state := s.profileState()
	s.bus.Publish(events.New(events.ConfigChanged, "", "", map[string]any{"config": masked, "profiles": state}))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "profiles": state, "config": masked})
}

func (s *Server) profileRunActive() bool {
	if s.registry == nil {
		return false
	}
	for _, item := range s.registry.List() {
		snapshot := item.Snapshot()
		if snapshot.QueuedMessages > 0 {
			return true
		}
		switch snapshot.Run.Status {
		case "queued", "running", "paused", "stopping":
			return true
		}
	}
	return false
}
