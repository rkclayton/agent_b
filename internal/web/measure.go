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
		s.measurements[connectionID] = measureState{Running: true, Text: "brief 0 of " + strconv.Itoa(len(measurementBriefs)) + " — thinking off", Total: len(measurementBriefs)}
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

	// Item 2ih (b) and (c): TWO ARMS, then the rule decides.
	//
	// The arms are the same ten briefs with thinking on and thinking off, and
	// the only difference between them is the switch -- if the two differed in
	// any other way the comparison would measure that instead. The rule that
	// consumes them is config.DecideReasoning, which shipped at v1.20.0 and was
	// fed live numbers at rel-1.21.0; nothing here re-derives it.
	//
	// The off-arm runs FIRST. It is the cheaper one, and if the operator stops
	// the measurement or the cap bites, the arm that finished is the one that
	// costs least to repeat.
	off, offErr := s.measureArm(ctx, connectionID, connection, false)

	// (f): A MEASUREMENT THAT CANNOT RUN SAYS SO AND WRITES NOTHING.
	//
	// If the connection advertises no way to express the switch, both arms would
	// go out identical and the comparison would measure sampling noise and call
	// it reasoning. An on-arm that never ran is exactly what DecideReasoning
	// refuses to decide from, and its line says the measurement did not run.
	var on config.MeasurementArm
	var onErr error
	if reasoningControlOf(connection) == "" {
		on = config.MeasurementArm{Total: len(measurementBriefs)}
	} else {
		on, onErr = s.measureArm(ctx, connectionID, connection, true)
	}
	runErr := offErr
	if runErr == nil {
		runErr = onErr
	}

	decision := config.DecideReasoning(on, off)
	result := &config.Measurement{
		Passed: off.Passed, Total: len(measurementBriefs), BriefsRun: off.BriefsRun + on.BriefsRun,
		ToolErrors:    off.ToolErrors + on.ToolErrors,
		ToolErrorRate: measurementErrorRate(off.ToolErrors+on.ToolErrors, off.BriefsRun+on.BriefsRun), Trials: 2,
		Provenance: measurementProvenance, MeasuredAt: time.Now().UTC().Format(time.RFC3339),
		DurationMS: time.Since(started).Milliseconds(),
		Capped:     off.Capped || on.Capped, Stopped: off.Stopped || on.Stopped,
		ReasoningOn: &on, ReasoningOff: &off, Decision: &decision,
		NCtx: connection.Context.NCtx, WindowTokens: connection.Context.NCtx - connection.Context.ReserveOutput,
	}

	if err := s.storeMeasurement(connectionID, result); err != nil {
		runErr = err
	}
	state := measureState{Text: decision.Line, Result: result}
	if runErr != nil {
		state.Error = runErr.Error()
	}
	s.setMeasurement(connectionID, state)
}

// measureArm is one arm of item 2ih (b): the same ten briefs, with the thinking
// switch in one position.
func (s *Server) measureArm(ctx context.Context, connectionID string, connection config.Connection, thinking bool) (config.MeasurementArm, error) {
	label := "thinking off"
	if thinking {
		label = "thinking on"
	}
	// THE SWITCH LIVES IN TWO PLACES AND BOTH HAVE TO MOVE, which is not obvious
	// and cost this item a test to find out. Request.Thinking chooses the
	// SAMPLING COLUMN; what the server is actually told is the CONNECTION's
	// Reasoning.Enabled, through whichever control it advertises --
	// chat_template_kwargs for a llama.cpp template, a top-level field
	// otherwise. An arm that flipped only the request would have measured two
	// sampling columns against one thinking model and called the difference
	// reasoning.
	//
	// The copy is local to the arm, so nothing is written to the operator's
	// configuration until (c) writes the decision.
	armConnection := connection
	armConnection.Reasoning.Enabled = thinking
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
			Text:      fmt.Sprintf("brief %d of %d — %s", index+1, len(measurementBriefs), label),
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
			// The ONLY difference between the arms.
			Thinking: thinking,
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

// writeReasoningDecision is (c)'s write: the switch the harness measured, onto
// the connection it measured, through the scope boundary item 2m0 built.
func (s *Server) writeReasoningDecision(connectionID string, decision config.ReasoningDecision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.cfg.Connections {
		if s.cfg.Connections[index].ID != connectionID {
			continue
		}
		s.cfg.Connections[index].Reasoning.Enabled = decision.Enabled
		// (c) and @keep: the cap is written only when one was OBSERVED, so an
		// operator's own value survives a measurement that could not report one.
		if decision.ReasoningCap > 0 {
			s.cfg.Connections[index].Reasoning.MaxTokens = decision.ReasoningCap
		}
		return s.saveMachineConfig(*s.cfg)
	}
	return nil
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

// reasoningControlOf resolves how this connection expresses the thinking switch:
// what the operator configured, or what the probe found, or nothing at all.
func reasoningControlOf(connection config.Connection) string {
	control := connection.Reasoning.Control
	if control == "auto" {
		control = connection.Capabilities.ReasoningControl
	}
	if control == "none" {
		return ""
	}
	return control
}
