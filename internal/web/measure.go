package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/llm"
)

const measurementProvenance = "setup-wizard-ten-briefs-n1"

type measureState struct {
	Running bool                `json:"running"`
	Text    string              `json:"text"`
	Error   string              `json:"error,omitempty"`
	Result  *config.Measurement `json:"result,omitempty"`
}

var measurementBriefs = []string{
	"Read README.md and report its first heading.",
	"List the files in the current folder.",
	"Search Go files for the text TODO.",
	"Read lines 1 through 5 of go.mod.",
	"List the files below internal.",
	"Search Markdown files for the word security.",
	"Read INTERFACES.md and report its first heading.",
	"List the files below scripts.",
	"Search JavaScript files for setup-step.",
	"Read SECURITY.md and report its first heading.",
}

var measurementTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "inspect_workspace",
		"description": "Inspect the workspace to answer the brief.",
		"parameters": map[string]any{
			"type":       "object",
			"properties": map[string]any{"request": map[string]any{"type": "string"}},
			"required":   []string{"request"},
		},
	},
}

func (s *Server) measureProfile(w http.ResponseWriter, r *http.Request) {
	profileID := strings.TrimSpace(r.URL.Query().Get("profile_id"))
	switch r.Method {
	case http.MethodGet:
		s.measureMu.RLock()
		state, ok := s.measurements[profileID]
		s.measureMu.RUnlock()
		if !ok {
			state = measureState{Text: "unmeasured"}
		}
		writeJSON(w, http.StatusOK, state)
	case http.MethodPost:
		var body struct {
			ProfileID string `json:"profile_id"`
		}
		if !decode(w, r, &body) {
			return
		}
		profileID = strings.TrimSpace(body.ProfileID)
		profile, ok := s.Profile(profileID)
		if !ok {
			writeError(w, http.StatusNotFound, "profile not found", "profile_id")
			return
		}
		s.measureMu.Lock()
		if s.measurements[profileID].Running {
			s.measureMu.Unlock()
			writeError(w, http.StatusConflict, "this profile is already being measured", "profile_id")
			return
		}
		s.measurements[profileID] = measureState{Running: true, Text: "Starting ten briefs"}
		s.measureMu.Unlock()
		go s.runMeasurement(profileID, *profile)
		writeJSON(w, http.StatusAccepted, s.measurements[profileID])
	default:
		method(w)
	}
}

func (s *Server) runMeasurement(profileID string, profile config.Profile) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client := llm.New(&profile)
	passed, toolErrors := 0, 0
	capped := false
	var runErr error
	for index, brief := range measurementBriefs {
		s.setMeasurement(profileID, measureState{Running: true, Text: fmt.Sprintf("Brief %d of %d", index+1, len(measurementBriefs))})
		response, err := client.Chat(ctx, llm.Request{
			Messages: []llm.Message{
				{Role: "system", Content: "Call inspect_workspace exactly once. Put the brief in its request argument. Do not answer in prose."},
				{Role: "user", Content: brief},
			},
			Tools: []any{measurementTool}, ToolChoice: "required", MaxTokens: 256,
		})
		if err != nil {
			if ctx.Err() != nil {
				capped = true
				break
			}
			runErr = err
			toolErrors++
			continue
		}
		if validMeasurementCall(response.ToolCalls, brief) {
			passed++
		} else {
			toolErrors++
		}
	}
	result := &config.Measurement{
		Passed: passed, Total: len(measurementBriefs), ToolErrors: toolErrors,
		ToolErrorRate: float64(toolErrors) / float64(len(measurementBriefs)), Trials: 1,
		Provenance: measurementProvenance, MeasuredAt: time.Now().UTC().Format(time.RFC3339),
		DurationMS: time.Since(started).Milliseconds(), Capped: capped,
	}
	if err := s.storeMeasurement(profileID, result); err != nil {
		runErr = err
	}
	state := measureState{Text: fmt.Sprintf("%d/%d briefs passed", passed, len(measurementBriefs)), Result: result}
	if runErr != nil {
		state.Error = runErr.Error()
	}
	s.setMeasurement(profileID, state)
}

func validMeasurementCall(calls []llm.ToolCall, brief string) bool {
	if len(calls) != 1 || calls[0].Function.Name != "inspect_workspace" {
		return false
	}
	var args struct {
		Request string `json:"request"`
	}
	return json.Unmarshal([]byte(calls[0].Function.Arguments), &args) == nil && strings.TrimSpace(args.Request) == brief
}

func (s *Server) setMeasurement(profileID string, state measureState) {
	s.measureMu.Lock()
	s.measurements[profileID] = state
	s.measureMu.Unlock()
}

func (s *Server) storeMeasurement(profileID string, result *config.Measurement) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.cfg.Servers {
		if s.cfg.Servers[index].ID != profileID {
			continue
		}
		s.cfg.Servers[index].Measurement = result
		if err := s.cfg.Save(s.configPath); err != nil {
			return err
		}
		s.bus.Publish(events.New(events.ConfigChanged, "", "", map[string]any{"config": s.cfg.Masked()}))
		return nil
	}
	return fmt.Errorf("profile %q disappeared during measurement", profileID)
}
