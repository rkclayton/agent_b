package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/llm"
	"harness/internal/probe"
)

const measurementProvenance = "setup-wizard-ten-briefs-n1"

// Item 2nn (c): the state carries the COUNT as numbers, not only inside a
// sentence. The screen shows item 2nh's determinate bar while the harness runs,
// and a bar cannot be drawn from prose -- the briefs are a known ten per arm, so
// the honest mode here is determinate and the numbers have to travel.
type measureState struct {
	Running   bool                `json:"running"`
	Text      string              `json:"text"`
	Processed int                 `json:"processed,omitempty"`
	Total     int                 `json:"total,omitempty"`
	Error     string              `json:"error,omitempty"`
	Result    *config.Measurement `json:"result,omitempty"`
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
		s.measurements[connectionID] = measureState{Running: true, Text: "brief 0 of " + strconv.Itoa(len(measurementBriefs)), Total: len(measurementBriefs)}
		s.measureCancels[connectionID] = cancel
		s.measureMu.Unlock()
		go s.runEvaluation(ctx, connectionID, *connection)
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
			state.Text = fmt.Sprintf("stopping after brief %d of %d", state.Processed, state.Total)
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

func (s *Server) runEvaluation(ctx context.Context, connectionID string, connection config.Connection) {
	caps, findings, err := probe.Eval(ctx, &connection)
	if err != nil {
		s.measureMu.Lock()
		delete(s.measureCancels, connectionID)
		s.measureMu.Unlock()
		s.setMeasurement(connectionID, measureState{Error: err.Error(), Text: "Eval failed during capability checks"})
		return
	}
	caps.Findings = findings
	if ceiling, by := usableContextCeiling(caps); ceiling > 0 {
		findings = append(findings, fmt.Sprintf("usable ceiling: %d tokens set by %s", ceiling, by))
		if connection.Context.NCtx > ceiling {
			findings = append(findings, fmt.Sprintf("saved context %d is %d above measured ceiling; saved value is kept", connection.Context.NCtx, connection.Context.NCtx-ceiling))
		}
	}
	caps.Findings = findings
	s.mu.Lock()
	for i := range s.cfg.Connections {
		if s.cfg.Connections[i].ID == connectionID {
			s.cfg.Connections[i].Capabilities = caps
			_ = s.saveMachineConfig(*s.cfg)
			connection.Capabilities = caps
			break
		}
	}
	s.mu.Unlock()
	s.runMeasurement(ctx, connectionID, connection)
}

func (s *Server) runMeasurement(ctx context.Context, connectionID string, connection config.Connection) {
	started := time.Now()
	defer func() {
		s.measureMu.Lock()
		delete(s.measureCancels, connectionID)
		s.measureMu.Unlock()
	}()

	measured, runErr := s.measureArm(ctx, connectionID, connection)
	result := &config.Measurement{
		Passed: measured.Passed, Total: len(measurementBriefs), BriefsRun: measured.BriefsRun,
		ToolErrors: measured.ToolErrors, ToolErrorRate: measurementErrorRate(measured.ToolErrors, measured.BriefsRun), Trials: 1,
		Provenance: measurementProvenance, MeasuredAt: time.Now().UTC().Format(time.RFC3339),
		DurationMS:   time.Since(started).Milliseconds(),
		CompletionMS: measured.CompletionMS,
		Capped:       measured.Capped, Stopped: measured.Stopped,
		NCtx: connection.Context.NCtx, WindowTokens: connection.Context.NCtx - connection.Context.ReserveOutput,
	}

	if err := s.storeMeasurement(connectionID, result); err != nil {
		runErr = err
	}
	state := measureState{Text: fmt.Sprintf("%d/%d passed · %d tool errors", measured.Passed, measured.Total, measured.ToolErrors), Result: result}
	if runErr != nil {
		state.Error = runErr.Error()
	}
	s.setMeasurement(connectionID, state)
}

func (s *Server) measureArm(ctx context.Context, connectionID string, connection config.Connection) (config.MeasurementArm, error) {
	armConnection := connection
	armConnection.Reasoning.Enabled = false
	client := llm.New(&armConnection)
	arm := config.MeasurementArm{Total: len(measurementBriefs)}
	var runErr error
	var completions []int64
	for index, brief := range measurementBriefs {
		if ctx.Err() != nil {
			break
		}
		s.setMeasurement(connectionID, measureState{
			Running:   true,
			Text:      fmt.Sprintf("brief %d of %d", index+1, len(measurementBriefs)),
			Processed: index + 1,
			Total:     len(measurementBriefs),
		})
		arm.BriefsRun++
		at := time.Now()
		response, err := client.Chat(ctx, llm.Request{
			Messages: []llm.Message{
				{Role: "system", Content: "Call inspect_workspace exactly once. Put the brief in its request argument. Do not answer in prose."},
				{Role: "user", Content: brief},
			},
			Tools: []any{measurementTool}, ToolChoice: "required", MaxTokens: 256,
		})
		completions = append(completions, time.Since(at).Milliseconds())
		if err != nil {
			if ctx.Err() != nil {
				arm.Capped = ctx.Err() == context.DeadlineExceeded
				arm.Stopped = ctx.Err() == context.Canceled
				break
			}
			runErr = err
			arm.ToolErrors++
			continue
		}
		switch {
		case validMeasurementCall(response.ToolCalls, brief):
			arm.Passed++
		case len(response.ToolCalls) == 0 && strings.TrimSpace(response.Content) == "":
			// (b)'s empty-reply rate: a turn that finished with neither a call nor
			// prose. rel-1.21.0 saw one of these on a live model, and it is the
			// clause the decision rule vetoes on.
			arm.EmptyReplies++
		default:
			arm.ToolErrors++
		}
		arm.ReasoningP95 = maxInt(arm.ReasoningP95, len(response.Reasoning))
	}
	arm.Ran = arm.BriefsRun > 0 && !arm.Stopped
	arm.CompletionMS = percentile95(completions)
	return arm, runErr
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// percentile95 is the 95th percentile of what was observed, which is what (c)
// derives the reasoning cap from. An empty sample is zero, and zero means the
// cap is left alone rather than invented.
func percentile95(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := (len(sorted) * 95) / 100
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
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
		if err := s.saveMachineConfig(*s.cfg); err != nil {
			return err
		}
		s.bus.Publish(events.New(events.ConfigChanged, "", "", map[string]any{"config": s.cfg.Masked()}))
		return nil
	}
	return fmt.Errorf("connection %q disappeared during measurement", connectionID)
}
