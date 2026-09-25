package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"harness/internal/config"
	contextmgr "harness/internal/context"
	"harness/internal/delivery"
	"harness/internal/events"
	"harness/internal/llm"
	"harness/internal/session"
	"harness/internal/tools"
)

type Runner struct {
	bus                *events.Bus
	tools              *tools.Registry
	prompt             *PromptRenderer
	connection         func(string) (*config.Connection, bool)
	cfg                func() config.Config
	gate               *Gate
	budget             *Budgeter
	compact            *contextmgr.Compactor
	toolActivity       func(string)
	deliver            func(*session.Session, string, []delivery.Source) delivery.Result
	shellGrantMu       sync.Mutex
	shellGrants        map[string][]shellRunGrant
	shellSessionGrants map[string][]shellSessionGrant
	fileGrantMu        sync.Mutex
	fileRunGrants      map[string]bool
	fileSessionGrants  map[string]string
	identityGrantMu    sync.Mutex
	identityChatGrants map[string]string
	policyGrantMu      sync.Mutex
	policyChatGrants   map[string]map[string]bool
	flights            *flightBook
	renameSession      func(string, string, string) error
	mailboxBoundary    func(context.Context, string, bool) BoundaryAction
	modelUnreachable   func(string, string)
	recordMessageLimit func(string, int) error
	ids                atomic.Int64
	nameAttempts       sync.Map
	messageLimits      sync.Map
}

type BoundaryAction struct {
	Stop     bool
	Revision string
	Delay    time.Duration
	Err      error
}

const minimumOutputFloor = 4096

func NewRunner(bus *events.Bus, registry *tools.Registry, prompt *PromptRenderer, connection func(string) (*config.Connection, bool), cfg func() config.Config) *Runner {
	return &Runner{bus: bus, tools: registry, prompt: prompt, connection: connection, cfg: cfg, gate: NewGate(bus, cfg), budget: NewBudgeter(), compact: contextmgr.New(bus), flights: newFlightBook()}
}
func (r *Runner) Configure(cfg config.Config) {
	r.tools.Configure(cfg)
	r.budget.InvalidateToolCosts()
}
func (r *Runner) Gate() *Gate                     { return r.gate }
func (r *Runner) SetToolActivity(fn func(string)) { r.toolActivity = fn }
func (r *Runner) SetDeliverer(fn func(*session.Session, string, []delivery.Source) delivery.Result) {
	r.deliver = fn
}
func (r *Runner) SetSessionRenamer(fn func(string, string, string) error) { r.renameSession = fn }
func (r *Runner) SetMailboxBoundary(fn func(context.Context, string, bool) BoundaryAction) {
	r.mailboxBoundary = fn
}
func (r *Runner) SetModelUnreachable(fn func(string, string))        { r.modelUnreachable = fn }
func (r *Runner) SetMessageLimitRecorder(fn func(string, int) error) { r.recordMessageLimit = fn }
func (r *Runner) BindDelegate(tool *tools.Delegate)                  { tool.SetRunner(r.runDelegate) }
func (r *Runner) AcceptPlanEdit(ctx context.Context, s *session.Session, path, oldText, newText string) tools.CallOutcome {
	if !s.BeginPlanAccept() {
		return tools.CallOutcome{Content: "error: plan acceptance is available only on the Plan page"}
	}
	defer s.EndPlanAccept()
	args := map[string]any{"path": path, "old_string": oldText, "new_string": newText}
	outcome := r.tools.CallDetailed(ctx, s, "edit_file", args)
	if outcome.OperatorOverrideAvailable {
		content, ok := r.tools.CallAsOperator(ctx, s, "edit_file", args)
		return tools.CallOutcome{Content: content, OK: ok, OperatorContext: true}
	}
	return outcome
}

// Verify runs an item's verifier command as the worker: the ordinary shell tool,
// through the same gate, grants and identity a model's shell call takes, so it
// can raise the same card. It reports whether the command exited 0. The run it
// belongs to has already stopped, so the session's run state is put back after.
func (r *Runner) Verify(ctx context.Context, s *session.Session, command string) (bool, string) {
	before := s.Snapshot().Run
	defer s.SetRun(before)
	runID := r.id("verify")
	outcome := r.executeTool(ctx, s, runID, runID+"-call", "shell", map[string]any{"command": command})
	return outcome.OK, outcome.Content
}
func (r *Runner) SettlePlanTurns(ctx context.Context, s *session.Session, itemID string, ids []string) bool {
	pointer := fmt.Sprintf("[settled → plan item %s]", itemID)
	p, ok := r.connection(s.ConnectionID)
	if !ok {
		return false
	}
	return r.compact.Settle(s, "", pointer, ids, func(text string) (int, bool) { return r.count(ctx, p, text) })
}
func (r *Runner) id(prefix string) string { return fmt.Sprintf("%s-%d", prefix, r.ids.Add(1)) }

