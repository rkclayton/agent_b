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
	if r.Method == http.MethodGet {
		s.writeChatTree(w)
		return
	}
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var body struct {
		Action, Parent, Path, Name, ID, Folder string
		Pinned                                 bool
	}
	if !decode(w, r, &body) {
		return
	}
	s.chatWatchMu.Lock()
	s.stopChatTrackingLocked()
	defer func() { s.startChatTrackingLocked(s.registry); s.chatWatchMu.Unlock() }()
	var err error
	var changed map[string]any
	switch body.Action {
	case "add":
		var destination string
		destination, err = s.chatStore.AddFolder(body.Parent, body.Name)
		if err == nil {
			changed = map[string]any{"operation": "add", "path": s.relativeChatPath(destination)}
		}
	case "rename":
		var destination string
		destination, err = s.chatStore.RenameFolder(body.Path, body.Name)
		if err == nil {
			changed = map[string]any{"operation": "rename", "path": filepath.ToSlash(filepath.Clean(body.Path)), "value": s.relativeChatPath(destination)}
		}
	case "delete":
		err = s.chatStore.DeleteFolder(body.Path)
		if err == nil {
			changed = map[string]any{"operation": "delete", "path": filepath.ToSlash(filepath.Clean(body.Path))}
		}
	case "move":
		var destination string
		destination, err = s.chatStore.Move(body.ID, body.Folder)
		if err == nil {
			changed = map[string]any{"operation": "move", "session_id": body.ID, "folder": s.relativeChatPath(filepath.Dir(destination))}
		}
	case "pin":
		err = s.chatStore.SetPinned(body.ID, body.Pinned)
	case "archive":
		if err = s.registry.Archive(body.ID); err == nil {
			_, err = s.chatStore.Archive(body.ID, time.Now())
		}
		if err == nil {
			s.projector.Delete(body.ID)
			s.bus.Publish(events.New(events.ChatDeleted, "", "", map[string]any{"session_id": body.ID, "archived": true}))
		}
	case "restore":
		err = s.restoreArchivedChat(body.ID)
	default:
		writeError(w, 400, "unknown chat tree action", "action")
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, err.Error(), "chats")
		return
	}
	entries, err := s.chatStore.Scan()
	if err != nil {
		writeError(w, 500, err.Error(), "chats")
		return
	}
	s.registry.ReconcileChatHomes(entries)
	if changed != nil {
		s.bus.Publish(events.New(events.ChatListPatch, "", "", changed))
	}
	s.writeChatTree(w)
}

func (s *Server) relativeChatPath(path string) string {
	relative, err := filepath.Rel(s.chatStore.Root(), path)
	if err != nil || relative == "." {
		return ""
	}
	return filepath.ToSlash(relative)
}

func (s *Server) renameStoredChat(id, label string) (string, error) {
	s.chatWatchMu.Lock()
	defer s.chatWatchMu.Unlock()
	s.stopChatTrackingLocked()
	defer s.startChatTrackingLocked(s.registry)
	return s.chatStore.Rename(id, label)
}

func (s *Server) chatFolders() ([]string, error) {
	folders := []string{}
	if s.chatStore == nil {
		return folders, nil
	}
	root := s.chatStore.Root()
	err := filepath.WalkDir(root, func(path string, item fs.DirEntry, walkErr error) error {
		if os.IsNotExist(walkErr) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if !item.IsDir() || path == root {
			return nil
		}
		if _, err := os.Stat(filepath.Join(path, chatstore.MetadataFile)); err == nil {
			return filepath.SkipDir
		}
		folders = append(folders, s.relativeChatPath(path))
		return nil
	})
	return folders, err
}

func (s *Server) decorateChatList(sessions map[string]projection.Snapshot) []string {
	folders, _ := s.chatFolders()
	if s.chatStore == nil {
		return folders
	}
	entries, err := s.chatStore.Scan()
	if err != nil {
		return folders
	}
	for _, entry := range entries {
		value, found := sessions[entry.Metadata.ID]
		if !found {
			continue
		}
		value.Folder = s.relativeChatPath(filepath.Dir(entry.Path))
		value.LastActivity = latestChatActivity(value)
		if value.LastActivity == "" {
			activity := entry.Metadata.LastActivity
			if activity.IsZero() {
				activity = entry.Metadata.Created
			}
			value.LastActivity = activity.UTC().Format(time.RFC3339Nano)
		}
		sessions[entry.Metadata.ID] = value
	}
	return folders
}

func latestChatActivity(value projection.Snapshot) string {
	if count := len(value.Timeline); count > 0 {
		return value.Timeline[count-1].TS
	}
	return value.CreatedAt
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
	entries, archived, err := s.chatStore.List()
	if err != nil {
		writeError(w, 500, err.Error(), "chats")
		return
	}
	folders, _ := s.chatFolders()
	chats := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		rel, _ := filepath.Rel(root, entry.Path)
		chats = append(chats, map[string]any{"id": entry.Metadata.ID, "name": filepath.Base(entry.Path), "folder": filepath.ToSlash(filepath.Dir(rel)), "pinned": entry.Metadata.Pinned})
	}
	for _, chat := range chats {
		if chat["folder"] == "." {
			chat["folder"] = ""
		}
	}
	archivedChats := make([]map[string]string, 0, len(archived))
	for _, entry := range archived {
		rel, _ := filepath.Rel(root, entry.Path)
		archivedChats = append(archivedChats, map[string]string{"id": entry.Metadata.ID, "name": entry.Metadata.Label, "folder": filepath.ToSlash(filepath.Dir(rel)), "archived_at": entry.Metadata.ArchivedAt.Format(time.RFC3339Nano)})
	}
	writeJSON(w, 200, map[string]any{"root": root, "folders": folders, "chats": chats, "archived": archivedChats})
}

func (s *Server) restoreArchivedChat(id string) error {
	_, found, err := s.chatStore.Find(id)
	if err != nil || !found {
		return errors.Join(err, fmt.Errorf("chat not found"))
	}
	projected, err := projection.NewCache().ProjectFile(filepath.Join(s.chatStore.Root(), id+".jsonl"), 0)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(projected)
	var saved session.Snapshot
	if err = json.Unmarshal(raw, &saved); err != nil {
		return err
	}
	saved.ID = id
	if _, err = s.registry.RestoreWithTranscript(saved, projected.Chat); err != nil {
		return err
	}
	_, err = s.chatStore.Restore(id)
	return err
}

func (s *Server) deleteAllChats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var body struct {
		Confirm bool `json:"confirm"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !body.Confirm {
		writeError(w, 400, "confirmation is required", "confirm")
		return
	}
	items := s.registry.List()
	for _, item := range items {
		if item.IsRunning() {
			writeError(w, http.StatusConflict, item.Snapshot().Label+" is running", "chat")
			return
		}
	}
	for _, item := range items {
		if !item.IsClosed() {
			if err := s.registry.Close(item.ID); err != nil {
				writeError(w, 409, err.Error(), "chat")
				return
			}
		}
		if _, err := s.deleteChat(item); err != nil {
			writeError(w, 409, err.Error(), "chat")
			return
		}
	}
	writeJSON(w, 200, map[string]any{"deleted": len(items)})
}
