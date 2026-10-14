package web

import (
	"net/http"
	"strings"

	"harness/internal/tools"
)

func (s *Server) SetFileUndoStore(store *tools.FileUndoStore) { s.fileUndo = store }

func (s *Server) undoFileChanges(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.fileUndoRead(w, r)
		return
	}
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var body struct {
		SessionID string `json:"session_id"`
		RunID     string `json:"run_id"`
		Confirm   bool   `json:"confirm"`
		FileID    string `json:"file_id"`
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
	var result tools.FileUndoResult
	var err error
	if body.FileID == "" {
		result, err = s.fileUndo.Undo(item.ID, body.RunID)
	} else {
		result, err = s.fileUndo.UndoFile(item.ID, body.RunID, body.FileID)
	}
	if err != nil {
		writeError(w, http.StatusConflict, err.Error(), "run_id")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) fileUndoRead(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	item, ok := s.registry.Get(strings.TrimSpace(query.Get("session_id")))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found", "session_id")
		return
	}
	runID := strings.TrimSpace(query.Get("run_id"))
	if runID == "" {
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
	if id := strings.TrimSpace(query.Get("file_id")); id != "" {
		diff, err := s.fileUndo.Diff(item.ID, runID, id)
		if err != nil {
			writeError(w, http.StatusGone, err.Error(), "run_id")
			return
		}
		writeJSON(w, http.StatusOK, diff)
		return
	}
	files, held, err := s.fileUndo.Files(item.ID, runID)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error(), "run_id")
		return
	}
	if !held {
		writeError(w, http.StatusGone, "file changes are no longer held", "run_id")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}
