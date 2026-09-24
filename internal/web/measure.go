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

func (s *Server) measureConnection(w http.ResponseWriter, r *http.Request) {
	connectionID := strings.TrimSpace(r.URL.Query().Get("connection_id"))
	switch r.Method {
	case http.MethodGet:
		s.measureMu.RLock()
		state, ok := s.measurements[connectionID]
		s.measureMu.RUnlock()
		if !ok {
			state = measureState{Text: "unmeasured"}
		}
		writeJSON(w, http.StatusOK, state)
	case http.MethodPost:
		var body struct {
			ConnectionID string `json:"connection_id"`
		}
		if !decode(w, r, &body) {
			return
		}
		connectionID = strings.TrimSpace(body.ConnectionID)
		connection, ok := s.Connection(connectionID)
		if !ok {
			writeError(w, http.StatusNotFound, "connection not found", "connection_id")
			return
		}
		s.measureMu.Lock()
		if s.measurements[connectionID].Running {
			s.measureMu.Unlock()
			writeError(w, http.StatusConflict, "this connection is already being measured", "connection_id")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		s.measurements[connectionID] = measureState{Running: true, Text: "Starting ten briefs"}
		s.measureCancels[connectionID] = cancel
		s.measureMu.Unlock()
		go s.runMeasurement(ctx, connectionID, *connection)
		writeJSON(w, http.StatusAccepted, s.measurements[connectionID])
	case http.MethodDelete:
		if connectionID == "" {
			writeError(w, http.StatusBadRequest, "connection_id is required", "connection_id")
			return
		}
		s.measureMu.Lock()
		cancel := s.measureCancels[connectionID]
		state := s.measurements[connectionID]
		if cancel != nil && state.Running {
			state.Text = "Stopping after the current brief"
			s.measurements[connectionID] = state
			cancel()
		}
		s.measureMu.Unlock()
		if cancel == nil || !state.Running {
			writeError(w, http.StatusConflict, "this connection is not being measured", "connection_id")
			return
		}
		writeJSON(w, http.StatusAccepted, state)
	default:
		method(w)
	}
}

func (s *Server) runMeasurement(ctx context.Context, connectionID string, connection config.Connection) {
	started := time.Now()
	defer func() {
		s.measureMu.Lock()
		delete(s.measureCancels, connectionID)
		s.measureMu.Unlock()
	}()
	client := llm.New(&connection)
	passed, toolErrors, briefsRun := 0, 0, 0
	capped := false
	stopped := false
	var runErr error
	for index, brief := range measurementBriefs {
		s.setMeasurement(connectionID, measureState{Running: true, Text: fmt.Sprintf("Brief %d of %d", index+1, len(measurementBriefs))})
		briefsRun++
		response, err := client.Chat(ctx, llm.Request{
			Messages: []llm.Message{
				{Role: "system", Content: "Call inspect_workspace exactly once. Put the brief in its request argument. Do not answer in prose."},
				{Role: "user", Content: brief},
			},
			Tools: []any{measurementTool}, ToolChoice: "required", MaxTokens: 256,
		})
		if err != nil {
			if ctx.Err() != nil {
				capped = ctx.Err() == context.DeadlineExceeded
				stopped = ctx.Err() == context.Canceled
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
		Passed: passed, Total: len(measurementBriefs), BriefsRun: briefsRun, ToolErrors: toolErrors,
		ToolErrorRate: measurementErrorRate(toolErrors, briefsRun), Trials: 1,
		Provenance: measurementProvenance, MeasuredAt: time.Now().UTC().Format(time.RFC3339),
		DurationMS: time.Since(started).Milliseconds(), Capped: capped, Stopped: stopped,
	}
	if err := s.storeMeasurement(connectionID, result); err != nil {
		runErr = err
	}
	state := measureState{Text: fmt.Sprintf("%d/%d briefs passed; %d ran", passed, len(measurementBriefs), briefsRun), Result: result}
	if runErr != nil {
		state.Error = runErr.Error()
	}
	s.setMeasurement(connectionID, state)
}

func measurementErrorRate(toolErrors, briefsRun int) float64 {
	if briefsRun == 0 {
		return 0
	}
	return float64(toolErrors) / float64(briefsRun)
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

func (s *Server) setMeasurement(connectionID string, state measureState) {
	s.measureMu.Lock()
	s.measurements[connectionID] = state
	s.measureMu.Unlock()
}

func (s *Server) storeMeasurement(connectionID string, result *config.Measurement) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.cfg.Connections {
		if s.cfg.Connections[index].ID != connectionID {
			continue
		}
		s.cfg.Connections[index].Measurement = result
		if err := s.cfg.Save(s.configPath); err != nil {
			return err
		}
		s.bus.Publish(events.New(events.ConfigChanged, "", "", map[string]any{"config": s.cfg.Masked()}))
		return nil
	}
	return fmt.Errorf("connection %q disappeared during measurement", connectionID)
}