func (r *Runner) runDelegate(ctx context.Context, parent *session.Session, task, thoroughness string) (tools.DelegateResult, error) {
	started := time.Now()
	turns := 8
	if thoroughness == "thorough" {
		turns = 20
	}
	parentState := parent.Snapshot()
	child := &session.Session{
		ID: parent.ID + "-delegate-" + r.id("e"), Label: "delegate", Role: "e", AgentID: parentState.AgentID,
		ParentSessionID: parent.ID,
		ConnectionID:    parentState.ConnectionID, Workspace: parentState.WorkspaceDir, PlanID: parentState.PlanID,
		PlanDir: parent.PlanDir, PlanRepo: parent.PlanRepo, PlansRoot: parent.PlansRoot, PlanRepos: parent.PlanRepos,
		NetworkBoundary: parentState.NetworkBoundary, NetworkBoundarySet: true, Runnable: true,
		Run: session.RunState{Status: "idle", MaxTurns: turns}, ToolsEnabled: map[string]bool{
			"read_file": true, "list_dir": true, "search": true, "fetch_url": true, "recall": true,
		}, ToolCalls: map[string]int{}, LastSeen: map[string]time.Time{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{},
	}
	connection, ok := r.connection(child.ConnectionID)
	if !ok {
		return tools.DelegateResult{}, fmt.Errorf("delegate connection %s is unavailable", child.ConnectionID)
	}
	message, _ := r.makeMessage(ctx, connection, "user", task, "history", 0)
	child.Append(message)
	r.bus.Publish(events.New(events.MessageAppended, child.ID, "", map[string]any{"message": message, "parent_session_id": parent.ID}))
	reason, detail, _ := r.Run(ctx, child, r.id("delegate"))
	if ctx.Err() != nil {
		return tools.DelegateResult{}, ctx.Err()
	}
	partial := reason == "turn_ceiling"
	if reason != "done" && !partial {
		return tools.DelegateResult{}, fmt.Errorf("delegate stopped: %s: %s", reason, detail)
	}
	transcript, summary := child.MessagesCopy(), ""
	for index := len(transcript) - 1; index >= 0; index-- {
		if transcript[index].Role == "assistant" && strings.TrimSpace(transcript[index].Content) != "" {
			summary = strings.TrimSpace(transcript[index].Content)
			break
		}
	}
	if summary == "" {
		summary = "No summary was produced before the child stopped."
		partial = true
	}
	if tokens, _ := r.count(ctx, connection, summary); tokens > 2000 {
		runes := []rune(summary)
		runes = runes[:max(1, len(runes)*2000/tokens)]
		summary, partial = strings.TrimSpace(string(runes))+"\n[summary capped at 2,000 tokens]", true
	}
	toolCalls := 0
	for _, tool := range child.Snapshot().Tools {
		toolCalls += tool.Calls
	}
	return tools.DelegateResult{Summary: summary, Partial: partial, DurationMS: time.Since(started).Milliseconds(), ToolCalls: toolCalls, Transcript: transcript}, nil
}

// ReserveIDs moves the id counter past floor, so ids minted after a restart
// never repeat one a restored chat already holds (item 2es).
func (r *Runner) ReserveIDs(floor int64) { reserveCounter(&r.ids, floor) }

func reserveCounter(counter *atomic.Int64, floor int64) {
	for {
		current := counter.Load()
		if current >= floor || counter.CompareAndSwap(current, floor) {
			return
		}
	}
}
func (r *Runner) AddUser(ctx context.Context, s *session.Session, text string) (events.Message, error) {
	return r.AddUserAttachments(ctx, s, text, nil)
}
func (r *Runner) AddUserAttachments(ctx context.Context, s *session.Session, text string, attachments []events.Attachment) (events.Message, error) {
	message, err := r.QueueUserAttachments(ctx, s, text, attachments)
	if err != nil {
		return events.Message{}, err
	}
	r.AppendUser(s, message)
	return message, nil
}
func (r *Runner) QueueUser(ctx context.Context, s *session.Session, text string) (events.Message, error) {
	return r.QueueUserAttachments(ctx, s, text, nil)
}
func (r *Runner) QueueUserAttachments(ctx context.Context, s *session.Session, text string, attachments []events.Attachment) (events.Message, error) {
	connection, ok := r.connection(s.ConnectionID)
	if !ok {
		return events.Message{}, fmt.Errorf("connection not found")
	}
	attachments = prepareNativeAttachmentsWithBudget(connection, attachments, remainingNativeAttachmentBudget(connection, s.MessagesCopy()))
	message := events.Message{ID: r.id("m"), Role: "user", Content: text, Category: "history", Attachments: append([]events.Attachment(nil), attachments...)}
	var tokens int
	var estimated bool
	if _, requested := requestedPlanPath(text); requested {
		tokens, estimated = estimatedTokenCount(renderedUserText(connection, s, message)), true
	} else {
		tokens, estimated = r.count(ctx, connection, renderedUserText(connection, s, message))
	}
	message.Tokens, message.Estimated = tokens, estimated
	return message, nil
}
func (r *Runner) AppendUser(s *session.Session, message events.Message) {
	s.Append(message)
	// Item 2go: the chat takes its name from this message if it has none yet.
	// Once, from the operator's own words, and never again by the harness.
	r.nameFromFirstMessage(s, message.Content)
	r.bus.Publish(events.New(events.MessageAppended, s.ID, "", map[string]any{"message": message}))
}

func serviceIdentityUnavailableData(reason string) map[string]any {
	return map[string]any{
		"message": "Service identity is not set up. Set it up now with one Windows prompt, or run as you for 20 minutes.",
		"reason":  reason,
		"actions": []string{"provision", "operator_mode"},
	}
}

func (r *Runner) Run(ctx context.Context, s *session.Session, runID string) (reason string, detail string, turns int) {
	s.MarkNetworkBoundaryStale(session.NetworkBoundary(r.cfg()))
	workspaceOK, workspaceReason, workspaceChanged := s.EnsureWorkspace()
	if workspaceChanged {
		snapshot := s.Snapshot()
		r.bus.Publish(events.New(events.SessionUpdated, s.ID, runID, map[string]any{
			"session_id": s.ID, "workspace_missing": snapshot.WorkspaceMissing,
			"runnable": snapshot.Runnable, "not_runnable_reason": snapshot.NotRunnableReason,
		}))
	}
	if !workspaceOK {
		return "workspace_not_runnable", workspaceReason, 0
	}
	s.ResetRunTouches()
	// Pin the message this run is answering before anything can compact. From
	// here to the end of the history is the task, and it is never summarised,
	// elided or superseded away.
	s.SetRunPin(session.RunPinFromTail(s.MessagesCopy()))
	defer s.SetRunPin("")
	r.beginFlight(s.ID, runID)
	defer r.endFlight(s.ID, runID)
	if cfg := r.cfg(); !cfg.Shell.ServiceAccount.Enabled {
		log.Printf("tool identity: process (service split disabled) session=%s run=%s", s.ID, runID)
	} else if !cfg.Shell.OperatorContext {
		if err := r.tools.PreflightServiceIdentity(); err != nil {
			log.Printf("tool identity: service identity not set up session=%s run=%s reason=%q", s.ID, runID, err.Error())
			r.bus.Publish(events.New(events.ServiceIdentityUnavailable, s.ID, runID, serviceIdentityUnavailableData(err.Error())))
		} else {
			log.Printf("tool identity: service session=%s run=%s", s.ID, runID)
		}
	} else {
		log.Printf("tool identity: operator mode session=%s run=%s", s.ID, runID)
	}
	runCfg := r.cfg().Run
	if runCfg.MaxWallClockSeconds <= 0 {
		runCfg.MaxWallClockSeconds = config.DefaultMaxWallClockSeconds
	}
	if runCfg.MaxToolCalls <= 0 {
		runCfg.MaxToolCalls = config.DefaultMaxToolCalls
	}
	var wallExceeded atomic.Bool
	wallContext, cancelWall := context.WithCancel(ctx)
	wallTimer := time.AfterFunc(time.Duration(runCfg.MaxWallClockSeconds)*time.Second, func() {
		wallExceeded.Store(true)
		cancelWall()
	})
	defer func() { wallTimer.Stop(); cancelWall() }()
	ctx = wallContext
	contextStop := func(turn int, fallback string) (string, string, int) {
		if wallExceeded.Load() {
			return "wall_clock", fmt.Sprintf("guessed wall-clock backstop reached after %d seconds", runCfg.MaxWallClockSeconds), turn
		}
		return r.stopped(s, runID, turn, fallback)
	}
	produced := map[string]delivery.Source{}
	workspaceBefore := workspaceFileSnapshot(s.Workspace)
	defer func() {
		for key, source := range workspaceFileChanges(s.Workspace, workspaceBefore) {
			produced[key] = source
		}
		result := delivery.Result{}
		r.stage(s, runID, turns, "append", func() {
			if r.deliver != nil {
				result = r.deliver(s, runID, delivery.SortedSources(produced))
			}
		})
		if reason == "turn_ceiling" {
			detail = turnCeilingDetail(turns, result)
		}
	}()
	defer r.lapseShellGrants(s, runID)
	defer r.lapseFileRunGrant(s, runID)
	if stop, detail := r.applyMailboxBoundary(ctx, s, runID, false); stop {
		if ctx.Err() != nil {
			return contextStop(0, detail)
		}
		return "mailbox_stop", detail, 0
	}
	connection, ok := r.connection(s.ConnectionID)
	if !ok {
		return "connection_not_runnable", "connection " + s.ConnectionID + " no longer exists", 0
	}
	connection, windowSource, windowErr := resolveContextWindow(ctx, connection)
	if windowErr != nil {
		return "connection_not_runnable", windowErr.Error(), 0
	}
	snapshot := s.Snapshot()
	if !snapshot.Runnable {
		return "connection_not_runnable", snapshot.NotRunnableReason, 0
	}
	if handled, registrationDetail := r.handlePlanRegistration(ctx, s, runID); handled {
		return "done", registrationDetail, 0
	}
	publishFinalBudget := true
	defer func() {
		if publishFinalBudget {
			if ctx.Err() == nil {
				r.PublishBudget(ctx, s)
				return
			}
			// Item 2fg: a Stop cancels ctx. The closing measurement is not part of
			// the stopped run: it runs on its own bounded context, after the run
			// has returned, so Stop's bound holds and a canceled request never
			// reads as an outage.
			go func() {
				closing, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
				defer cancel()
				r.PublishBudget(closing, s)
			}()
		}
	}()
	turn := 0
	toolCallsUsed := 0
	lengthSeen := false
	truncatedToolRetry := ""
	retryInstruction := ""
	malformedToolRetried := false
	// Item 2fv: a read_file window refused on two consecutive turns ends the
	// read; from then on the model answers from what it has read, without
	// tools. refusedTurn is the turn of the latest refusal (-1: none since the
	// last window that fit). The elide between the two turns has had its chance.
	refusedTurn, readCutShort := -1, false
	accountingRepairTried := false
	templateRetryTried := false
	softLineChecked := false
	messageLimitRetried := false
	guards := newRunGuards(runCfg.CycleWindow, runCfg.MaxConsecutiveToolErrors)
	currentReasoning := map[string]bool{}
	for {
		if ctx.Err() != nil {
			return contextStop(turn, "cancellation requested")
		}
		if stop, detail := r.applyMailboxBoundary(ctx, s, runID, false); stop {
			if ctx.Err() != nil {
				return contextStop(turn, detail)
			}
			return "mailbox_stop", detail, turn
		}
		turn++
		connection, ok = r.connection(s.ConnectionID)
		if !ok {
			return "connection_not_runnable", "connection " + s.ConnectionID + " no longer exists", turn - 1
		}
		connection, windowSource, windowErr = resolveContextWindow(ctx, connection)
		if windowErr != nil {
			return "connection_not_runnable", windowErr.Error(), turn - 1
		}
		if value, found := r.messageLimits.Load(connection.ID); found {
			connection.Capabilities.ObservedMessageLimit = value.(int)
		}
		// An invalid durable tool call is a history-shape problem, not an
		// accounting endpoint failure. Repair it before any template or tokenizer
		// request so the one accounting degrade boundary never has to leak an
		// endpoint error merely to trigger history repair.
		if !accountingRepairTried && r.repairMalformedToolCall(ctx, s, runID, connection, currentReasoning) {
			accountingRepairTried = true
			turn--
			continue
		}
		client := llm.New(connection)
		state := s.Snapshot().Run
		state.Turn = turn
		s.SetRun(state)
		enabled := s.EnabledTools()
		schemas := r.tools.Schemas(enabled)
		toolNames := r.tools.Names(enabled)
		var request llm.Request
		var body map[string]any
		var requestEvent events.Event
		system := ""
		var budget events.Budget
		var budgetErr error
		budgetBusy := false
		r.stage(s, runID, turn, "assemble", func() {
			records := s.MessagesCopy()
			systemBase := r.prompt.RenderParts(connection, s, toolNames, "", "")
			systemProject := r.prompt.RenderParts(connection, s, toolNames, s.ProjectBlock, "")
			systemWorkspaceMemory := r.prompt.RenderMemoryParts(connection, s, toolNames, s.ProjectBlock, s.MemoryBlock, "")
			system = r.prompt.RenderMemoryParts(connection, s, toolNames, s.ProjectBlock, s.MemoryBlock, s.AgentMemoryBlock)
			messages := []llm.Message{}
			requestRecords := make([]events.Message, 0, len(records))
			current := runningTurnIDs(records, s.RunPin())
			for _, message := range records {
				if isHarnessAbortRecord(message) {
					messages = append(messages, requestMessageAt(connection, s, message, current[message.ID]))
					requestRecords = append(requestRecords, message)
				}
			}
			for _, message := range records {
				if isHarnessAbortRecord(message) {
					continue
				}
				converted := requestMessageAt(connection, s, message, current[message.ID])
				if preserveReasoning(connection) {
					converted.ReasoningContent = message.Reasoning
				}
				messages = append(messages, converted)
				requestRecords = append(requestRecords, message)
			}
			// Item 2fd rule 1: the list is checked before it is measured or sent.
			conversation, repairedRecords, _ := normalizeAdjacentAssistants(messages, requestRecords)
			hadUser := false
			for _, message := range conversation {
				if message.Role == llm.RoleUser {
					hadUser = true
					break
				}
			}
			messages, budgetErr = llm.BuildMessageList(system, conversation)
			requestRecords = repairedRecords
			if budgetErr != nil {
				return
			}
			if !hadUser {
				requestRecords = append(requestRecords, events.Message{Role: llm.RoleHarness, Category: "history", Content: "Continue from the recorded context."})
			}
			if retryInstruction != "" {
				messages = append(messages, llm.Message{Role: llm.RoleAssistant, Content: "[harness note]\n" + retryInstruction})
				requestRecords = append(requestRecords, events.Message{Role: llm.RoleHarness, Category: "history", Content: retryInstruction})
			}
			request = llm.Request{Messages: messages, Tools: schemas, ToolChoice: "auto", Thinking: connection.Reasoning.Enabled}
			if readCutShort {
				request.ToolChoice = "none"
			}
			budget, budgetErr = r.budget.MeasureWithBusy(ctx, connection, s, r.cfg().Context, budgetInput{SystemBase: systemBase, SystemProject: systemProject, SystemWorkspaceMemory: systemWorkspaceMemory, System: system, WithoutToolSystems: r.withoutToolSystems(connection, s, enabled, s.MemoryBlock), Schemas: schemas, AllSchemas: r.tools.AllSchemas(), Messages: messages[1:], Records: requestRecords}, false, func(err error) {
				budgetBusy = true
				r.bus.Publish(events.New(events.ModelBusy, s.ID, runID, map[string]any{"host": modelHost(connection), "detail": err.Error()}))
			})
			if budgetErr != nil {
				return
			}
			guardUsed := guardedPromptTokens(budget)
			request.MaxTokens = requestTokenLimit(connection, budget, guardUsed)
			if request.Thinking && connection.Reasoning.MaxTokens > 0 && !containsFinding(connection, "server reasoning budget: accepted") {
				request.MaxTokens = min(request.MaxTokens, connection.Reasoning.MaxTokens)
			}
			diagnosticRequest := request
			diagnosticRequest.Messages = diagnosticMessages(request.Messages)
			body = llm.BuildRequest(connection, diagnosticRequest, true)
			r.bus.Publish(events.New(events.BudgetEvent, s.ID, runID, budget))
			data := map[string]any{"turn": turn, "message_count": len(messages), "tool_count": len(schemas), "params": requestParams(connection, request.MaxTokens), "est_prompt_tokens": budget.UsedEst, "estimated": budget.Estimated}
			requestEvent = events.New(events.ModelRequest, s.ID, runID, data)
			requestEvent.Body = body
		})
		if budgetBusy && budgetErr == nil {
			r.bus.Publish(events.New(events.ModelReachable, s.ID, runID, map[string]any{"connection_id": connection.ID}))
		}
		if budgetErr != nil {
			if ctx.Err() != nil {
				return contextStop(turn-1, "budget accounting canceled")
			}
			r.operationalError(s, runID, "budget", budgetErr)
			if !accountingRepairTried && r.repairMalformedToolCall(ctx, s, runID, connection, currentReasoning) {
				accountingRepairTried = true
				turn--
				continue
			}
			// Item 2fd rule 2: a refused template is repaired and tried once more;
			// only a second refusal stops the run, with its reason. The list is
			// normalised on every assembly, so the retry reassembles it.
			if !templateRetryTried && templateRefused(budgetErr) {
				templateRetryTried = true
				turn--
				continue
			}
			if r.publishModelUnreachable(s, runID, connection, budgetErr) {
				publishFinalBudget = false
				return "model_unreachable", budgetErr.Error(), turn - 1
			}
			return "model_error", "budget accounting: " + budgetErr.Error(), turn - 1
		}
		if limit := connection.Capabilities.ObservedMessageLimit; limit > 0 && len(request.Messages) >= limit {
			if r.compactForMessageLimit(ctx, s, runID, connection, limit) {
				turn--
				continue
			}
		}
		// Item 2ey: a run's first request gets the projection a later turn gets at
		// its end, so a restored chat over the soft line compacts before it is
		// sent instead of after it overflows.
		if !softLineChecked {
			softLineChecked = true
			if budget.Ceiling > 0 && budget.UsedEst >= int(float64(budget.Ceiling)*r.cfg().Context.SoftPct) && r.compactAfterTurn(ctx, s, runID, turn-1, connection, currentReasoning) {
				turn--
				continue
			}
		}
		guardUsed := guardedPromptTokens(budget)
		floor := outputFloor(connection)
		if guardUsed+floor > budget.NCtx {
			changed, exhausted := r.compactToFit(ctx, s, runID, connection, currentReasoning, budget)
			if changed {
				turn--
				continue
			}
			if exhausted {
				// Everything outside the running turn is already compacted. The
				// only thing left to cut is the task itself, and answering some
				// older message instead is what 2eg was filed for.
				return "context_exhausted", fmt.Sprintf("prompt count %d (%s) + output floor %d exceeds context window %d (%s) after compaction; nothing outside it is left to compact%s", guardUsed, budgetCountSource(budget), floor, budget.NCtx, windowSource, keptReadsSentence(s)), turn - 1
			}
			return "context_ceiling", fmt.Sprintf("prompt count %d (%s) leaves less than the %d-token output floor in context window %d (%s) after compaction", guardUsed, budgetCountSource(budget), floor, budget.NCtx, windowSource), turn - 1
		}
		r.bus.Publish(requestEvent)
		r.budget.MarkRequest(s.ID, budget.UsedEst)
		var response llm.Response
		var callErr error
		partial := ""
		toolArgumentBytes := map[int]int{}
		toolArgumentRunes := map[int]int{}
		var streamBusy atomic.Bool
		requestDone := make(chan struct{})
		r.stage(s, runID, turn, "call_model", func() {
			response, callErr = client.ChatStreamStatus(ctx, request, func(delta llm.Delta) {
				r.addFlightDelta(s.ID, runID, delta)
				if delta.Kind == "progress" {
					r.bus.Publish(events.New(events.ModelProgress, s.ID, runID, map[string]any{"turn": turn, "total": delta.Total, "cache": delta.Cache, "processed": delta.Processed}))
					return
				}
				if delta.Kind == "content" {
					partial += delta.Text
					s.UpdatePartial(partial)
				}
				data := map[string]any{"turn": turn, "kind": delta.Kind, "index": delta.Index, "text": durableModelDeltaText(delta.Kind, delta.Text)}
				if delta.Kind == "tool_call" {
					toolArgumentBytes[delta.Index] += len([]byte(delta.Text))
					toolArgumentRunes[delta.Index] += len([]rune(delta.Text))
					data["call_id"], data["name"] = delta.CallID, delta.Name
					data["argument_bytes"] = toolArgumentBytes[delta.Index]
					data["argument_tokens"] = int(math.Ceil(float64(toolArgumentRunes[delta.Index]) / 3.6))
				}
				r.bus.Publish(events.New(events.ModelDelta, s.ID, runID, data))
			}, func() {
				go func() {
					timer := time.NewTimer(2500 * time.Millisecond)
					defer timer.Stop()
					select {
					case <-timer.C:
						streamBusy.Store(true)
						r.bus.Publish(events.New(events.ModelBusy, s.ID, runID, map[string]any{"host": modelHost(connection), "detail": "connected; waiting for model response"}))
					case <-requestDone:
					}
				}()
			})
			close(requestDone)
			s.UpdatePartial("")
		})
		if streamBusy.Load() && callErr == nil {
			r.bus.Publish(events.New(events.ModelReachable, s.ID, runID, map[string]any{"connection_id": connection.ID}))
		}
		if callErr != nil {
			if ctx.Err() != nil {
				return contextStop(turn, "model call canceled")
			}
			if limit, sentence, matched := messageLimitError(callErr); matched {
				r.messageLimits.Store(connection.ID, limit)
				connection.Capabilities.ObservedMessageLimit = limit
				if r.recordMessageLimit != nil {
					if err := r.recordMessageLimit(connection.ID, limit); err != nil {
						r.operationalError(s, runID, "record_message_limit", err)
					}
				}
				if !messageLimitRetried {
					messageLimitRetried = true
					if r.compactForMessageLimit(ctx, s, runID, connection, limit) {
						turn--
						continue
					}
				}
				return "model_error", sentence, turn
			}
			if r.publishModelUnreachable(s, runID, connection, callErr) {
				publishFinalBudget = false
				return "model_unreachable", callErr.Error(), turn
			}
			return "model_error", callErr.Error(), turn
		}
		r.budget.RecordUsage(s.ID, response.Usage.PromptTokens, response.Usage.CachedTokens)
		toolCalls := make([]events.ToolCall, 0, len(response.ToolCalls))
		for _, call := range response.ToolCalls {
			toolCalls = append(toolCalls, events.ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
		}
		rawToolCalls := append([]events.ToolCall(nil), toolCalls...)
		s.RecordModelTurn()
		if retry, stop := malformedToolTurnAction(response.FinishReason, len(toolCalls), malformedToolRetried); retry {
			malformedToolRetried = true
			retryInstruction = "The previous response ended as tool_calls but contained no calls. Return one complete offered tool call or a final answer."
			r.bus.Publish(events.New(events.ModelRetry, s.ID, runID, map[string]any{"turn": turn, "next_turn": turn + 1, "reason": "malformed_tool_turn", "attempt": 1, "max_attempts": 1}))
			continue
		} else if stop {
			r.appendHarnessLine(ctx, connection, s, runID, turn, "malformed tool turn: finish_reason tool_calls contained no calls twice")
			return "malformed_turn", "finish_reason tool_calls contained no calls after one retry", turn
		}
		var guardLines []string
		toolCalls, guardLines = guardModelToolCalls(toolCalls, enabled)
		for _, line := range guardLines {
			r.appendHarnessLine(ctx, connection, s, runID, turn, line)
		}
		if len(response.ToolCalls) > 0 && len(toolCalls) == 0 {
			retryInstruction = "The previous response named no offered tool. Use only an offered tool or return a final answer."
			continue
		}
		reasoningTokens, reasoningTokensEstimated := r.count(ctx, connection, response.Reasoning)
		durableToolCalls := sanitizedToolCalls(toolCalls)
		responseData := map[string]any{"turn": turn, "finish_reason": response.FinishReason, "content": response.Content, "reasoning_tokens": reasoningTokens, "reasoning_tokens_estimated": reasoningTokensEstimated, "tool_calls": durableToolCalls, "usage": map[string]any{"prompt_tokens": response.Usage.PromptTokens, "completion_tokens": response.Usage.CompletionTokens, "cached_tokens": nullable(response.Usage.CachedTokens)}, "timings": response.Timings, "duration_ms": response.DurationMS}
		responseEvent := events.New(events.ModelResponse, s.ID, runID, responseData)
		responseEvent.Raw = redactToolCallHeaders(string(response.Raw), rawToolCalls)
		r.bus.Publish(responseEvent)
		if s.Role == "e" && s.ParentSessionID != "" {
			r.bus.Publish(events.New(events.DelegatedUsage, s.ParentSessionID, runID, map[string]any{"usage": responseData["usage"], "tool_calls": durableToolCalls}))
		}
		r.maybeAuxProgress(ctx, s, runID, turn)
		r.stage(s, runID, turn, "parse", func() {})
		if response.FinishReason == "length" && len(toolCalls) > 0 {
			if lengthSeen {
				return "length", fmt.Sprintf("truncated %s call hit the output limit twice", firstToolName(durableToolCalls)), turn
			}
			if firstToolName(durableToolCalls) == "" {
				return "length", "truncated tool call had no function name and could not be retried safely", turn
			}
			lengthSeen = true
			truncatedToolRetry = firstToolName(durableToolCalls)
			retryInstruction = "The previous " + truncatedToolRetry + " call was truncated. Return that complete offered tool call again, with valid JSON arguments."
			r.bus.Publish(events.New(events.ModelRetry, s.ID, runID, map[string]any{"turn": turn, "next_turn": turn + 1, "reason": "truncated_tool_call", "tool": truncatedToolRetry, "attempt": 1, "max_attempts": 1}))
			continue
		}
		if truncatedToolRetry != "" {
			if len(toolCalls) == 0 || firstToolName(durableToolCalls) != truncatedToolRetry {
				return "length", fmt.Sprintf("truncated %s call retry did not return that tool call", truncatedToolRetry), turn
			}
			truncatedToolRetry = ""
			retryInstruction = ""
			lengthSeen = false
		}
		if len(toolCalls) == 0 && response.FinishReason != "tool_calls" {
			if response.FinishReason == "length" {
				return "length", "model output was truncated", turn
			}
			finalContent := response.Content
			stopReason, stopDetail := "done", ""
			if strings.TrimSpace(finalContent) == "" {
				if reasoning := strings.TrimSpace(response.Reasoning); reasoning != "" {
					finalContent = reasoning + "\n\n[answer taken from the model's reasoning]"
					stopReason, stopDetail = "reply_empty_reasoning_shown", "reply empty — reasoning shown"
				} else {
					finalContent = "[model returned an empty reply]"
					stopReason, stopDetail = "reply_empty", "reply empty — no reasoning available"
				}
			} else if response.FinishReason == "stop" && announcedActionOnly(finalContent) {
				stopReason, stopDetail = "announced_action_and_stopped", "announced an action and stopped"
			}
			r.stage(s, runID, turn, "append", func() {
				visible, proposals := planProposalsFor(s, finalContent)
				message, _ := r.makeMessage(ctx, connection, "assistant", visible, "history", turn)
				message.Reasoning = response.Reasoning
				message.PlanProposals = bindPlanProposalSources(s.MessagesCopy(), proposals, message.ID)
				currentReasoning[message.ID] = true
				s.Append(message)
				r.bus.Publish(events.New(events.MessageAppended, s.ID, runID, map[string]any{"message": message}))
			})
			// Item 2fa: a final message that is exactly the registration sentence
			// raises the operator's card; the line stays in the transcript above it.
			finalText, _ := parsePlanProposals(finalContent)
			if stopReason != "done" {
				return stopReason, stopDetail, turn
			}
			if handled, detail := r.handleModelPlanProposal(ctx, s, runID, finalText); handled {
				return "done", detail, turn
			}
			return "done", "", turn
		}
		visible, proposals := planProposalsFor(s, response.Content)
		assistant, _ := r.makeMessage(ctx, connection, "assistant", visible, "history", turn)
		assistant.Reasoning = response.Reasoning
		assistant.PlanProposals = bindPlanProposalSources(s.MessagesCopy(), proposals, assistant.ID)
		currentReasoning[assistant.ID] = true
		assistant.ToolCalls = durableToolCalls
		type result struct {
			call            events.ToolCall
			durableCall     events.ToolCall
			args            map[string]any
			argErr          error
			content         string
			ok              bool
			operatorContext bool
			category        string
			untrusted       bool
			metadata        map[string]any
			ms              int64
		}
		results := []result{}
		r.stage(s, runID, turn, "dispatch", func() {
			for index, call := range toolCalls {
				var args map[string]any
				decoded, err := tools.DecodeArgs(call.Arguments)
				if err == nil {
					args = decoded
				} else {
					args = map[string]any{}
				}
				results = append(results, result{call: call, durableCall: durableToolCalls[index], args: args, argErr: err})
			}
		})
		if toolCallsUsed+len(results) > runCfg.MaxToolCalls {
			return "tool_budget", fmt.Sprintf("guessed tool-call backstop of %d would be exceeded", runCfg.MaxToolCalls), turn
		}
		toolCallsUsed += len(results)
		r.stage(s, runID, turn, "execute", func() {
			// Item 2et: -1 means the window is unknown and nothing is clamped;
			// 0 means no room is left, which clamps every later window read.
			remainingResultTokens := -1
			if budget.NCtx > 0 {
				remainingResultTokens = max(0, budget.NCtx-budget.Reserve-budget.UsedEst-toolResultContextMargin)
			}
			for index := range results {
				item := &results[index]
				r.bus.Publish(events.New(events.ToolCallEvent, s.ID, runID, map[string]any{"turn": turn, "call_id": item.call.ID, "name": item.call.Name, "args": sanitizedToolArguments(item.call.Name, item.args)}))
				start := time.Now()
				if item.argErr != nil {
					item.content = "error: " + item.argErr.Error()
					item.ok = false
				} else {
					r.setFlightTool(s.ID, runID, turn, item.durableCall, item.args)
					outcome := r.executeTool(ctx, s, runID, item.call.ID, item.call.Name, item.args)
					item.content, item.ok, item.operatorContext = outcome.Content, outcome.OK, outcome.OperatorContext
					r.setFlightToolOutput(s.ID, runID, item.content)
					item.category, item.untrusted, item.metadata = outcome.Category, outcome.Untrusted, outcome.Metadata
					if item.ok && item.call.Name == "read_file" && untrustedAttachmentRead(s, item.args) {
						item.untrusted = true
					}
					if item.ok {
						fileMetadata := producedFileMetadata(s, item.call.Name, item.args)
						item.metadata = mergeResultMetadata(item.metadata, fileMetadata)
						if file, ok := fileMetadata["file"].(map[string]any); ok {
							path, _ := file["path"].(string)
							bytes, _ := file["bytes"].(int64)
							produced[strings.ToLower(path)] = delivery.Source{Path: path, Bytes: bytes}
						}
					}
				}
				if ctx.Err() != nil {
					break
				}
				item.ms = time.Since(start).Milliseconds()
				resultTokens := r.textTokens(ctx, connection, item.content)
				item.content, item.ok, item.metadata, resultTokens = r.fitWindowResult(
					ctx, s, connection, item.call.Name, item.args, item.content, item.ok, item.metadata, resultTokens, remainingResultTokens, item.operatorContext,
				)
				if _, batch := item.args["windows"]; item.call.Name == "read_file" && !batch {
					if tooLarge, _ := item.metadata["result_too_large"].(bool); tooLarge {
						if refusedTurn >= 0 && refusedTurn == turn-1 {
							item.content, item.ok, item.metadata = readCutShortResult(item.args, item.metadata)
							resultTokens = r.textTokens(ctx, connection, item.content)
							readCutShort = true
						} else if refusedTurn != turn {
							refusedTurn = turn
						}
					} else if item.ok {
						refusedTurn = -1
					}
				}
				if remainingResultTokens >= 0 {
					remainingResultTokens = max(0, remainingResultTokens-resultTokens)
				}
				data := toolResultEventData(turn, item.call.ID, item.call.Name, item.content, item.ok, item.operatorContext, item.untrusted, item.ms, resultTokens, item.metadata)
				s.IncrementToolCall(item.call.Name)
				r.bus.Publish(events.New(events.ToolResult, s.ID, runID, data))
			}
		})
		if ctx.Err() != nil {
			return contextStop(turn, "tool execution canceled")
		}
		r.stage(s, runID, turn, "append", func() {
			s.Append(assistant)
			r.bus.Publish(events.New(events.MessageAppended, s.ID, runID, map[string]any{"message": assistant}))
			for _, item := range results {
				category := item.category
				if category == "" {
					category = "results"
				}
				if item.call.Name == "read_file" && item.category == "" {
					category = "files"
				}
				message, _ := r.makeMessage(ctx, connection, "tool", item.content, category, turn)
				message.ToolCallID = item.call.ID
				message.Name = item.call.Name
				message.OK = boolPointer(item.ok)
				s.Append(message)
				r.bus.Publish(events.New(events.MessageAppended, s.ID, runID, map[string]any{"message": message}))
				if item.ok && item.call.Name == "remember" {
					r.appendHarnessLine(ctx, connection, s, runID, turn, "Memory was saved for new chats; this chat's system prompt remains unchanged.")
				}
			}
		})
		for _, item := range results {
			eventArgs := sanitizedToolArguments(item.call.Name, item.args)
			reason, detail, prior := guards.Observe(item.call.ID, item.call.Name, eventArgs, item.content, item.ok)
			if reason == "cycle" {
				r.bus.Publish(events.New(events.CycleDetected, s.ID, runID, map[string]any{"call_id": item.call.ID, "name": item.call.Name, "args": eventArgs, "prior_call_id": prior}))
				decision, err := r.gate.WaitCycleDecision(ctx, s, runID, item.call.ID+"-cycle", map[string]any{"tool": item.call.Name, "detail": detail})
				if err != nil {
					if ctx.Err() != nil {
						return contextStop(turn, "approval wait canceled")
					}
					return "cycle", err.Error(), turn
				}
				if decision == "continue" {
					guards.ResetCycle()
					continue
				}
				return "cycle", detail, turn
			}
			if reason != "" {
				return reason, detail, turn
			}
		}
		maxTurns := r.cfg().Run.MaxTurns
		if configured := s.Snapshot().Run.MaxTurns; configured > 0 {
			maxTurns = configured
		}
		if turn >= maxTurns {
			return "turn_ceiling", "maximum turns reached", turn
		}
		r.stage(s, runID, turn, "compact", func() {
			// Item 2fv: after a refused window the elide runs even on a cold
			// prefill, so a second refusal means nothing elidable was left.
			r.compactAfterTurnForcing(ctx, s, runID, turn, connection, currentReasoning, refusedTurn == turn)
		})
	}
}

var messageLimitPattern = regexp.MustCompile(`(?i)(conversation too long:\s*\d+ messages\s*\(limit\s*(\d+)\)?|[^\r\n\"]*message[^\r\n\"]*limit[^\r\n\"]*)`)

func messageLimitError(err error) (int, string, bool) {
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		return 0, "", false
	}
	match := messageLimitPattern.FindStringSubmatch(err.Error())
	if len(match) == 0 {
		return 0, "", false
	}
	limit := 0
	if len(match) > 2 {
		fmt.Sscanf(match[2], "%d", &limit)
	}
	if limit <= 1 {
		return 0, "", false
	}
	return limit, strings.TrimSpace(match[1]), true
}

func (r *Runner) compactForMessageLimit(ctx context.Context, s *session.Session, runID string, connection *config.Connection, limit int) bool {
	changed := false
	// One summary replaces an arbitrarily large old span with one message. A
	// second pass is useful for restored chats containing nested summaries.
	for attempts := 0; attempts < 2 && len(s.MessagesCopy())+1 >= limit; attempts++ {
		if !r.summarize(withCompactionTrigger(ctx, "message_limit"), s, runID, connection) {
			break
		}
		changed = true
	}
	return changed
}

type workspaceFileState struct {
	Path    string
	Size    int64
	ModTime int64
}

func workspaceFileSnapshot(root string) map[string]workspaceFileState {
	out := map[string]workspaceFileState{}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err == nil {
			clean := filepath.Clean(rel)
			out[strings.ToLower(clean)] = workspaceFileState{Path: filepath.ToSlash(clean), Size: info.Size(), ModTime: info.ModTime().UnixNano()}
		}
		return nil
	})
	return out
}

