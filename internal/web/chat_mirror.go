package web

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"harness/internal/chatstore"
	"harness/internal/events"
	"harness/internal/session"
)

type mirrorEvent struct {
	TS    string          `json:"ts"`
	RunID string          `json:"run_id"`
	Type  string          `json:"type"`
	Data  json.RawMessage `json:"data"`
}
type mirrorFile struct {
	Path            string `json:"path"`
	Bytes           int64  `json:"bytes"`
	SHA256, Content string
}
type mirrorAppend struct {
	ChatID string       `json:"chat_id"`
	Origin string       `json:"origin"`
	Owner  string       `json:"owner"`
	Seq    int          `json:"seq"`
	Event  mirrorEvent  `json:"event"`
	Files  []mirrorFile `json:"files,omitempty"`
}

func (s *Server) dispatchMirror(request appRequestUnit) []byte {
	switch request.Route {
	case "chat.mirror":
		var append mirrorAppend
		if json.Unmarshal(request.Body, &append) != nil {
			return s.appProblem(request.ID, 400, "chat.mirror body is invalid")
		}
		body, status, err := s.applyMirror(append)
		if err != nil {
			return s.appProblem(request.ID, status, err.Error())
		}
		return s.appResponse(request.ID, status, body)
	case "chat.mirror.since":
		var body struct {
			ChatID string `json:"chat_id"`
		}
		if json.Unmarshal(request.Body, &body) != nil || body.ChatID == "" {
			return s.appProblem(request.ID, 400, "chat_id is required")
		}
		metadata, _, err := s.mirrorMetadata(body.ChatID)
		if err != nil {
			return s.appProblem(request.ID, 404, "mirrored chat not found")
		}
		return s.appResponse(request.ID, 200, map[string]any{"chat_id": body.ChatID, "last_seq": metadata.MirrorSeq, "origin": metadata.Origin, "owner": metadata.Owner})
	case "chat.mirror.take":
		var body struct {
			ChatID   string `json:"chat_id"`
			AfterSeq int    `json:"after_seq"`
		}
		if json.Unmarshal(request.Body, &body) != nil {
			return s.appProblem(request.ID, 400, "chat.mirror.take body is invalid")
		}
		if err := s.takeMirror(body.ChatID, body.AfterSeq, "phone"); err != nil {
			return s.appProblem(request.ID, http.StatusConflict, err.Error())
		}
		return s.appResponse(request.ID, 200, map[string]any{"chat_id": body.ChatID, "owner": "phone", "next_seq": body.AfterSeq + 1})
	}
	return nil
}

func (s *Server) appResponse(id string, status int, body any) []byte {
	payload, _ := json.Marshal(body)
	out, _ := json.Marshal(appResponseUnit{V: 1, Kind: "response", ID: id, Status: status, Body: payload})
	return out
}

