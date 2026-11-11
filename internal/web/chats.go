package web

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"harness/internal/chatstore"
	"harness/internal/events"
	"harness/internal/projection"
)

type chatSearchMatch struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Line      string `json:"line"`
	MessageID string `json:"message_id"`
	Archived  bool   `json:"archived"`
	activity  time.Time
}

func (s *Server) chatSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	answer := struct {
		Matches []chatSearchMatch `json:"matches"`
	}{Matches: []chatSearchMatch{}}
	if len([]rune(query)) < 2 || s.writers == nil || s.chatState.storeSnapshot() == nil {
		writeJSON(w, 200, answer)
		return
	}
	entries, err := s.chatState.storeSnapshot().Scan()
	if err != nil {
		writeError(w, 500, err.Error(), "chats")
		return
	}
	for _, entry := range entries {
		message, found, searchErr := s.writers.SearchChatMessage(entry.Metadata.ID, query)
		if os.IsNotExist(searchErr) {
			continue
		}
		if searchErr != nil {
			writeError(w, 500, searchErr.Error(), "chats")
			return
		}
		if !found {
			continue
		}
		activity := entry.Metadata.LastActivity
		if activity.IsZero() {
			activity = entry.Metadata.Created
		}
		answer.Matches = append(answer.Matches, chatSearchMatch{ID: entry.Metadata.ID, Name: entry.Metadata.Label, Line: chatMatchLine(message.Content, query), MessageID: message.ID, Archived: !entry.Metadata.ArchivedAt.IsZero(), activity: activity})
	}
	sort.SliceStable(answer.Matches, func(i, j int) bool { return answer.Matches[i].activity.After(answer.Matches[j].activity) })
	if len(answer.Matches) > 50 {
		answer.Matches = answer.Matches[:50]
	}
	writeJSON(w, 200, answer)
}

func chatMatchLine(content, query string) string {
	text := []rune(strings.Map(func(value rune) rune {
		if value == '\r' || value == '\n' || value == '\t' {
			return ' '
		}
		return value
	}, content))
	needle := []rune(strings.ToLower(strings.TrimSpace(query)))
	lower := []rune(strings.ToLower(string(text)))
	index := 0
	for ; index+len(needle) <= len(lower); index++ {
		if string(lower[index:index+len(needle)]) == string(needle) {
			break
		}
	}
	start := index - 48
	if start < 0 {
		start = 0
	}
	end := start + 120
	if end > len(text) {
		end = len(text)
		start = end - 120
		if start < 0 {
			start = 0
		}
	}
	line := string(text[start:end])
	if start > 0 {
		line = "…" + string([]rune(line)[1:])
	}
	if end < len(text) {
		trimmed := []rune(line)
		line = string(trimmed[:len(trimmed)-1]) + "…"
	}
	return line
}

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
	defer s.chatState.lockForMutation(s.registry)()
	store := s.chatState.storeSnapshot()
	var err error
	var changed map[string]any
	switch body.Action {
	case "add":
		var destination string
		destination, err = store.AddFolder(body.Parent, body.Name)
		if err == nil {
			changed = map[string]any{"operation": "add", "path": s.relativeChatPath(destination)}
		}
	case "rename":
		var destination string
		destination, err = store.RenameFolder(body.Path, body.Name)
		if err == nil {
			changed = map[string]any{"operation": "rename", "path": filepath.ToSlash(filepath.Clean(body.Path)), "value": s.relativeChatPath(destination)}
		}
	case "delete":
		err = store.DeleteFolder(body.Path)
		if err == nil {
			changed = map[string]any{"operation": "delete", "path": filepath.ToSlash(filepath.Clean(body.Path))}
		}
	case "move":
		var destination string
		destination, err = store.Move(body.ID, body.Folder)
		if err == nil {
			changed = map[string]any{"operation": "move", "session_id": body.ID, "folder": s.relativeChatPath(filepath.Dir(destination))}
		}
	case "pin":
		err = store.SetPinned(body.ID, body.Pinned)
	case "archive", "restore":
		err = store.SetArchived(body.ID, body.Action == "archive")
		if err == nil {
			changed = map[string]any{"operation": body.Action, "session_id": body.ID}
		}
	default:
		writeError(w, 400, "unknown chat tree action", "action")
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, err.Error(), "chats")
		return
	}
	entries, err := store.Scan()
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
	relative, err := filepath.Rel(s.chatState.storeSnapshot().Root(), path)
	if err != nil || relative == "." {
		return ""
	}
	return filepath.ToSlash(relative)
}

