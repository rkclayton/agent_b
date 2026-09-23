package web

import (
	"context"
	"log"
	"net/http"
	"time"

	"harness/internal/events"
)

func (s *Server) updateEndpoint(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil {
		writeError(w, http.StatusServiceUnavailable, "update service is unavailable", "update")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if r.URL.Query().Get("attach") == "1" {
			if s.updater.TriggerIfStale(context.Background(), 15*time.Minute) {
				log.Printf("update check on window attach: started")
			} else {
				log.Printf("update check on window attach: skipped (running or checked within 15 minutes)")
			}
		}
		writeJSON(w, http.StatusOK, s.updater.State())
	case http.MethodPost:
		var body struct {
			Action string `json:"action"`
		}
		if !decode(w, r, &body) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		switch body.Action {
		case "check":
			if err := s.updater.Check(ctx); err != nil {
				writeError(w, http.StatusBadGateway, err.Error(), "update")
				return
			}
			writeJSON(w, http.StatusOK, s.updater.State())
		case "install":
			path, err := s.updater.Install(ctx)
			if err != nil {
				writeError(w, http.StatusBadGateway, err.Error(), "update")
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]any{"started": true, "path": path, "update": s.updater.State()})
		default:
			writeError(w, http.StatusBadRequest, "action must be check or install", "action")
		}
	default:
		method(w)
	}
}

func (s *Server) publishUpdateChanged(state any) {
	if s.bus != nil {
		s.bus.Publish(events.New(events.UpdateChanged, "", "", state))
	}
}
