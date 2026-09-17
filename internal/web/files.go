package web

import (
	"fmt"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"harness/internal/tools"
)

func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		method(w)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/files/")
	if path == "" || strings.HasSuffix(path, "/") {
		http.NotFound(w, r)
		return
	}
	workspace, ok := s.fileWorkspace(r.URL.Query().Get("session"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	resolved, err := tools.Resolve(workspace, path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(resolved)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(resolved)})
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, filepath.Base(resolved), info.ModTime(), file)
}

func (s *Server) fileWorkspace(sessionID string) (string, bool) {
	if sessionID == "" {
		return s.roots.Workspace, s.roots.Workspace != ""
	}
	if s.registry != nil {
		if item, ok := s.registry.Get(sessionID); ok {
			if item.Role == "d" && item.PlanDir != "" {
				return item.PlanDir, true
			}
			return item.Workspace, true
		}
	}
	if s.replay != nil {
		if item, ok := s.replay.Sessions[sessionID]; ok {
			if item.Role == "d" && item.PlanDir != "" {
				return item.PlanDir, true
			}
			return item.Workspace, item.Workspace != ""
		}
	}
	return "", false
}

func (s *Server) openFileFolder(w http.ResponseWriter, r *http.Request) {
	s.openDelivered(w, r, false)
}

// openDeliveredFile opens a delivered file with the operator's default
// application (item 2ep: an .xlsx opens in Excel). The harness runs as the
// operator, so this needs no Run-as-you. A default open runs whatever the file
// type runs, so only document types are opened; anything else answers 415 and
// the chip still offers its folder.
func (s *Server) openDeliveredFile(w http.ResponseWriter, r *http.Request) {
	s.openDelivered(w, r, true)
}

var openableExtensions = map[string]bool{
	".xlsx": true, ".xls": true, ".csv": true, ".docx": true, ".doc": true, ".pptx": true,
	".pdf": true, ".txt": true, ".md": true, ".json": true, ".log": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
}

// OpenableExtension reports whether the chip may open a file of this type.
func OpenableExtension(path string) bool {
	return openableExtensions[strings.ToLower(filepath.Ext(path))]
}

func (s *Server) openDelivered(w http.ResponseWriter, r *http.Request, file bool) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var body struct {
		SessionID string `json:"session_id"`
		Path      string `json:"path"`
		Scope     string `json:"scope"`
	}
	if !decode(w, r, &body) {
		return
	}
	root := ""
	if body.Scope == "" || body.Scope == "workspace" {
		var ok bool
		root, ok = s.fileWorkspace(body.SessionID)
		if !ok {
			writeError(w, http.StatusNotFound, "session not found", "session_id")
			return
		}
	} else if body.Scope == "exchange" {
		var err error
		root, err = s.ConfigSnapshot().ResolvedExchangeFolder()
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error(), "deliver.exchange_folder")
			return
		}
	} else {
		writeError(w, http.StatusBadRequest, "scope must be folder or exchange", "scope")
		return
	}
	resolved, err := tools.Resolve(root, body.Path)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found", "path")
		return
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "file not found", "path")
		return
	}
	if file {
		if !OpenableExtension(resolved) {
			writeError(w, http.StatusUnsupportedMediaType, "this file type is not opened from Chat; open its folder instead", "path")
			return
		}
		if err := s.openFile(resolved); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error(), "path")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"path": body.Path})
		return
	}
	if err := s.openFolder(resolved); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "path")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": body.Path})
}

func openWithDefaultApplication(path string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", path)
	case "darwin":
		command = exec.Command("open", path)
	default:
		command = exec.Command("xdg-open", path)
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	return command.Process.Release()
}

func openContainingFolder(path string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("explorer.exe", "/select,"+path)
	case "darwin":
		command = exec.Command("open", "-R", path)
	default:
		command = exec.Command("xdg-open", filepath.Dir(path))
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("open containing folder: %w", err)
	}
	return command.Process.Release()
}
