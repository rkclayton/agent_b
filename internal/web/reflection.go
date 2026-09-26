package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"harness/internal/events"
	"harness/internal/llm"
	"harness/internal/reflection"
)

// Item 17-i: reflection runs after a run closes and on a daily tick. The
// server subscribes to the event bus rather than calling into the run loop, so
// the loop's latency is untouched by construction: the run is already stopped
// and its outcome already published before anything here starts. A failure
// anywhere in reflection is logged and dropped.

type reflectionState struct {
	runner   *reflection.Runner
	store    *reflection.Store
	stop     chan struct{}
	inFlight sync.WaitGroup
}

// StartReflection opens the store and begins the pass. It is called once, from
// the harness's start-up, and is a no-op when the store cannot be opened.
func (s *Server) StartReflection(tick time.Duration) {
	store, err := reflection.Open(s.profileRoot())
	if err != nil {
		log.Printf("reflection: the store could not be opened, reflection is off: %v", err)
		return
	}
	runner := &reflection.Runner{
		Store:       store,
		Call:        s.reflectionCall,
		SessionLogs: s.reflectionSessionLogs,
		AllLogs:     s.reflectionAllLogs,
	}
	if s.memoryState != nil {
		runner.Noter = s.memoryState
	}
	runner.NoteWritten = func(summary reflection.Summary, note string) {
		// The note is published like the model's own, so the operator's
		// existing "delete this chat and drop its memory" path can revoke it
		// (v1.1.0/W6 cold review).
		s.bus.Publish(events.New(events.MemoryNoted, summary.SessionID, summary.RunID, map[string]any{"note": note, "path": summary.Workspace, "target": "folder", "source": "reflection"}))
	}
	s.mu.Lock()
	s.reflection = &reflectionState{runner: runner, store: store, stop: make(chan struct{})}
	state := s.reflection
	s.mu.Unlock()
	go s.reflectionLoop(tick, state)
}

// StopReflection ends the pass and closes the store.
func (s *Server) StopReflection() {
	s.mu.Lock()
	state := s.reflection
	s.reflection = nil
	s.mu.Unlock()
	if state == nil {
		return
	}
	close(state.stop)
	// Wait for a summary that is already in flight before closing the store.
	state.inFlight.Wait()
	if err := state.store.Close(); err != nil {
		log.Printf("reflection: closing the store: %v", err)
	}
}

// reflectionNow is the live state, or nil when reflection is off.
func (s *Server) reflectionNow() *reflectionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reflection
}

// reflectionLoop watches for closed runs and runs the fuller pass on the tick.
func (s *Server) reflectionLoop(tick time.Duration, state *reflectionState) {
	channel, unsubscribe := s.bus.Subscribe()
	defer unsubscribe()
	if tick <= 0 {
		tick = 24 * time.Hour
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-state.stop:
			return
		case event := <-channel:
			if event.Type != events.RunStopped {
				continue
			}
			data, _ := event.Data.(map[string]any)
			runID, _ := data["run_id"].(string)
			// Item 2ls: the floor, applied before anything is dispatched. A run
			// below it costs nothing rather than costing a model call and being
			// discarded afterwards.
			if skip, counts := s.belowReflectionFloor(data); skip {
				s.bus.Publish(events.New(events.ReflectionSkipped, event.SessionID, runID, counts))
				log.Printf("reflection skipped: below floor (%v model call(s), %v tool call(s)) for run %s",
					counts["model_calls"], counts["tool_calls"], runID)
				continue
			}
			state.inFlight.Add(1)
			go func(sessionID, runID string) {
				defer state.inFlight.Done()
				s.reflectOnClosedRun(sessionID, runID)
			}(event.SessionID, runID)
		case <-ticker.C:
			state.inFlight.Add(1)
			go func() {
				defer state.inFlight.Done()
				s.reflectionPass(false)
			}()
		}
	}
}

// belowReflectionFloor decides whether this run is too small to be worth a
// summary, from the counts item 2ls put on run.stopped.
//
// Item 2ls (a): the floor is three model calls OR one tool call, whichever is
// reached first — so a run clears it by doing enough of either. Both numbers are
// configuration, because they came from one machine's distribution.
//
// Item 2ls (d): A RUN THAT ENDED BADLY ALWAYS REFLECTS, however small. A
// two-call run that failed is exactly the one worth a summary, and a floor that
// skipped it would be saving money on the only runs anybody goes back to read.
func (s *Server) belowReflectionFloor(data map[string]any) (bool, map[string]any) {
	modelCalls, toolCalls := intField(data["model_calls"]), intField(data["tool_calls"])
	counts := map[string]any{"model_calls": modelCalls, "tool_calls": toolCalls}
	floor := s.ConfigSnapshot().Reflection.Floor
	if floor.ModelCalls <= 0 && floor.ToolCalls <= 0 {
		return false, counts
	}
	// (d): the ordinary stop reasons are the ones a floor may skip. Anything
	// else — a limit, a tool-error stop, a model failure — reflects regardless
	// of how small the run was.
	reason, _ := data["reason"].(string)
	if !ordinaryStopReason(reason) {
		counts["kept_because"] = reason
		return false, counts
	}
	if floor.ModelCalls > 0 && modelCalls >= floor.ModelCalls {
		return false, counts
	}
	if floor.ToolCalls > 0 && toolCalls >= floor.ToolCalls {
		return false, counts
	}
	counts["floor_model_calls"], counts["floor_tool_calls"] = floor.ModelCalls, floor.ToolCalls
	return true, counts
}