func (s *Server) applyMirror(append mirrorAppend) (map[string]any, int, error) {
	if append.ChatID == "" || append.Origin != "phone" || append.Owner == "" || append.Seq < 1 || append.Event.Type == "" {
		return nil, 400, errors.New("chat.mirror fields are invalid")
	}
	encoded, _ := json.Marshal(append)
	digest := sha256.Sum256(encoded)
	hash := hex.EncodeToString(digest[:])
	metadata, folder, err := s.mirrorMetadata(append.ChatID)
	if err != nil {
		if append.Seq != 1 || append.Event.Type != events.SessionCreated || append.Owner != "phone" {
			return nil, 409, errors.New("the first mirror append must be the phone owner's session.created")
		}
		var wrapper struct {
			Session session.Snapshot `json:"session"`
		}
		if json.Unmarshal(append.Event.Data, &wrapper) != nil || wrapper.Session.ID != append.ChatID {
			return nil, 400, errors.New("session.created does not seed this chat")
		}
		wrapper.Session.Origin, wrapper.Session.Owner, wrapper.Session.Scratch = "phone", "phone", true
		wrapper.Session.Workspace, wrapper.Session.WorkspaceDir = "", ""
		if _, err = s.registry.Restore(wrapper.Session); err != nil {
			return nil, 400, err
		}
		metadata, folder, err = s.mirrorMetadata(append.ChatID)
		if err != nil {
			return nil, 500, err
		}
	} else {
		if append.Origin != metadata.Origin || append.Owner != metadata.Owner {
			log.Printf("chat mirror refused a non-owner append for %s", append.ChatID)
			return nil, 409, errors.New("only the current owner may append")
		}
		if append.Seq <= metadata.MirrorSeq {
			if append.Seq <= len(metadata.MirrorHashes) && metadata.MirrorHashes[append.Seq-1] == hash {
				return map[string]any{"chat_id": append.ChatID, "last_seq": metadata.MirrorSeq}, 200, nil
			}
			return nil, 409, errors.New("a repeated sequence differs from the durable event")
		}
		if append.Seq != metadata.MirrorSeq+1 {
			return nil, 409, fmt.Errorf("sequence gap: have %d, received %d", metadata.MirrorSeq, append.Seq)
		}
	}
	if err := validateMirrorFiles(folder, append.Files); err != nil {
		return nil, 400, err
	}
	if err := writeMirrorFiles(folder, append.Files); err != nil {
		return nil, 500, err
	}
	if append.Seq > 1 {
		var data any
		if json.Unmarshal(append.Event.Data, &data) != nil {
			return nil, 400, errors.New("event data is invalid")
		}
		event := events.Event{TS: append.Event.TS, SessionID: append.ChatID, RunID: append.Event.RunID, Type: append.Event.Type, Data: data}
		if err := s.registry.ApplyMirrored(event); err != nil {
			return nil, 409, err
		}
		s.bus.Publish(event)
	}
	metadata.Origin, metadata.Owner, metadata.MirrorSeq = append.Origin, append.Owner, append.Seq
	metadata.MirrorHashes = addMirrorHash(metadata.MirrorHashes, hash)
	if err := writeMirrorMetadata(folder, metadata); err != nil {
		return nil, 500, err
	}
	return map[string]any{"chat_id": append.ChatID, "last_seq": append.Seq}, 200, nil
}

func addMirrorHash(values []string, value string) []string { return append(values, value) }

func writeMirrorMetadata(folder string, metadata chatstore.Metadata) error {
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		if err = chatstore.WriteMetadata(folder, metadata); err == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return err
}

func (s *Server) mirrorMetadata(id string) (chatstore.Metadata, string, error) {
	if s.chatStore == nil {
		return chatstore.Metadata{}, "", errors.New("chat storage unavailable")
	}
	entry, found, err := s.chatStore.Find(id)
	if err != nil || !found {
		return chatstore.Metadata{}, "", errors.Join(err, os.ErrNotExist)
	}
	return entry.Metadata, entry.Path, nil
}

func validateMirrorFiles(folder string, files []mirrorFile) error {
	for _, file := range files {
		path := filepath.ToSlash(file.Path)
		if !strings.HasPrefix(path, "attachments/") || strings.Contains(strings.TrimPrefix(path, "attachments/"), "/") || filepath.ToSlash(filepath.Clean(file.Path)) != path {
			return fmt.Errorf("file path %q is not an attachment child", file.Path)
		}
		content, err := base64.RawURLEncoding.DecodeString(file.Content)
		if err != nil || int64(len(content)) != file.Bytes {
			return fmt.Errorf("file %q length does not match", file.Path)
		}
		digest := sha256.Sum256(content)
		if !strings.EqualFold(hex.EncodeToString(digest[:]), file.SHA256) {
			return fmt.Errorf("file %q digest does not match", file.Path)
		}
		if current, err := os.ReadFile(filepath.Join(folder, filepath.FromSlash(path))); err == nil && string(current) != string(content) {
			return fmt.Errorf("file %q already exists with different bytes", file.Path)
		}
	}
	return nil
}