func workspaceFileChanges(root string, before map[string]workspaceFileState) map[string]delivery.Source {
	after := workspaceFileSnapshot(root)
	out := map[string]delivery.Source{}
	for key, state := range after {
		if prior, existed := before[key]; existed && prior == state {
			continue
		}
		out[key] = delivery.Source{Path: state.Path, Bytes: state.Size}
	}
	return out
}

func announcedActionOnly(content string) bool {
	text := strings.TrimSpace(content)
	if text == "" || len([]rune(text)) > 400 || strings.ContainsAny(text, "\r\n") {
		return false
	}
	lower := strings.ToLower(text)
	for _, prefix := range []string{"i'll ", "i’ll ", "i will ", "i am going to ", "now i'll ", "now i’ll ", "now i will ", "now i am going to ", "let me "} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func turnCeilingDetail(turns int, result delivery.Result) string {
	delivered := "no files"
	if len(result.Items) > 0 {
		items := make([]string, 0, len(result.Items))
		for _, item := range result.Items {
			value := item.SourcePath + " " + item.Status
			if item.DeliveredPath != "" {
				value += " to " + item.DeliveredPath
			}
			items = append(items, value)
		}
		delivered = strings.Join(items, ", ")
	}
	return fmt.Sprintf("completed %d turns; delivery: %s; continue: send Continue in this chat; the session history and delivered work are retained", turns, delivered)
}

func (r *Runner) applyMailboxBoundary(ctx context.Context, s *session.Session, runID string, approvalPending bool) (bool, string) {
	if r.mailboxBoundary == nil || s.Role == "e" {
		return false, ""
	}
	action := r.mailboxBoundary(ctx, s.ID, approvalPending)
	if action.Err != nil {
		r.bus.Publish(events.New(events.Error, s.ID, runID, map[string]any{"where": "mailbox", "message": action.Err.Error()}))
	}
	if action.Stop {
		return true, "STOP read from INBOX.md"
	}
	if action.Revision != "" {
		if _, err := r.AddUser(ctx, s, action.Revision); err != nil {
			r.bus.Publish(events.New(events.Error, s.ID, runID, map[string]any{"where": "mailbox", "message": err.Error()}))
		}
	}
	if action.Delay > 0 {
		timer := time.NewTimer(action.Delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return true, ctx.Err().Error()
		case <-timer.C:
		}
	}
	return false, ""
}

func producedFileMetadata(s *session.Session, name string, args map[string]any) map[string]any {
	if name != "write_file" && name != "edit_file" {
		return nil
	}
	requested, _ := args["path"].(string)
	if requested == "" {
		return nil
	}
	root, err := s.WriteRoot(requested)
	if err != nil {
		return nil
	}
	resolved, err := tools.Resolve(root, requested)
	if err != nil {
		return nil
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil
	}
	return map[string]any{"file": map[string]any{
		"path":  filepath.ToSlash(relative),
		"bytes": info.Size(),
	}}
}

func mergeResultMetadata(current, added map[string]any) map[string]any {
	if len(added) == 0 {
		return current
	}
	if current == nil {
		current = map[string]any{}
	}
	for key, value := range added {
		current[key] = value
	}
	return current
}

func toolResultEventData(turn int, callID, name, content string, ok, operatorContext, untrusted bool, ms int64, tokens int, metadata map[string]any) map[string]any {
	data := map[string]any{
		"turn": turn, "call_id": callID, "name": name, "ok": ok,
		"operator_context": operatorContext, "untrusted": untrusted, "ms": ms,
		"bytes": len(content), "tokens": tokens, "preview": preview(content),
	}
	for key, value := range metadata {
		data[key] = value
	}
	return data
}

func (r *Runner) executeTool(ctx context.Context, s *session.Session, runID, callID, name string, args map[string]any) tools.CallOutcome {
	cfg := r.cfg()
	eventArgs := sanitizedToolArguments(name, args)
	if cfg.Shell.ServiceAccount.Enabled && !cfg.Shell.OperatorContext {
		if err := r.tools.PreflightServiceIdentity(); err != nil {
			if s.Role == "e" {
				return r.tools.CallDetailed(ctx, s, name, args)
			}
			switch name {
			case "read_file", "list_dir", "search", "search_text", "find_files":
				return r.callFileAsOperator(ctx, s, name, args)
			case "write_file", "edit_file", "shell", "run_script", "call_service":
				if s.Scratch && (name == "write_file" || name == "edit_file") {
					break
				}
				return tools.CallOutcome{Content: "error: service identity not set up"}
			}
		}
	}
	if cfg.Shell.ServiceAccount.Enabled && !cfg.Shell.OperatorContext && r.hasIdentityChatGrant(s.ID) {
		if fileGrantTool(name) {
			return r.callFileAsOperator(ctx, s, name, args)
		}
		if name == "shell" || name == "run_script" {
			return r.callShellAsOperator(ctx, s, name, args)
		}
	}
	if fileGrantTool(name) && cfg.Shell.ServiceAccount.Enabled && !cfg.Shell.OperatorContext && r.hasFileGrant(s.ID, runID) {
		return r.callFileAsOperator(ctx, s, name, args)
	}
	if name == "shell" && cfg.Shell.ServiceAccount.Enabled && !cfg.Shell.OperatorContext {
		if r.hasShellGrant(s.ID, runID, shellGrantBoundary, "") {
			return r.callShellAsOperator(ctx, s, name, args)
		}
		if command, matched := r.tools.OperatorCommandWith(name, args, s.Policy().Shell.OperatorCommandsAdd); matched {
			return r.executeOperatorCommand(ctx, s, runID, callID, name, args, command)
		}
	}
	if name == "shell" && repoRunGrantMatches(s.Policy().Shell.RunGrantDefaults, args) && !r.hasShellGrant(s.ID, runID, shellGrantPolicy, "") {
		identity := "operator"
		if cfg.Shell.ServiceAccount.Enabled {
			identity = "service"
		}
		r.grantShellRun(s, runID, shellRunGrant{Rule: shellGrantPolicy, Identity: identity})
	}
	decision := "approve"
	var gateErr error
	if name == "run_script" && cfg.Shell.ServiceAccount.Enabled && !r.hasPolicyChatGrant(s.ID, name) {
		decision, gateErr = r.gate.WaitPolicyDecision(ctx, s, runID, callID, name, eventArgs)
	} else if name == "shell" && r.hasShellGrant(s.ID, runID, shellGrantPolicy, "") {
		// An operator-approved repository default grants this displayed command
		// pattern for the run under the already-configured identity.
	} else if name == "shell" && cfg.Shell.ServiceAccount.Enabled && !cfg.Shell.OperatorContext && r.gate.requiredFor(s, name) {
		if !r.hasShellGrant(s.ID, runID, shellGrantPolicy, "") {
			decision, gateErr = r.gate.WaitPolicyDecision(ctx, s, runID, callID, name, eventArgs)
		}
	} else if r.hasPolicyChatGrant(s.ID, name) {
		// The operator already allowed this policy-governed action for the chat.
	} else {
		decision, gateErr = r.gate.WaitDecision(ctx, s, runID, callID, name, eventArgs)
	}
	if gateErr != nil {
		return tools.CallOutcome{Content: "error: call canceled"}
	}
	if !approvalGranted(decision) {
		return tools.CallOutcome{Content: "error: call denied by user"}
	}
	if name == "shell" && decision == "run" {
		r.grantShellRun(s, runID, shellRunGrant{Rule: shellGrantPolicy, Identity: "service"})
	}
	if name == "shell" && decision == "session" {
		r.grantShellSession(s, runID, shellRunGrant{Rule: shellGrantPolicy, Identity: "service"})
	}
	if decision == "session" {
		r.grantPolicyChat(s.ID, name)
	}
	if name == "shell" && repoRunGrantMatches(s.Policy().Shell.RunGrantDefaults, args) {
		args = resolveRepoGrantedExecutable(cfg, args)
	}
	outcome := r.callDetailed(ctx, s, name, args)
	if !outcome.OperatorOverrideAvailable {
		return outcome
	}
	command, _ := args["command"].(string)
	path, _ := args["path"].(string)
	subject := "tool call"
	scope := "rerun this exact tool call once"
	if name == "shell" || name == "run_script" {
		subject = "command"
		scope = "rerun this exact command once"
	}
	overrideID := callID + ":operator"
	overrideArgs := map[string]any{
		"identity": "Agent_b operator (not Administrator)",
		"reason":   outcome.OperatorOverrideReason,
		"scope":    scope,
	}
	if command != "" {
		overrideArgs["command"] = command
	}
	if path != "" {
		overrideArgs["path"] = path
	}
	// v0.69.0/W12 cold review: run_script has no command or path argument, so
	// its card showed nothing of what would run as the operator.
	if source, _ := args["source"].(string); name == "run_script" && source != "" {
		overrideArgs["source"] = source
		if language, _ := args["language"].(string); language != "" {
			overrideArgs["language"] = language
		}
	}
	// With no service identity, the outside-folder card (item 2fi): its "Yes,
	// for this chat" holds for this chat's outside-folder calls in this posture.
	// The identity grants below apply only with the service identity on, so
	// storing them here left a grant that woke up if the posture changed. The
	// sandbox card keeps its own handling.
	outsideCard := !cfg.Shell.ServiceAccount.Enabled && strings.Contains(outcome.OperatorOverrideReason, "outside the folder")
	overrideDecision := "deny"
	var overrideErr error
	if outsideCard && r.hasPolicyChatGrant(s.ID, outsideFolderChatGrant) {
		overrideDecision = "once"
	} else {
		overrideDecision, overrideErr = r.gate.WaitBoundaryDecision(ctx, s, runID, overrideID, name+".operator_override", overrideArgs)
	}
	if overrideErr != nil {
		outcome.OK, outcome.OperatorContext = false, false
		outcome.Metadata = withHarnessNote(outcome.Metadata, "operator-identity override canceled")
		outcome.Content = withModelNote(outcome.Content, "operator-identity override canceled")
		return outcome
	}
	if !approvalGranted(overrideDecision) {
		log.Printf("%s operator-identity override denied: session=%s call=%s command=%q path=%q", name, s.ID, callID, command, path)
		outcome.OK, outcome.OperatorContext = false, false
		outcome.Metadata = withHarnessNote(outcome.Metadata, "operator-identity override was offered and denied by the user")
		outcome.Content = withModelNote(outcome.Content, "operator-identity override was offered and denied by the user")
		return outcome
	}
	if outsideCard {
		if overrideDecision == "session" {
			r.grantPolicyChat(s.ID, outsideFolderChatGrant)
		}
		overrideDecision = "once"
	}
	if name == "shell" && overrideDecision == "run" {
		r.grantShellRun(s, runID, shellRunGrant{Rule: shellGrantBoundary, Identity: "operator"})
	}
	if name == "shell" && overrideDecision == "session" {
		r.grantShellSession(s, runID, shellRunGrant{Rule: shellGrantBoundary, Identity: "operator"})
	}
	if fileGrantTool(name) && overrideDecision == "run" {
		r.grantFileRun(s, runID)
	}
	if fileGrantTool(name) && overrideDecision == "session" {
		r.grantFileSession(s, runID)
	}
	if overrideDecision == "session" {
		r.grantIdentityChat(s.ID, runID)
	}
	log.Printf("SECURITY: %s operator-identity override approved: session=%s call=%s command=%q path=%q", name, s.ID, callID, command, path)
	overrideContent, overrideOK := r.tools.CallAsOperator(ctx, s, name, args)
	if overrideOK {
		if strings.TrimSpace(overrideContent) == "" {
			overrideContent = "the tool completed with no output"
		}
		outcome.Content, outcome.OK, outcome.OperatorContext = overrideContent, true, true
		outcome.Metadata = withHarnessNote(mergeResultMetadata(outcome.Metadata, sandboxResultMetadata(cfg, s, name, args)), "operator-identity override succeeded; exact "+subject+" rerun once")
		return outcome
	}
	outcome.Content, outcome.OK, outcome.OperatorContext = overrideContent, false, true
	outcome.Metadata = withHarnessNote(outcome.Metadata, "operator-identity override was attempted but failed")
	outcome.Content = withModelNote(outcome.Content, "operator-identity override was attempted but failed")
	return outcome
}

func malformedToolTurnAction(finish string, calls int, retried bool) (bool, bool) {
	if finish != "tool_calls" || calls != 0 {
		return false, false
	}
	return !retried, retried
}

func guardModelToolCalls(calls []events.ToolCall, offered map[string]bool) ([]events.ToolCall, []string) {
	kept, lines := make([]events.ToolCall, 0, len(calls)), []string{}
	seen := map[string]bool{}
	for _, call := range calls {
		if !offered[call.Name] {
			lines = append(lines, "dropped tool call "+call.Name+": name was not offered")
			continue
		}
		key := call.Name + "\x00" + call.Arguments
		if seen[key] {
			lines = append(lines, "collapsed duplicate "+call.Name+" tool call")
			continue
		}
		seen[key] = true
		kept = append(kept, call)
	}
	return kept, lines
}

// withModelNote tells the model what happened to a call that did not succeed
// (v0.65.0/W15 cold review): a denied, canceled or failed override is an error,
// not file content, and a model that is not told retries and prompts again. A
// successful rerun's content stays exactly what the tool returned.
func withModelNote(content, note string) string {
	return content + "\n\n[harness: " + note + "]"
}

// withHarnessNote records what the harness did around a tool call as a result
// field, rendered as its own harness line. Item 2eo: prepended to the content,
// the note was read by the model as the first line of the file it had asked for.
func withHarnessNote(metadata map[string]any, note string) map[string]any {
	next := cloneMetadata(metadata)
	if next == nil {
		next = map[string]any{}
	}
	next["harness_note"] = note
	return next
}

func sandboxResultMetadata(cfg config.Config, s *session.Session, name string, args map[string]any) map[string]any {
	if name != "shell" {
		language, _ := args["language"].(string)
		if name != "run_script" || !strings.EqualFold(strings.TrimSpace(language), "bash") {
			return nil
		}
	}
	if target, ok := cfg.SandboxTarget(s.ID, s.SandboxMounts()); ok {
		return map[string]any{"target": "sandbox " + target}
	}
	return nil
}

func repoRunGrantMatches(defaults []string, args map[string]any) bool {
	command, _ := args["command"].(string)
	normalized := strings.Join(strings.Fields(strings.ToLower(command)), " ")
	for _, value := range defaults {
		candidate := strings.Join(strings.Fields(strings.ToLower(value)), " ")
		if candidate != "" && (normalized == candidate || strings.HasPrefix(normalized, candidate+" ")) {
			return true
		}
	}
	return false
}

func resolveRepoGrantedExecutable(cfg config.Config, args map[string]any) map[string]any {
	if len(cfg.Shell.Command) == 0 {
		return args
	}
	shellName := strings.ToLower(filepath.Base(cfg.Shell.Command[0]))
	if shellName != "powershell" && shellName != "powershell.exe" && shellName != "pwsh" && shellName != "pwsh.exe" {
		return args
	}
	command, _ := args["command"].(string)
	fields := strings.Fields(command)
	if len(fields) == 0 || strings.ContainsAny(fields[0], `'"&|;$(){}[]`) {
		return args
	}
	resolved, err := exec.LookPath(fields[0])
	if err != nil {
		return args
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return args
	}
	rest := strings.TrimSpace(strings.TrimPrefix(command, fields[0]))
	quoted := "'" + strings.ReplaceAll(absolute, "'", "''") + "'"
	prepared := "& " + quoted
	if rest != "" {
		prepared += " " + rest
	}
	copy := make(map[string]any, len(args))
	for key, value := range args {
		copy[key] = value
	}
	copy["command"] = prepared
	return copy
}

func (r *Runner) callFileAsOperator(ctx context.Context, s *session.Session, name string, args map[string]any) tools.CallOutcome {
	if r.toolActivity != nil {
		r.toolActivity("started")
		defer r.toolActivity("completed")
	}
	content, ok := r.tools.CallAsOperator(ctx, s, name, args)
	if ok && strings.TrimSpace(content) == "" {
		content = "the tool completed with no output"
	}
	return tools.CallOutcome{Content: content, OK: ok, OperatorContext: true}
}

func (r *Runner) callDetailed(ctx context.Context, s *session.Session, name string, args map[string]any) (outcome tools.CallOutcome) {
	if r.toolActivity != nil {
		r.toolActivity("started")
		defer r.toolActivity("completed")
	}
	return r.tools.CallDetailed(ctx, s, name, args)
}

func (r *Runner) stage(s *session.Session, runID string, turn int, name string, fn func()) {
	start := time.Now()
	r.setFlightStage(s.ID, runID, turn, name)
	r.bus.Publish(events.New(events.Stage, s.ID, runID, map[string]any{"stage": name, "state": "enter", "turn": turn, "ms": 0}))
	fn()
	r.bus.Publish(events.New(events.Stage, s.ID, runID, map[string]any{"stage": name, "state": "exit", "turn": turn, "ms": time.Since(start).Milliseconds()}))
}
func (r *Runner) makeMessage(ctx context.Context, p *config.Connection, role, content, category string, turn int) (events.Message, error) {
	tokens, estimated := r.count(ctx, p, content)
	return events.Message{ID: r.id("m"), Role: role, Content: content, Category: category, Tokens: tokens, Estimated: estimated, Turn: turn}, nil
}

func preserveReasoning(connection *config.Connection) bool {
	return connection.Reasoning.Preserve || connection.IsQwen38()
}

func containsFinding(connection *config.Connection, finding string) bool {
	for _, value := range connection.Capabilities.Findings {
		if value == finding {
			return true
		}
	}
	return false
}

func (r *Runner) appendHarnessLine(ctx context.Context, connection *config.Connection, s *session.Session, runID string, turn int, content string) {
	message, _ := r.makeMessage(ctx, connection, llm.RoleHarness, content, "history", turn)
	s.Append(message)
	r.bus.Publish(events.New(events.MessageAppended, s.ID, runID, map[string]any{"message": message}))
}
func (r *Runner) count(ctx context.Context, p *config.Connection, text string) (int, bool) {
	if p.Capabilities.Tokenize {
		countCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
		client := llm.New(p)
		count, err := client.Tokenize(countCtx, text, false)
		if err == nil {
			return count, false
		}
	}
	return estimatedTokenCount(text), true
}

func estimatedTokenCount(text string) int {
	return int(math.Ceil(float64(len([]rune(text))) / 3.6))
}

func modelUnavailable(connection *config.Connection, err error) (string, bool) {
	// Item 2fg: a canceled request says nothing about the model.
	if err == nil || errors.Is(err, context.Canceled) {
		return "", false
	}
	if llm.TransportKindOf(err) != llm.TransportDial {
		return "", false
	}
	return modelHost(connection), true
}

func (r *Runner) publishModelUnreachable(s *session.Session, runID string, connection *config.Connection, err error) bool {
	host, unavailable := modelUnavailable(connection, err)
	if !unavailable {
		return false
	}
	r.bus.Publish(events.New(events.ModelUnreachable, s.ID, runID, map[string]any{"host": host, "detail": err.Error()}))
	if r.modelUnreachable != nil {
		r.modelUnreachable(s.ID, connection.ID)
	}
	return true
}

func modelHost(connection *config.Connection) string {
	host := strings.TrimSpace(connection.BaseURL)
	if endpoint, parseErr := url.Parse(host); parseErr == nil && endpoint.Host != "" {
		host = endpoint.Host
	}
	return host
}
func (r *Runner) textTokens(ctx context.Context, p *config.Connection, text string) int {
	value, _ := r.count(ctx, p, text)
	return value
}
func (r *Runner) PublishBudget(ctx context.Context, s *session.Session) {
	p, ok := r.connection(s.ConnectionID)
	if !ok {
		return
	}
	p, _, windowErr := resolveContextWindow(ctx, p)
	if windowErr != nil {
		r.operationalError(s, "", "budget", windowErr)
		return
	}
	budget, err := r.measureSession(ctx, p, s, nil, false)
	if err != nil {
		r.operationalError(s, "", "budget", err)
		r.publishModelUnreachable(s, "", p, err)
		return
	}
	r.bus.Publish(events.New(events.BudgetEvent, s.ID, "", budget))
}
func (r *Runner) measureSession(ctx context.Context, p *config.Connection, s *session.Session, currentReasoning map[string]bool, mark bool) (events.Budget, error) {
	enabled := s.EnabledTools()
	toolNames := r.tools.Names(enabled)
	schemas := r.tools.Schemas(enabled)
	base := r.prompt.RenderParts(p, s, toolNames, "", "")
	project := r.prompt.RenderParts(p, s, toolNames, s.ProjectBlock, "")
	workspaceMemory := r.prompt.RenderMemoryParts(p, s, toolNames, s.ProjectBlock, s.MemoryBlock, "")
	system := r.prompt.RenderMemoryParts(p, s, toolNames, s.ProjectBlock, s.MemoryBlock, s.AgentMemoryBlock)
	// As the run's own assembly does, harness abort records lead the list: a
	// system-role record mid-history is refused by chat templates (item 2fg).
	all := s.MessagesCopy()
	records := make([]events.Message, 0, len(all))
	for _, message := range all {
		if isHarnessAbortRecord(message) {
			records = append(records, message)
		}
	}
	for _, message := range all {
		if !isHarnessAbortRecord(message) {
			records = append(records, message)
		}
	}
	messages := make([]llm.Message, 0, len(records))
	current := runningTurnIDs(records, s.RunPin())
	for _, message := range records {
		converted := requestMessageAt(p, s, message, current[message.ID])
		if preserveReasoning(p) {
			converted.ReasoningContent = message.Reasoning
		}
		messages = append(messages, converted)
	}
	messages, records, _ = normalizeAdjacentAssistants(messages, records)
	return r.budget.Measure(ctx, p, s, r.cfg().Context, budgetInput{SystemBase: base, SystemProject: project, SystemWorkspaceMemory: workspaceMemory, System: system, WithoutToolSystems: r.withoutToolSystems(p, s, enabled, s.MemoryBlock), Schemas: schemas, AllSchemas: r.tools.AllSchemas(), Messages: messages, Records: records}, mark)
}

func (r *Runner) withoutToolSystems(p *config.Connection, s *session.Session, enabled map[string]bool, memory string) map[string]string {
	out := map[string]string{}
	for _, name := range r.tools.Names(enabled) {
		without := make(map[string]bool, len(enabled))
		for candidate, value := range enabled {
			without[candidate] = value
		}
		without[name] = false
		out[name] = r.prompt.Render(p, s, r.tools.Names(without), memory)
	}
	return out
}

// compactAfterTurn projects the next request at the end of a turn (item 2ey):
// at soft_pct it elides one batch toward 60%, at summary_pct it summarizes, so
// the next request starts under the line. It reports whether anything changed.
func (r *Runner) compactAfterTurn(ctx context.Context, s *session.Session, runID string, turn int, p *config.Connection, current map[string]bool) bool {
	return r.compactAfterTurnForcing(ctx, s, runID, turn, p, current, false)
}

// compactAfterTurnForcing is compactAfterTurn with the cold-prefill deferral of
// the batch elide overridden when force is set (item 2fv: a read window was
// just refused, so waiting a turn for a warm cache would only refuse again).
func (r *Runner) compactAfterTurnForcing(ctx context.Context, s *session.Session, runID string, turn int, p *config.Connection, current map[string]bool, force bool) bool {
	cfg := r.cfg()
	readDefaultLimit := min(cfg.Tools.ReadFile.DefaultLimit, cfg.Tools.ReadFile.MaxLimit)
	changed := r.compact.Supersede(s, runID, turn, readDefaultLimit, func(text string) (int, bool) { return r.count(ctx, p, text) })
	budget, err := r.measureSession(ctx, p, s, current, false)
	if err != nil {
		r.operationalError(s, runID, "compaction_budget", err)
		return changed
	}
	if shouldBatchElide(budget.UsedEst, budget.Ceiling, cfg.Context, r.budget.ColdPrefill(s.ID) && !force) {
		did, _ := r.compact.ElideOldWindow(s, runID, "soft_pct", budget.UsedEst, int(float64(budget.Ceiling)*.60), contextWindow(budget), readDefaultLimit, func(text string) (int, bool) { return r.count(ctx, p, text) })
		changed = changed || did
		if did {
			budget, err = r.measureSession(ctx, p, s, current, false)
			if err != nil {
				r.operationalError(s, runID, "compaction_budget", err)
				return changed
			}
		}
	}
	if budget.Ceiling > 0 && budget.UsedEst >= int(float64(budget.Ceiling)*cfg.Context.SummaryPct) {
		changed = r.summarize(withCompactionTrigger(ctx, "summary_pct"), s, runID, p) || changed
	}
	if changed {
		next, err := r.measureSession(ctx, p, s, current, false)
		if err != nil {
			r.operationalError(s, runID, "compaction_budget", err)
			return changed
		}
		r.bus.Publish(events.New(events.BudgetEvent, s.ID, runID, next))
	}
	return changed
}
func shouldBatchElide(used, ceiling int, cfg config.GlobalContext, coldPrefill bool) bool {
	if ceiling <= 0 || coldPrefill {
		return false
	}
	// Item 2ey: the batch elide fires at the soft line (it waited for
	// summary_pct before, so the first compaction came only near overflow).
	return used >= int(float64(ceiling)*cfg.SoftPct)
}

type compactionTriggerKey struct{}

// withCompactionTrigger names the line that asked for a compaction, so the
// summarize event records it without threading a parameter through every path.
func withCompactionTrigger(ctx context.Context, trigger string) context.Context {
	return context.WithValue(ctx, compactionTriggerKey{}, trigger)
}

func compactionTrigger(ctx context.Context) string {
	trigger, _ := ctx.Value(compactionTriggerKey{}).(string)
	return trigger
}

// compactToFit reports whether it changed anything, and whether the only reason
// it could not is that the running turn is all that is left.
func (r *Runner) compactToFit(ctx context.Context, s *session.Session, runID string, p *config.Connection, current map[string]bool, budget events.Budget) (bool, bool) {
	cfg := r.cfg()
	readDefaultLimit := min(cfg.Tools.ReadFile.DefaultLimit, cfg.Tools.ReadFile.MaxLimit)
	ctx = withCompactionTrigger(ctx, "overflow")
	changed, _ := r.compact.ElideOldWindow(s, runID, "overflow", budget.UsedEst, int(float64(budget.Ceiling)*.60), contextWindow(budget), readDefaultLimit, func(text string) (int, bool) { return r.count(ctx, p, text) })
	next, err := r.measureSession(ctx, p, s, current, false)
	if err != nil {
		r.operationalError(s, runID, "compaction_budget", err)
	} else {
		guard := next.UsedEst
		if next.Mode == "estimated" {
			guard = int(math.Ceil(float64(guard) * 1.10))
		}
		if guard+next.Reserve > next.NCtx {
			changed = r.summarize(ctx, s, runID, p) || changed
		}
	}
	if changed {
		r.bus.Publish(events.New(events.Stage, s.ID, runID, map[string]any{"stage": "compact", "state": "enter", "turn": s.Snapshot().Run.Turn, "ms": 0}))
		r.bus.Publish(events.New(events.Stage, s.ID, runID, map[string]any{"stage": "compact", "state": "exit", "turn": s.Snapshot().Run.Turn, "ms": 0}))
		return true, false
	}
	_, spanLeft := contextmgr.SummarizeSpan(s.MessagesCopy(), s.RunPin())
	return false, !spanLeft
}
func (r *Runner) operationalError(s *session.Session, runID, where string, err error) {
	r.bus.Publish(events.New(events.Error, s.ID, runID, map[string]any{"where": where, "message": err.Error()}))
}
func requestParams(p *config.Connection, maxTokens int) map[string]any {
	s := p.Sampling.Nonthinking
	if p.Reasoning.Enabled {
		s = p.Sampling.Thinking
	}
	control := p.Reasoning.Control
	if control == "auto" {
		control = p.Capabilities.ReasoningControl
	}
	return map[string]any{"temperature": s.Temperature, "top_p": s.TopP, "top_k": s.TopK, "min_p": s.MinP, "presence_penalty": s.PresencePenalty, "repeat_penalty": s.RepeatPenalty, "max_tokens": maxTokens, "reasoning": map[string]any{"control": control, "effort": p.Reasoning.Effort, "enabled": p.Reasoning.Enabled, "preserve": p.Reasoning.Preserve, "max_tokens": p.Reasoning.MaxTokens}}
}

// keptReadsSentence names the reads still carried verbatim when a run stops
// for context, so the stop says what it was holding on to (item 2et).
func keptReadsSentence(s *session.Session) string {
	messages := s.MessagesCopy()
	calls := map[string]events.ToolCall{}
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			calls[call.ID] = call
		}
	}
	kept := []string{}
	for _, message := range messages {
		if message.Role != "tool" || message.Elided || message.Name != "read_file" {
			continue
		}
		var args struct {
			Path string `json:"path"`
		}
		if json.Unmarshal([]byte(calls[message.ToolCallID].Arguments), &args) == nil && args.Path != "" {
			kept = append(kept, filepath.Base(args.Path))
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return "; reads kept verbatim: " + strings.Join(kept, ", ")
}

func resolveContextWindow(ctx context.Context, connection *config.Connection) (*config.Connection, string, error) {
	if connection.Context.NCtx > 0 {
		return connection, "connection context size", nil
	}
	resolved := *connection
	if connection.Capabilities.NCtx > 0 {
		resolved.Context.NCtx = connection.Capabilities.NCtx
		return &resolved, "probed n_ctx", nil
	}
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	props, err := llm.New(connection).Props(check)
	if err == nil {
		if props.DefaultGenerationSettings.NCtx > 0 {
			resolved.Context.NCtx = props.DefaultGenerationSettings.NCtx
			return &resolved, "per-slot /props n_ctx", nil
		}
		if props.NCtx > 0 {
			resolved.Context.NCtx = props.NCtx
			return &resolved, "/props n_ctx", nil
		}
	}
	label := strings.TrimSpace(connection.Label)
	if label == "" {
		label = connection.ID
	}
	return nil, "", fmt.Errorf("connection %q context size unknown", label)
}

func budgetCountSource(budget events.Budget) string {
	if budget.Estimated || budget.Mode == "estimated" {
		return "estimated accounting"
	}
	return "measured accounting"
}

func guardedPromptTokens(budget events.Budget) int {
	used := budget.UsedEst
	if budget.Mode == "estimated" {
		used = int(math.Ceil(float64(used) * 1.10))
	}
	return used
}

func requestTokenLimit(p *config.Connection, budget events.Budget, promptTokens int) int {
	return max(0, min(max(outputFloor(p), p.Context.ReserveOutput), budget.NCtx-promptTokens))
}

func outputFloor(p *config.Connection) int {
	return min(minimumOutputFloor, p.Context.NCtx)
}
func roughBodyTokens(body any) int { return int(math.Ceil(float64(jsonSize(body)) / 3.6)) }
func jsonSize(value any) int       { data, _ := json.Marshal(value); return len(data) }
func nullable(value int) any {
	if value < 0 {
		return nil
	}
	return value
}
func boolPointer(value bool) *bool { return &value }
func preview(value string) string {
	value = strings.ReplaceAll(value, "\r", "")
	runes := []rune(value)
	if len(runes) > 500 {
		return string(runes[:500]) + "…"
	}
	return value
}
func appendUnique(values []string, value string) []string {
	for _, v := range values {
		if v == value {
			return values
		}
	}
	return append(values, value)
}
