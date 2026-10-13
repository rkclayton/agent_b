package web

import (
	"net/http"
	"strings"

	"harness/internal/tools"
)

func (s *Server) SetFileUndoStore(store *tools.FileUndoStore) { s.fileUndo = store }

func (s *Server) undoFileChanges(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var body struct {
		SessionID string `json:"session_id"`
		RunID     string `json:"run_id"`
		Confirm   bool   `json:"confirm"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !body.Confirm {
		writeError(w, http.StatusBadRequest, "confirm is required", "confirm")
		return
	}
	item, ok := s.registry.Get(strings.TrimSpace(body.SessionID))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found", "session_id")
		return
	}
	if body.RunID == "" {
		writeError(w, http.StatusBadRequest, "run_id is required", "run_id")
		return
	}
	if item.IsRunning() {
		writeError(w, http.StatusConflict, "wait for the run to finish", "run_id")
		return
	}
	if s.fileUndo == nil {
		writeError(w, http.StatusServiceUnavailable, "file undo is unavailable", "run_id")
		return
	}
	result, err := s.fileUndo.Undo(item.ID, body.RunID)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error(), "run_id")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