// ordinaryStopReason is the set item 2ls (d) permits the floor to skip: a run
// that simply finished, or that the operator ended deliberately. Everything else
// is a run somebody may want to read about.
func ordinaryStopReason(reason string) bool {
	switch reason {
	case "done", "user_stop", "cancellation_requested", "mailbox_stop":
		return true
	}
	return false
}

func intField(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	}
	return 0
}

// reflectOnClosedRun summarises one finished run.
func (s *Server) reflectOnClosedRun(sessionID, runID string) {
	state := s.reflectionNow()
	if state == nil || sessionID == "" || runID == "" {
		return
	}
	workspace, planID, connectionID := s.reflectionSessionFacts(sessionID)
	if connectionID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// No aux connection exists on this schema: the run's own connection summarises
	// and the summary says so (item 17-i's @verify, answered in v1.1.0/W1).
	summary := state.runner.SummariseRun(ctx, sessionID, runID, workspace, planID, connectionID, false)
	if summary.Failed != "" {
		log.Printf("reflection: the summary for run %s was not written (the run is unaffected): %s", runID, summary.Failed)
	}
}

// reflectionPass runs the fuller pass.
func (s *Server) reflectionPass(manual bool) (reflection.PassResult, error) {
	state := s.reflectionNow()
	if state == nil {
		return reflection.PassResult{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	result, err := state.runner.Pass(ctx, manual)
	if err != nil {
		log.Printf("reflection: the pass did not finish: %v", err)
	}
	for _, skipped := range result.Skipped {
		log.Printf("reflection: skipped %s", skipped)
	}
	// A pass may have proposed a plan; the operator meets it as an ordinary
	// card when they are next here (v1.1.1/W3).
	s.OfferReflectionProposals()
	return result, err
}

// reflectionSessionFacts reads the workspace, plan and connection of a chat.
func (s *Server) reflectionSessionFacts(sessionID string) (workspace, planID, connectionID string) {
	if s.registry == nil {
		return "", "", ""
	}
	item, ok := s.registry.Get(sessionID)
	if !ok {
		return "", "", ""
	}
	snapshot := item.Snapshot()
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, found := s.cfg.Agent(snapshot.AgentID)
	if !found {
		return snapshot.Workspace, snapshot.PlanID, ""
	}
	return snapshot.Workspace, snapshot.PlanID, agent.ConnectionFor(snapshot.Role)
}

// reflectionCall is reflection's one model call.
func (s *Server) reflectionCall(ctx context.Context, connectionID, system, user string) (string, error) {
	s.mu.Lock()
	connection, ok := s.cfg.Connection(connectionID)
	s.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("reflection: no connection %q", connectionID)
	}
	client := llm.New(connection)
	response, err := client.Chat(ctx, llm.Request{Messages: []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}, MaxTokens: 700})
	if err != nil {
		return "", err
	}
	return response.Content, nil
}

// reflectionSessionLogs and reflectionAllLogs give the pass the JSONL it reads.
func (s *Server) reflectionSessionLogs(sessionID string) []string {
	if s.writers == nil {
		return nil
	}
	inventory, err := s.writers.SessionInventory(sessionID)
	if err != nil {
		return nil
	}
	return append([]string{}, inventory.Paths...)
}

func (s *Server) reflectionAllLogs() []string {
	if s.writers == nil {
		return nil
	}
	paths, err := s.writers.DurableChatPaths()
	if err != nil {
		return nil
	}
	return paths
}

// reflectionResponse is what Console reads: the latest overview and the latest
// tool-candidate report, as text. Read-only; no control is added.
type reflectionResponse struct {
	Enabled  bool   `json:"enabled"`
	At       string `json:"at,omitempty"`
	PlanID   string `json:"plan_id,omitempty"`
	Tier     string `json:"tier,omitempty"`
	Overview string `json:"overview,omitempty"`
	ReportAt string `json:"report_at,omitempty"`
	Report   string `json:"report,omitempty"`
}

func (s *Server) reflectionEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	response := reflectionResponse{}
	if state := s.reflectionNow(); state != nil {
		response.Enabled = true
		planID := r.URL.Query().Get("plan_id")
		if overviews, err := state.store.Overviews(planID, 1); err == nil && len(overviews) > 0 {
			response.At = overviews[0].At.Format(time.RFC3339)
			response.PlanID = overviews[0].PlanID
			response.Tier = overviews[0].Tier
			response.Overview = overviews[0].Text
		}
		if report, err := state.store.LatestReport(); err == nil && report.Text != "" {
			response.ReportAt = report.At.Format(time.RFC3339)
			response.Report = report.Text
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
