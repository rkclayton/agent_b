package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"harness/internal/chatstore"
	"harness/internal/events"
	"harness/internal/projection"
	"harness/internal/session"
)

func (s *Server) chatTree(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet { s.writeChatTree(w); return }
	if r.Method != http.MethodPost { method(w); return }
	var body struct{ Action, Parent, Path, Name, ID, Folder string }
	if !decode(w, r, &body) { return }
	var err error
	switch body.Action {
	case "add": _, err = s.chatStore.AddFolder(body.Parent, body.Name)
	case "rename": _, err = s.chatStore.RenameFolder(body.Path, body.Name)
	case "delete": err = s.chatStore.DeleteFolder(body.Path)
	case "move": _, err = s.chatStore.Move(body.ID, body.Folder)
	case "archive":
		if err = s.registry.Archive(body.ID); err == nil { _, err = s.chatStore.Archive(body.ID, time.Now()) }
		if err == nil { s.projector.Delete(body.ID); s.bus.Publish(events.New(events.ChatDeleted, "", "", map[string]any{"session_id": body.ID, "archived": true})) }
	case "restore":
		err = s.restoreArchivedChat(body.ID)
	default: writeError(w, 400, "unknown chat tree action", "action"); return
	}
	if err != nil { writeError(w, http.StatusConflict, err.Error(), "chats"); return }
	entries, err := s.chatStore.Scan(); if err != nil { writeError(w, 500, err.Error(), "chats"); return }
	s.registry.ReconcileChatHomes(entries); s.writeChatTree(w)
}

func (s *Server) writeChatTree(w http.ResponseWriter) {
	// Replay has projected sessions but deliberately has no writable chat store.
	// Settings still asks for the global chat tree, so answer with the honest
	// empty filesystem view instead of dereferencing the absent store.
	if s.chatStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"root": "", "folders": []string{}, "chats": []map[string]string{}})
		return
	}
	root := s.chatStore.Root()
	entries, archived, err := s.chatStore.List(); if err != nil { writeError(w, 500, err.Error(), "chats"); return }
	folders := []string{}
	_ = filepath.WalkDir(root, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil || !item.IsDir() || path == root { return nil }
		if _, err := os.Stat(filepath.Join(path, chatstore.MetadataFile)); err == nil { return filepath.SkipDir }
		rel, _ := filepath.Rel(root, path); folders = append(folders, filepath.ToSlash(rel)); return nil
	})
	chats := make([]map[string]string, 0, len(entries))
	for _, entry := range entries { rel, _ := filepath.Rel(root, entry.Path); chats = append(chats, map[string]string{"id": entry.Metadata.ID, "name": filepath.Base(entry.Path), "folder": filepath.ToSlash(filepath.Dir(rel))}) }
	for _, chat := range chats { if chat["folder"] == "." { chat["folder"] = "" } }
	archivedChats := make([]map[string]string, 0, len(archived))
	for _, entry := range archived { rel, _ := filepath.Rel(root, entry.Path); archivedChats = append(archivedChats, map[string]string{"id": entry.Metadata.ID, "name": entry.Metadata.Label, "folder": filepath.ToSlash(filepath.Dir(rel)), "archived_at": entry.Metadata.ArchivedAt.Format(time.RFC3339Nano)}) }
	writeJSON(w, 200, map[string]any{"root": root, "folders": folders, "chats": chats, "archived": archivedChats})
}

func (s *Server) restoreArchivedChat(id string) error {
	_, found, err := s.chatStore.Find(id); if err != nil || !found { return errors.Join(err, fmt.Errorf("chat not found")) }
	projected, err := projection.NewCache().ProjectFile(filepath.Join(s.chatStore.Root(), id+".jsonl"), 0); if err != nil { return err }
	raw, _ := json.Marshal(projected); var saved session.Snapshot; if err = json.Unmarshal(raw, &saved); err != nil { return err }
	saved.ID = id
	if _, err = s.registry.RestoreWithTranscript(saved, projected.Chat); err != nil { return err }
	_, err = s.chatStore.Restore(id); return err
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
