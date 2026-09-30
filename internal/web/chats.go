package web

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"harness/internal/chatstore"
)

func (s *Server) chatTree(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet { s.writeChatTree(w); return }
	if r.Method != http.MethodPost { method(w); return }
	var body struct { Action, Parent, Path, Name, ID, Folder string }
	if !decode(w, r, &body) { return }
	var err error
	switch body.Action {
	case "add": _, err = s.chatStore.AddFolder(body.Parent, body.Name)
	case "rename": _, err = s.chatStore.RenameFolder(body.Path, body.Name)
	case "delete": err = s.chatStore.DeleteFolder(body.Path)
	case "move": _, err = s.chatStore.Move(body.ID, body.Folder)
	default: writeError(w, 400, "unknown chat tree action", "action"); return
	}
	if err != nil { writeError(w, http.StatusConflict, err.Error(), "chats"); return }
	entries, err := s.chatStore.Scan(); if err != nil { writeError(w, 500, err.Error(), "chats"); return }
	s.registry.ReconcileChatHomes(entries); s.writeChatTree(w)
}

func (s *Server) writeChatTree(w http.ResponseWriter) {
	root := s.chatStore.Root()
	entries, err := s.chatStore.Scan(); if err != nil { writeError(w, 500, err.Error(), "chats"); return }
	folders := []string{}
	_ = filepath.WalkDir(root, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil || !item.IsDir() || path == root { return nil }
		if _, err := os.Stat(filepath.Join(path, chatstore.MetadataFile)); err == nil { return filepath.SkipDir }
		rel, _ := filepath.Rel(root, path); folders = append(folders, filepath.ToSlash(rel)); return nil
	})
	chats := make([]map[string]string, 0, len(entries))
	for _, entry := range entries { rel, _ := filepath.Rel(root, entry.Path); chats = append(chats, map[string]string{"id": entry.Metadata.ID, "name": filepath.Base(entry.Path), "folder": filepath.ToSlash(filepath.Dir(rel))}) }
	for _, chat := range chats { if chat["folder"] == "." { chat["folder"] = "" } }
	writeJSON(w, 200, map[string]any{"root": root, "folders": folders, "chats": chats})
}

func (s *Server) deleteAllChats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { method(w); return }
	var body struct { Confirm bool `json:"confirm"` }; if !decode(w, r, &body) { return }
	if !body.Confirm { writeError(w, 400, "confirmation is required", "confirm"); return }
	items := s.registry.List()
	for _, item := range items { if item.IsRunning() { writeError(w, http.StatusConflict, item.Snapshot().Label+" is running", "chat"); return } }
	for _, item := range items {
		if !item.IsClosed() { if err := s.registry.Close(item.ID); err != nil { writeError(w, 409, err.Error(), "chat"); return } }
		if _, err := s.deleteChat(item); err != nil { writeError(w, 409, err.Error(), "chat"); return }
	}
	writeJSON(w, 200, map[string]any{"deleted": len(items)})
}