func writeMirrorFiles(folder string, files []mirrorFile) error {
	if len(files) == 0 {
		return nil
	}
	root := filepath.Join(folder, "attachments")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	for _, file := range files {
		content, _ := base64.RawURLEncoding.DecodeString(file.Content)
		path := filepath.Join(folder, filepath.FromSlash(file.Path))
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) takeMirror(chatID string, afterSeq int, owner string) error {
	metadata, folder, err := s.mirrorMetadata(chatID)
	if err != nil {
		return errors.New("mirrored chat not found")
	}
	if metadata.MirrorSeq != afterSeq {
		return fmt.Errorf("sequence changed: have %d", metadata.MirrorSeq)
	}
	item, ok := s.registry.Get(chatID)
	if !ok {
		return errors.New("mirrored chat not found")
	}
	if status := item.Snapshot().Run.Status; status != "idle" {
		return errors.New("the phone is mid-turn")
	}
	if metadata.Owner == owner {
		return nil
	}
	metadata.Owner = owner
	if err := writeMirrorMetadata(folder, metadata); err != nil {
		return err
	}
	return s.registry.SetMirrorOwner(chatID, owner)
}

func (s *Server) outboundMirror(event events.Event) (map[string]any, bool) {
	metadata, folder, err := s.mirrorMetadata(event.SessionID)
	if err != nil || metadata.Owner != "pc" || event.Type == events.SessionUpdated {
		return nil, false
	}
	item, ok := s.registry.Get(event.SessionID)
	if !ok {
		return nil, false
	}
	files := mirrorEventFiles(item.Snapshot().WorkspaceDir, event)
	appendValue := mirrorAppend{ChatID: event.SessionID, Origin: metadata.Origin, Owner: "pc", Seq: metadata.MirrorSeq + 1, Event: mirrorEvent{TS: event.TS, RunID: event.RunID, Type: event.Type}}
	appendValue.Event.Data, _ = json.Marshal(event.Data)
	appendValue.Files = files
	encoded, _ := json.Marshal(appendValue)
	digest := sha256.Sum256(encoded)
	metadata.MirrorSeq++
	metadata.MirrorHashes = addMirrorHash(metadata.MirrorHashes, hex.EncodeToString(digest[:]))
	if writeMirrorMetadata(folder, metadata) != nil {
		return nil, false
	}
	body, _ := json.Marshal(appendValue)
	idDigest := sha256.Sum256(append(encoded, byte(metadata.MirrorSeq)))
	return map[string]any{"v": 1, "kind": "request", "id": hex.EncodeToString(idDigest[:16]), "route": "chat.mirror", "body": json.RawMessage(body)}, true
}

func mirrorEventFiles(root string, event events.Event) []mirrorFile {
	if event.Type != events.MessageAppended {
		return nil
	}
	raw, _ := json.Marshal(event.Data)
	var data struct {
		Message events.Message `json:"message"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return nil
	}
	var files []mirrorFile
	for _, attachment := range data.Message.Attachments {
		for _, relative := range []string{attachment.Path, attachment.Path + ".ocr.txt", attachment.Path + ".extract.txt"} {
			content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
			if err != nil {
				continue
			}
			digest := sha256.Sum256(content)
			files = append(files, mirrorFile{Path: relative, Bytes: int64(len(content)), SHA256: hex.EncodeToString(digest[:]), Content: base64.RawURLEncoding.EncodeToString(content)})
		}
	}
	return files
}

func (s *Server) takeMirrorHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var body struct {
		ChatID string `json:"chat_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	metadata, _, err := s.mirrorMetadata(body.ChatID)
	if err != nil || metadata.Owner != "phone" {
		writeError(w, 409, "this is not a phone-owned mirrored chat", "chat.mirror.take")
		return
	}
	requester, ok := s.brokerHost().(interface {
		RequestMirrorTake(context.Context, string, int) error
	})
	if !ok {
		writeError(w, 503, "the phone is not connected", "chat.mirror.take")
		return
	}
	if err := requester.RequestMirrorTake(r.Context(), body.ChatID, metadata.MirrorSeq); err != nil {
		writeError(w, 409, err.Error(), "chat.mirror.take")
		return
	}
	if err := s.takeMirror(body.ChatID, metadata.MirrorSeq, "pc"); err != nil {
		writeError(w, 409, err.Error(), "chat.mirror.take")
		return
	}
	writeJSON(w, 200, map[string]any{"chat_id": body.ChatID, "owner": "pc", "next_seq": metadata.MirrorSeq + 1})
}