func (s *Server) renameStoredChat(id, label string) (string, error) {
	defer s.chatState.lockForMutation(s.registry)()
	return s.chatState.storeSnapshot().Rename(id, label)
}

func (s *Server) chatFolders() ([]string, error) {
	folders := []string{}
	store := s.chatState.storeSnapshot()
	if store == nil {
		return folders, nil
	}
	root := store.Root()
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
	store := s.chatState.storeSnapshot()
	if store == nil {
		return folders
	}
	entries, err := store.Scan()
	if err != nil {
		return folders
	}
	for _, entry := range entries {
		if value, found := sessions[entry.Metadata.ID]; found {
			sessions[entry.Metadata.ID] = s.decorateChatRow(value, entry)
		}
	}
	return folders
}

func (s *Server) omitArchivedChats(sessions map[string]projection.Snapshot) {
	for id := range s.archivedChatIDs() {
		delete(sessions, id)
	}
}

func (s *Server) archivedChatIDs() map[string]bool {
	ids := map[string]bool{}
	store := s.chatState.storeSnapshot()
	if store == nil {
		return ids
	}
	entries, err := store.Scan()
	if err != nil {
		return ids
	}
	for _, entry := range entries {
		if !entry.Metadata.ArchivedAt.IsZero() {
			ids[entry.Metadata.ID] = true
		}
	}
	return ids
}

// decorateChatRow gives one chat its list row's folder and last activity.
func (s *Server) decorateChatRow(value projection.Snapshot, entry chatstore.Entry) projection.Snapshot {
	value.Folder = s.relativeChatPath(filepath.Dir(entry.Path))
	value.LastActivity = latestChatActivity(value)
	if value.LastActivity == "" {
		activity := entry.Metadata.LastActivity
		if !activity.IsZero() {
			value.LastActivity = activity.UTC().Format(time.RFC3339Nano)
		}
	}
	return value
}

func latestChatActivity(value projection.Snapshot) string {
	if value.LastActivity != "" {
		return value.LastActivity
	}
	for index := len(value.Timeline) - 1; index >= 0; index-- {
		if activity := turnActivity(value.Timeline[index]); activity != "" {
			return activity
		}
	}
	return ""
}

func latestTurnActivityPatch(value projection.Snapshot, patch projection.Patch) string {
	changed := false
	for _, operation := range patch.Operations {
		if operation.Path == "/messages" {
			changed = true
			break
		}
	}
	if !changed || len(value.Timeline) == 0 {
		return ""
	}
	return turnActivity(value.Timeline[len(value.Timeline)-1])
}

func turnActivity(event events.Event) string {
	if event.Type != events.MessageAppended {
		return ""
	}
	var data struct {
		Message events.Message `json:"message"`
	}
	encoded, _ := json.Marshal(event.Data)
	if json.Unmarshal(encoded, &data) == nil && (data.Message.Role == "user" || data.Message.Role == "assistant") {
		return event.TS
	}
	return ""
}

func (s *Server) writeChatTree(w http.ResponseWriter) {
	// Replay has projected sessions but deliberately has no writable chat store.
	// Settings still asks for the global chat tree, so answer with the honest
	// empty filesystem view instead of dereferencing the absent store.
	store := s.chatState.storeSnapshot()
	if store == nil {
		writeJSON(w, http.StatusOK, map[string]any{"root": "", "folders": []string{}, "chats": []map[string]string{}})
		return
	}
	root := store.Root()
	entries, err := store.Scan()
	if err != nil {
		writeError(w, 500, err.Error(), "chats")
		return
	}
	folders, _ := s.chatFolders()
	chats := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		rel, _ := filepath.Rel(root, entry.Path)
		activity := entry.Metadata.LastActivity
		if activity.IsZero() {
			activity = entry.Metadata.Created
		}
		chats = append(chats, map[string]any{"id": entry.Metadata.ID, "name": entry.Metadata.Label, "folder": filepath.ToSlash(filepath.Dir(rel)), "pinned": entry.Metadata.Pinned, "archived": !entry.Metadata.ArchivedAt.IsZero(), "last_activity": activity.UTC().Format(time.RFC3339Nano)})
	}
	for _, chat := range chats {
		if chat["folder"] == "." {
			chat["folder"] = ""
		}
	}
	writeJSON(w, 200, map[string]any{"root": root, "folders": folders, "chats": chats})
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
