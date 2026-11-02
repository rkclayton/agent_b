package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/llm"
	"harness/internal/session"
)

const (
	skillAuthorChatLabel = "Phone requests"
	skillAuthorTextLimit = 16 << 10
)

type skillAuthorRequest struct {
	ID    string   `json:"id"`
	Texts []string `json:"texts"`
	Kind  string   `json:"kind"`
}

type skillAuthorField struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Unit       string   `json:"unit,omitempty"`
	Examples   []string `json:"examples"`
	Everywhere bool     `json:"everywhere"`
}

type skillAuthorReply struct {
	ID     string             `json:"id"`
	Fields []skillAuthorField `json:"fields,omitempty"`
	Error  string             `json:"error,omitempty"`
}

func (s *Server) dispatchSkillAuthor(unit appRequestUnit) []byte {
	var request skillAuthorRequest
	if json.Unmarshal(unit.Body, &request) != nil || request.ID == "" || request.ID != unit.ID || request.Kind != "fields" || len(request.Texts) == 0 || len(request.Texts) > 5 {
		return s.skillAuthorProblem(unit.ID, http.StatusBadRequest, "skill.author needs the same id, kind fields, and one to five texts")
	}
	for _, text := range request.Texts {
		if len([]byte(text)) > skillAuthorTextLimit {
			return s.skillAuthorProblem(unit.ID, http.StatusRequestEntityTooLarge, "each skill.author text is limited to 16 KiB")
		}
	}

	s.skillAuthorMu.Lock()
	defer s.skillAuthorMu.Unlock()

	cfg := s.ConfigSnapshot()
	agentID := cfg.DefaultAgentID()
	selected, ok := cfg.AgentConnection(agentID, "b")
	if !ok {
		return s.skillAuthorProblem(unit.ID, http.StatusServiceUnavailable, "the agent's b connection is unavailable")
	}
	connection, ok := s.Connection(selected.ID)
	if !ok {
		return s.skillAuthorProblem(unit.ID, http.StatusServiceUnavailable, "the agent's b connection is unavailable")
	}
	chat, err := s.skillAuthorSession(agentID)
	if err != nil {
		return s.skillAuthorProblem(unit.ID, http.StatusInternalServerError, err.Error())
	}
	if chat.Snapshot().Run.Status != "idle" {
		return s.skillAuthorProblem(unit.ID, http.StatusConflict, "Phone requests is already running")
	}
	prompt, err := os.ReadFile(filepath.Join(s.roots.Application, "prompts", "skill-author.md"))
	if err != nil {
		return s.skillAuthorProblem(unit.ID, http.StatusInternalServerError, "the skill author prompt is unavailable")
	}

	runID := "skill-author-" + newMutationToken()
	requestBody, _ := json.Marshal(request)
	userMessage := events.Message{ID: "m-" + newMutationToken(), Role: "user", Category: "history", Content: string(requestBody), Turn: 1}
	chat.Append(userMessage)
	chat.SetRun(session.RunState{Status: "running", RunID: runID, Turn: 1})
	s.bus.Publish(events.New(events.MessageAppended, chat.ID, runID, map[string]any{"message": userMessage}))
	s.bus.Publish(events.New(events.RunStarted, chat.ID, runID, map[string]any{"run_id": runID, "user_message_id": userMessage.ID}))

	texts, _ := json.Marshal(request.Texts)
	modelRequest := llm.Request{Messages: []llm.Message{
		{Role: "system", Content: strings.TrimSpace(string(prompt))},
		{Role: "user", Content: string(texts)},
	}, MaxTokens: 4096}
	s.bus.Publish(events.New(events.ModelRequest, chat.ID, runID, map[string]any{"message_count": 2, "tool_count": 0, "model": connection.Model}))
	chatCall := s.skillAuthorChat
	if chatCall == nil {
		chatCall = func(ctx context.Context, c *config.Connection, request llm.Request) (llm.Response, error) {
			return llm.New(c).Chat(ctx, request)
		}
	}
	response, modelErr := chatCall(context.Background(), connection, modelRequest)
	if modelErr != nil {
		return s.finishSkillAuthorError(chat, runID, unit.ID, http.StatusBadGateway, "the model did not answer")
	}
	s.bus.Publish(events.New(events.ModelResponse, chat.ID, runID, map[string]any{"finish_reason": response.FinishReason, "tool_calls": 0, "content": response.Content}))

	var decoded struct {
		Fields []skillAuthorField `json:"fields"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(response.Content)), &decoded) != nil || len(decoded.Fields) == 0 || len(decoded.Fields) > 64 {
		return s.finishSkillAuthorError(chat, runID, unit.ID, http.StatusBadGateway, "the model's field list was not valid")
	}
	for _, field := range decoded.Fields {
		if !validSkillAuthorField(field, request.Texts) {
			return s.finishSkillAuthorError(chat, runID, unit.ID, http.StatusUnprocessableEntity, "the model named a field that the texts do not contain")
		}
	}
	reply := skillAuthorReply{ID: unit.ID, Fields: decoded.Fields}
	encoded, _ := json.Marshal(reply)
	s.finishSkillAuthorRun(chat, runID, string(encoded), "done")
	return s.appResponse(unit.ID, http.StatusOK, reply)
}

func (s *Server) skillAuthorSession(agentID string) (*session.Session, error) {
	if s.registry == nil {
		return nil, fmt.Errorf("chat runtime unavailable")
	}
	for _, item := range s.registry.List() {
		if item.Snapshot().Label == skillAuthorChatLabel {
			if item.IsClosed() {
				if err := s.registry.Reopen(item.ID); err != nil {
					return nil, err
				}
			}
			return item, nil
		}
	}
	return s.registry.CreateOperatorRole(skillAuthorChatLabel, agentID, "", "b", "")
}

func validSkillAuthorField(field skillAuthorField, texts []string) bool {
	name := strings.TrimSpace(field.Name)
	if name == "" || (field.Type != "number" && field.Type != "text" && field.Type != "date") || len(field.Examples) != len(texts) {
		return false
	}
	needle := strings.ToLower(name)
	for _, text := range texts {
		if strings.Contains(strings.ToLower(text), needle) {
			return true
		}
	}
	return false
}

func (s *Server) finishSkillAuthorError(chat *session.Session, runID, id string, status int, detail string) []byte {
	reply := skillAuthorReply{ID: id, Error: detail}
	encoded, _ := json.Marshal(reply)
	s.finishSkillAuthorRun(chat, runID, string(encoded), "error")
	return s.appResponse(id, status, reply)
}

func (s *Server) finishSkillAuthorRun(chat *session.Session, runID, content, reason string) {
	message := events.Message{ID: "m-" + newMutationToken(), Role: "assistant", Category: "history", Content: content, Turn: 1}
	chat.Append(message)
	chat.SetRun(session.RunState{Status: "idle", LastRunID: runID, LastStopReason: reason, ResultLabel: reason})
	s.bus.Publish(events.New(events.MessageAppended, chat.ID, runID, map[string]any{"message": message}))
	s.bus.Publish(events.New(events.RunStopped, chat.ID, runID, map[string]any{"reason": reason, "turns": 1}))
}

func (s *Server) skillAuthorProblem(id string, status int, detail string) []byte {
	return s.appResponse(id, status, skillAuthorReply{ID: id, Error: detail})
}
