package agent

import (
	"context"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"harness/internal/config"
	"harness/internal/delivery"
	"harness/internal/events"
	"harness/internal/llm"
	"harness/internal/session"
	"harness/internal/tools"
	workspaceinfo "harness/internal/workspace"
)

type runState struct {
	r                                                 *Runner
	ctx                                               context.Context
	s                                                 *session.Session
	runID                                             string
	runCfg                                            config.RunConfig
	wallExceeded                                      atomic.Bool
	cancelWall                                        context.CancelFunc
	wallTimer                                         *time.Timer
	produced                                          map[string]delivery.Source
	workspaceBefore                                   map[string]workspaceFileState
	publishFinalBudget                                bool
	turn                                              int
	lengthSeen, malformedToolRetried, answeredThisRun bool
	announcedActionContinued, autoContinue            bool
	truncatedToolRetry, retryInstruction              string
	refusalSentence                                   string
	refusalAttempt                                    int
	refusedTurn                                       int
	attachmentsAsTextTurn                             int
	accountingRepairTried, templateRetryTried         bool
	fallbackActive                                    bool
	fallbackID                                        string
	primaryLabel                                      string
	primaryWindow                                     int
	compacted                                         bool
	runMessageLimit, runByteLimit, runWindow          int
	guards                                            *runGuards
	currentReasoning                                  map[string]bool
	connection                                        *config.Connection
	windowSource                                      string
	contextStop                                       func(int, string) (string, string, int)
	client                                            *llm.Client
	enabled                                           map[string]bool
	request                                           llm.Request
	budget                                            events.Budget
	requestEvent                                      events.Event
	response                                          llm.Response
	callErr                                           error
	partial                                           string
	answerBegan                                       bool
	callCtx                                           context.Context
	answerStarted                                     time.Time
	toolCalls, durableToolCalls                       []events.ToolCall
	assistant                                         events.Message
	results                                           []runToolResult
	hooks                                             []config.Hook
	toolNames                                         []string
	schemas                                           []any
	body                                              map[string]any
	system                                            string
	budgetErr                                         error
	budgetBusy                                        bool
	pinReady, flightReady, wallReady, deliveryReady   bool
	shellReady, fileReady, budgetReady                bool
}

type runToolResult struct {
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

func (r *Runner) runInPhases(ctx context.Context, s *session.Session, runID string) (reason string, detail string, turns int) {
	x := &runState{r: r, ctx: ctx, s: s, runID: runID, refusedTurn: -1, publishFinalBudget: true, hooks: append([]config.Hook(nil), r.cfg().Hooks...)}
	defer func() {
		r.runHooks(context.Background(), s, runID, x.hooks, hookInput{event: "run_end", stopReason: reason})
	}()
	defer x.cleanupPin()
	defer x.cleanupFlight()
	defer x.cleanupWall()
	defer func() { x.cleanupDelivery(&reason, &detail, turns) }()
	defer x.cleanupShellGrants()
	defer x.r.tools.KillRunProcesses(s.ID, runID)
	defer x.cleanupFileGrant()
	defer x.cleanupFinalBudget()
	if outcome := x.start(); outcome.stop {
		return outcome.reason, outcome.detail, outcome.turns
	}
	for {
		restart := false
		for _, phase := range []func() runOutcome{x.turnBoundary, x.buildRequest, x.fitWindow, x.callModel, x.readAnswer, x.finishOrRunTools, x.endTurn} {
			outcome := phase()
			if outcome.stop {
				return outcome.reason, outcome.detail, outcome.turns
			}
			if outcome.again {
				restart = true
				break
			}
		}
		if restart {
			continue
		}
	}
}

type runOutcome struct {
	again, stop    bool
	reason, detail string
	turns          int
}

var runGoOnOutcome = runOutcome{}
var runAgainOutcome = runOutcome{again: true}

func stopWorkspaceNotRunnable(detail string, count int) (string, string, int) {
	return "workspace_not_runnable", detail, count
}

func stopMailboxStop(detail string, turn int) (string, string, int) {
	return "mailbox_stop", detail, turn
}

func stopConnectionNotRunnable(detail string, turn int) (string, string, int) {
	return "connection_not_runnable", detail, turn
}

func stopDone(detail string, turn int) (string, string, int) {
	return "done", detail, turn
}

func stopModelUnreachable(detail string, turn int) (string, string, int) {
	return "model_unreachable", detail, turn
}

func stopModelError(detail string, turn int) (string, string, int) {
	return "model_error", detail, turn
}

func stopContextExhausted(detail string, turn int) (string, string, int) {
	return "context_exhausted", detail, turn
}

func stopContextCeiling(detail string, turn int) (string, string, int) {
	return "context_ceiling", detail, turn
}

func stopLength(detail string, turn int) (string, string, int) {
	return "length", detail, turn
}

func stopMalformedTurn(detail string, turn int) (string, string, int) {
	return "malformed_turn", detail, turn
}

func stopCycle(detail string, turn int) (string, string, int) {
	return "cycle", detail, turn
}

func (x *runState) stop(reason, detail string, turns int) runOutcome {
	return runOutcome{stop: true, reason: reason, detail: detail, turns: turns}
}

func (x *runState) start() runOutcome {
	r := x.r

	x.s.MarkNetworkBoundaryStale(session.NetworkBoundary(r.cfg()))
	workspaceOK, workspaceReason, workspaceChanged := x.s.EnsureWorkspace()
	if workspaceChanged {
		snapshot := x.s.Snapshot()
		r.bus.Publish(events.New(events.SessionUpdated, x.s.ID, x.runID, map[string]any{
			"session_id": x.s.ID, "workspace_missing": snapshot.WorkspaceMissing,
			"runnable": snapshot.Runnable, "not_runnable_reason": snapshot.NotRunnableReason,
		}))
	}
	if !workspaceOK {
		return x.stop("workspace_not_runnable", workspaceReason, 0)
	}
	x.s.ResetRunTouches()
	for _, message := range x.s.MessagesCopy() {
		if message.Role == "user" {
			x.s.TouchMentionedProjects(message.Content)
		}
	}
	if block, changed := x.s.RefreshProject(); changed {
		files := x.s.Snapshot().ProjectFiles
		_, counts, _ := workspaceinfo.RenderInstructionSet(files)
		r.bus.Publish(events.New(events.ProjectInstructions, x.s.ID, x.runID, map[string]any{"block": block, "files": files, "tokens": counts, "refreshed": true}))
	}
	// Pin the message this run is answering before anything can compact. From
	// here to the end of the history is the task, and it is never summarised,
	// elided or superseded away.
	x.s.SetRunPin(session.RunPinFromTail(x.s.MessagesCopy()))
	x.pinReady = true
	r.beginFlight(x.s.ID, x.runID)
	x.flightReady = true
	if cfg := r.cfg(); !cfg.Shell.ServiceAccount.Enabled {
		log.Printf("tool identity: process (service split disabled) session=%s run=%s", x.s.ID, x.runID)
	} else if !cfg.Shell.OperatorContext {
		if err := r.tools.PreflightServiceIdentity(); err != nil {
			log.Printf("tool identity: service identity not set up session=%s run=%s reason=%q", x.s.ID, x.runID, err.Error())
			if r.identityInvitation.CompareAndSwap(false, true) {
				r.bus.Publish(events.New(events.ServiceIdentityUnavailable, x.s.ID, x.runID, serviceIdentityUnavailableData(err.Error())))
			}
		} else {
			log.Printf("tool identity: service session=%s run=%s", x.s.ID, x.runID)
		}
	} else {
		log.Printf("tool identity: operator mode session=%s run=%s", x.s.ID, x.runID)
	}
	x.runCfg = r.cfg().Run
	if x.runCfg.MaxWallClockSeconds <= 0 {
		x.runCfg.MaxWallClockSeconds = config.DefaultMaxWallClockSeconds
	}
	var wallContext context.Context
	wallContext, x.cancelWall = context.WithCancel(x.ctx)
	x.wallTimer = time.AfterFunc(time.Duration(x.runCfg.MaxWallClockSeconds)*time.Second, func() {
		x.wallExceeded.Store(true)
		x.cancelWall()
	})
	x.wallReady = true
	x.ctx = wallContext
	x.contextStop = func(turn int, fallback string) (string, string, int) {
		if x.wallExceeded.Load() {
			return "wall_clock", fmt.Sprintf("guessed wall-clock backstop reached after %d seconds", x.runCfg.MaxWallClockSeconds), turn
		}
		return r.stopped(x.s, x.runID, turn, fallback)
	}
	x.produced = map[string]delivery.Source{}
	x.workspaceBefore = workspaceFileSnapshot(x.s.Workspace)
	x.deliveryReady = true
	x.shellReady = true
	x.fileReady = true
	if stop, detail := r.applyMailboxBoundary(x.ctx, x.s, x.runID, false); stop {
		if x.ctx.Err() != nil {
			return x.stop(x.contextStop(0, detail))
		}
		return x.stop("mailbox_stop", detail, 0)
	}
	var ok bool
	x.connection, ok = r.currentConnection(x.s)
	if !ok {
		return x.stop("connection_not_runnable", "connection "+x.s.ConnectionID+" no longer exists", 0)
	}
	var windowErr error
	x.connection, x.windowSource, windowErr = r.resolveWindow(x.ctx, x.connection)
	if windowErr != nil {
		return x.stop("connection_not_runnable", windowErr.Error(), 0)
	}
	snapshot := x.s.Snapshot()
	if !snapshot.Runnable {
		return x.stop("connection_not_runnable", snapshot.NotRunnableReason, 0)
	}
	x.budgetReady = true
	x.turn = 0
	x.lengthSeen = false
	x.truncatedToolRetry = ""
	x.retryInstruction = ""
	x.malformedToolRetried = false
	x.answeredThisRun = false
	x.announcedActionContinued, x.autoContinue = false, false
	// a read_file window refused on two consecutive turns ends the
	// read; from then on the model answers from what it has read, without
	// tools. refusedTurn is the turn of the latest refusal (-1: none since the
	// last window that fit). The elide between the two turns has had its chance.
	x.refusedTurn = -1
	x.accountingRepairTried = false
	x.templateRetryTried = false
	x.runMessageLimit, x.runByteLimit, x.runWindow = 0, 0, 0
	x.guards = newRunGuards(x.runCfg.CycleWindow).withLoopKey(x.r.tools.LoopKey)
	x.currentReasoning = map[string]bool{}
	return runGoOnOutcome
}
func (x *runState) turnBoundary() runOutcome {
	r := x.r
	var ok bool
	var windowErr error
	if x.ctx.Err() != nil {
		return x.stop(x.contextStop(x.turn, "cancellation requested"))
	}
	if stop, detail := r.applyMailboxBoundary(x.ctx, x.s, x.runID, false); stop {
		if x.ctx.Err() != nil {
			return x.stop(x.contextStop(x.turn, detail))
		}
		return x.stop("mailbox_stop", detail, x.turn)
	}
	if r.takeQueued != nil {
		r.takeQueued(x.s, x.runID)
	}
	x.turn++
	connectionID := x.s.Snapshot().ConnectionID
	if x.fallbackActive {
		connectionID = x.fallbackID
	}
	x.connection, ok = r.connection(connectionID)
	if !ok {
		return x.stop("connection_not_runnable", "connection "+connectionID+" no longer exists", x.turn-1)
	}
	x.connection, x.windowSource, windowErr = r.resolveWindow(x.ctx, x.connection)
	if windowErr != nil {
		return x.stop("connection_not_runnable", windowErr.Error(), x.turn-1)
	}
	localConnection := *x.connection
	if profile, found := r.cfg().Agent(x.s.Snapshot().AgentID); found {
		localConnection = connectionWithProfileThinking(localConnection, *profile)
	}
	x.connection = &localConnection
	x.connection.Capabilities.ObservedMessageLimit = x.runMessageLimit
	x.connection.Capabilities.ObservedByteLimit = x.runByteLimit
	// 2wb: the turn whose server refused an attached part sends it as text, that turn only.
	if x.attachmentsAsTextTurn == x.turn {
		x.connection.AttachmentHandling = "extract"
	}
	if x.runWindow > 0 {
		x.connection.Context.NCtx = x.runWindow
		x.connection.Context.ReserveOutput = min(x.connection.Context.ReserveOutput, x.runWindow/2)
		x.windowSource = fmt.Sprintf("the server refused this run above %d tokens", x.runWindow)
	}
	// An invalid durable tool call is a history-shape problem, not an
	// accounting endpoint failure. Repair it before any template or tokenizer
	// request so the one accounting degrade boundary never has to leak an
	// endpoint error merely to trigger history repair.
	if !x.accountingRepairTried && r.repairMalformedToolCall(x.ctx, x.s, x.runID, x.connection, x.currentReasoning) {
		x.accountingRepairTried = true
		x.turn--
		return runAgainOutcome
	}
	x.client = llm.New(x.connection)
	state := x.s.Snapshot().Run
	state.Turn = x.turn
	x.s.SetRun(state)
	x.enabled = x.s.EnabledTools()
	if x.enabled["image"] {
		cfg := r.cfg()
		x.enabled["image"] = cfg.Images.Connection != "" || x.connection.Capabilities.Images
	}
	x.toolNames = r.tools.Names(x.enabled)
	// Non-configurable read-only tools are returned by Names even when they
	// have no toggle entry. The response guard must use the offered set too.
	for _, name := range x.toolNames {
		x.enabled[name] = true
	}
	x.schemas = r.tools.Schemas(x.enabled)
	x.system = ""
	x.budgetBusy = false
	// the trust flag is per TURN, so it clears here. A note is
	// suspect because of what was in the turn that wrote it, not because of
	// something three turns ago.
	x.s.BeginTurnTrust()
	return runGoOnOutcome
}
func (x *runState) buildRequest() runOutcome {
	r := x.r
	r.stage(x.s, x.runID, x.turn, "assemble", func() {
		records := latestTodoOnly(x.s.MessagesCopy())
		skills := ""
		if r.skills != nil {
			skills = r.skills.Index(x.s.AgentID)
		}
		systemBase := r.prompt.RenderSkillParts(x.connection, x.s, x.toolNames, "", "")
		systemProject := r.prompt.RenderSkillParts(x.connection, x.s, x.toolNames, x.s.ProjectBlock, "")
		systemSkills := r.prompt.RenderSkillParts(x.connection, x.s, x.toolNames, x.s.ProjectBlock, skills)
		workspaceMemory := x.s.WorkspaceMemory()
		systemWorkspaceMemory := r.prompt.RenderSkillParts(x.connection, x.s, x.toolNames, x.s.ProjectBlock, skills, workspaceMemory, "")
		x.system = r.prompt.RenderSkillParts(x.connection, x.s, x.toolNames, x.s.ProjectBlock, skills, workspaceMemory)
		messages := []llm.Message{}
		requestRecords := make([]events.Message, 0, len(records))
		current := runningTurnIDs(records, x.s.RunPin())
		for _, message := range records {
			if isHarnessAbortRecord(message) {
				messages = append(messages, requestMessageAt(x.connection, x.s, message, current[message.ID]))
				requestRecords = append(requestRecords, message)
			}
		}
		for _, message := range records {
			if isHarnessAbortRecord(message) {
				continue
			}
			converted := requestMessageAt(x.connection, x.s, message, current[message.ID])
			if preserveReasoning(x.connection) {
				converted.ReasoningContent = message.Reasoning
			}
			messages = append(messages, converted)
			requestRecords = append(requestRecords, message)
		}
		// rule 1: the list is checked before it is measured or sent.
		conversation, repairedRecords, _ := normalizeAdjacentAssistants(messages, requestRecords)
		hadUser := false
		for _, message := range conversation {
			if message.Role == llm.RoleUser {
				hadUser = true
				break
			}
		}
		messages, x.budgetErr = llm.BuildMessageList(x.system, conversation)
		requestRecords = repairedRecords
		if x.budgetErr != nil {
			return
		}
		if !hadUser {
			requestRecords = append(requestRecords, events.Message{Role: llm.RoleHarness, Category: "history", Content: "Continue from the recorded context."})
		}
		if x.autoContinue {
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: "[harness note]\nContinue from the recorded context."})
			requestRecords = append(requestRecords, events.Message{Role: llm.RoleHarness, Category: "history", Content: "Continue from the recorded context."})
			x.autoContinue = false
		}
		if x.retryInstruction != "" {
			messages = append(messages, llm.Message{Role: llm.RoleAssistant, Content: "[harness note]\n" + x.retryInstruction})
			requestRecords = append(requestRecords, events.Message{Role: llm.RoleHarness, Category: "history", Content: x.retryInstruction})
		}
		x.request = llm.Request{Messages: messages, Tools: x.schemas, ToolChoice: "auto", Thinking: x.connection.Reasoning.Enabled}
		x.budget, x.budgetErr = r.budget.MeasureWithBusy(x.ctx, x.connection, x.s, r.cfg().Context, budgetInput{SystemBase: systemBase, SystemProject: systemProject, SystemSkills: systemSkills, SystemWorkspaceMemory: systemWorkspaceMemory, System: x.system, WithoutToolSystems: r.withoutToolSystems(x.connection, x.s, x.enabled, workspaceMemory), Schemas: x.schemas, AllSchemas: r.tools.AllSchemas(), Messages: messages[1:], Records: requestRecords}, false, func(err error) {
			x.budgetBusy = true
			r.bus.Publish(events.New(events.ModelBusy, x.s.ID, x.runID, map[string]any{"host": modelHost(x.connection), "detail": err.Error()}))
		})
		if x.budgetErr != nil {
			return
		}
		guardUsed := guardedPromptTokens(x.budget)
		x.request.MaxTokens = requestTokenLimit(x.connection, x.budget, guardUsed)
		if x.request.Thinking && x.connection.Reasoning.MaxTokens > 0 && !containsFinding(x.connection, "server reasoning budget: accepted") {
			x.request.MaxTokens = min(x.request.MaxTokens, x.connection.Reasoning.MaxTokens)
		}
		// with reasoning on and no reasoning cap set, thinking and
		// the answer share one allowance, and a long thinking pass can take all
		// of it and leave nothing. Thinking gets its own share.
		if x.request.Thinking && x.connection.Reasoning.MaxTokens == 0 {
			x.request.ReasoningMaxTokens = config.ReasoningShareFor(x.request.MaxTokens)
		}
		diagnosticRequest := x.request
		diagnosticRequest.Messages = diagnosticMessages(x.request.Messages)
		x.body = llm.BuildRequest(x.connection, diagnosticRequest, true)
		if limit := x.connection.Capabilities.ObservedByteLimit; limit > 0 {
			if size := llm.SerializedBytes(x.connection, x.request, true); size > 0 && x.budget.UsedEst > 0 {
				byteCeiling := x.budget.UsedEst * limit / size
				if byteCeiling > 0 && byteCeiling < x.budget.Ceiling {
					x.budget.Ceiling = byteCeiling
					x.budget.Findings = append(x.budget.Findings, fmt.Sprintf("usable ceiling %d tokens set by %d-byte server limit", byteCeiling, limit))
					x.s.SetBudget(x.budget)
				}
			}
		}
		r.bus.Publish(events.New(events.BudgetEvent, x.s.ID, x.runID, x.budget))
		data := map[string]any{"turn": x.turn, "message_count": len(messages), "tool_count": len(x.schemas), "params": requestParams(x.connection, x.request.MaxTokens), "est_prompt_tokens": x.budget.UsedEst, "estimated": x.budget.Estimated}
		// what the flight recorder's invoke_agent span names, with
		// no text: the template's hash, the model's file name, the tools offered.
		data["connection_kind"], data["model_file"], data["n_ctx"] = connectionKind(x.connection), filepath.Base(filepath.ToSlash(x.connection.Model)), x.budget.NCtx
		data["prompt_hash"], data["tools_offered"] = shortHash(systemBase), x.toolNames
		x.requestEvent = events.New(events.ModelRequest, x.s.ID, x.runID, data)
		x.requestEvent.Body = x.body
	})
	if x.budgetBusy && x.budgetErr == nil {
		r.bus.Publish(events.New(events.ModelReachable, x.s.ID, x.runID, map[string]any{"connection_id": x.connection.ID}))
	}
	if x.budgetErr != nil {
		if x.ctx.Err() != nil {
			return x.stop(x.contextStop(x.turn-1, "budget accounting canceled"))
		}
		r.operationalError(x.s, x.runID, "budget", x.budgetErr)
		if !x.accountingRepairTried && r.repairMalformedToolCall(x.ctx, x.s, x.runID, x.connection, x.currentReasoning) {
			x.accountingRepairTried = true
			x.turn--
			return runAgainOutcome
		}
		// rule 2: a refused template is repaired and tried once more;
		// only a second refusal stops the run, with its reason. The list is
		// normalised on every assembly, so the retry reassembles it.
		if !x.templateRetryTried && templateRefused(x.budgetErr) {
			x.templateRetryTried = true
			x.turn--
			return runAgainOutcome
		}
		if x.beginFallback(x.budgetErr) {
			x.turn--
			return runAgainOutcome
		}
		if r.publishModelUnreachable(x.s, x.runID, x.connection, x.budgetErr) {
			x.publishFinalBudget = false
			return x.stop("model_unreachable", x.budgetErr.Error(), x.turn-1)
		}
		return x.stop("model_error", "budget accounting: "+x.budgetErr.Error(), x.turn-1)
	}
	return runGoOnOutcome
}

// latestTodoOnly keeps every durable message intact while ensuring that the
// model request carries only the current task list. Earlier calls remain as
// visible history, but their arguments no longer repeat superseded list data.
func latestTodoOnly(records []events.Message) []events.Message {
	latestMessage, latestCall := -1, -1
	for i := range records {
		for j := range records[i].ToolCalls {
			if records[i].ToolCalls[j].Name == "todo" {
				latestMessage, latestCall = i, j
			}
		}
	}
	if latestMessage < 0 {
		return records
	}
	result := append([]events.Message(nil), records...)
	for i := range result {
		if len(result[i].ToolCalls) == 0 {
			continue
		}
		result[i].ToolCalls = append([]events.ToolCall(nil), result[i].ToolCalls...)
		for j := range result[i].ToolCalls {
			if result[i].ToolCalls[j].Name == "todo" && (i != latestMessage || j != latestCall) {
				result[i].ToolCalls[j].Arguments = `{}`
			}
		}
	}
	return result
}
func (x *runState) fitWindow() runOutcome {
	if x.fallbackActive && guardedPromptTokens(x.budget) > x.budget.Ceiling {
		detail := fmt.Sprintf("%s (%d tokens) did not answer; %s (%d tokens) cannot fit the %d-token request", x.primaryLabel, x.primaryWindow, x.connection.Label, x.connection.Context.NCtx, guardedPromptTokens(x.budget))
		return x.stop("context_exhausted", detail, x.turn-1)
	}
	result := x.compactBeforeRequest()
	if result.Stop != "" {
		reason := "context_exhausted"
		if x.refusalSentence != "" {
			reason = "model_error"
		}
		return x.stop(reason, result.Stop, x.turn-1)
	}
	if result.Changed {
		x.compacted = true
		if x.refusalSentence != "" {
			x.refusalSentence = ""
		}
		x.turn--
		return runAgainOutcome
	}
	x.r.bus.Publish(x.requestEvent)
	x.r.budget.MarkRequest(x.s.ID, x.budget.UsedEst)
	return runGoOnOutcome
}

func (x *runState) callModel() runOutcome {
	return x.callModelStep()
}

func (x *runState) callModelStep() runOutcome {
	r := x.r
	x.response = llm.Response{}
	x.callErr = nil
	x.partial = ""
	x.answerBegan = false
	toolArgumentBytes := map[int]int{}
	toolArgumentRunes := map[int]int{}
	var streamBusy atomic.Bool
	// a ceiling on ONE answer, never on a run or a chat. Zero,
	// which is what every existing configuration carries, means no ceiling.
	x.callCtx = x.ctx
	x.answerStarted = time.Now()
	if ceiling := answerCeiling(x.connection); ceiling > 0 {
		var cancelAnswer context.CancelFunc
		x.callCtx, cancelAnswer = context.WithTimeout(x.ctx, ceiling)
		defer cancelAnswer()
	}
	statusCtx, stopStatus := context.WithCancel(x.callCtx)
	r.stage(x.s, x.runID, x.turn, "call_model", func() {
		for attempt := 0; ; attempt++ {
			x.response, x.callErr = x.client.ChatStreamStatus(x.callCtx, x.request, func(delta llm.Delta) {
				stopStatus()
				if delta.Kind != "progress" {
					x.answerBegan = true
				}
				r.addFlightDelta(x.s.ID, x.runID, delta)
				if delta.Kind == "progress" {
					r.bus.Publish(events.New(events.ModelProgress, x.s.ID, x.runID, map[string]any{"turn": x.turn, "total": delta.Total, "cache": delta.Cache, "processed": delta.Processed}))
					return
				}
				if delta.Kind == "content" {
					x.partial += delta.Text
					x.s.UpdatePartial(x.partial)
				}
				data := map[string]any{"turn": x.turn, "kind": delta.Kind, "index": delta.Index, "text": durableModelDeltaText(delta.Kind, delta.Text)}
				if delta.Kind == "tool_call" {
					toolArgumentBytes[delta.Index] += len([]byte(delta.Text))
					toolArgumentRunes[delta.Index] += len([]rune(delta.Text))
					data["call_id"], data["name"] = delta.CallID, delta.Name
					data["argument_bytes"] = toolArgumentBytes[delta.Index]
					data["argument_tokens"] = int(math.Ceil(float64(toolArgumentRunes[delta.Index]) / 3.6))
				}
				r.bus.Publish(events.New(events.ModelDelta, x.s.ID, x.runID, data))
			}, func() {
				streamBusy.Store(true)
				r.bus.Publish(events.New(events.ModelBusy, x.s.ID, x.runID, map[string]any{"host": modelHost(x.connection), "detail": "connected; waiting for model response"}))
				go watchConnectionModelLoading(statusCtx, x.client, x.connection, 5*time.Second, func(loading bool) {
					detail := "connected; waiting for model response"
					if loading {
						detail = "loading model"
					}
					r.bus.Publish(events.New(events.ModelBusy, x.s.ID, x.runID, map[string]any{"host": modelHost(x.connection), "detail": detail}))
				})
			})
			if x.callErr == nil || x.answerBegan || attempt >= len(r.modelRetryDelays) || !retryableModelCall(x.callErr) {
				break
			}
			next := attempt + 2
			r.bus.Publish(events.New(events.ModelBusy, x.s.ID, x.runID, map[string]any{"host": modelHost(x.connection), "detail": fmt.Sprintf("model unreachable · trying again · try %d of %d", next, len(r.modelRetryDelays)+1)}))
			timer := time.NewTimer(r.modelRetryDelays[attempt])
			select {
			case <-x.callCtx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				x.callErr = x.callCtx.Err()
				return
			case <-timer.C:
			}
		}
		stopStatus()
		x.s.UpdatePartial("")
	})
	if streamBusy.Load() && x.callErr == nil {
		r.bus.Publish(events.New(events.ModelReachable, x.s.ID, x.runID, map[string]any{"connection_id": x.connection.ID}))
	}
	if x.callErr != nil {
		if x.ctx.Err() != nil {
			return x.stop(x.contextStop(x.turn, "model call canceled"))
		}
		// a server's refusal as its status and its own type word,
		// never its message; published before any retry, so a refusal the run
		// recovers from is still counted.
		if status, kind := serverRefusal(x.callErr); status > 0 {
			r.bus.Publish(events.New(events.ModelRefused, x.s.ID, x.runID, map[string]any{"status": status, "error_type": kind, "connection_kind": connectionKind(x.connection)}))
		}
		// the answer, not the run, hit its ceiling. Same treatment
		// as a token truncation — name the ceiling and keep what was produced.
		if x.callCtx.Err() != nil && answerCeiling(x.connection) > 0 {
			truncated := x.response
			if strings.TrimSpace(truncated.Content) == "" {
				truncated.Content = x.partial
			}
			r.keepTruncatedAnswer(x.ctx, x.s, x.runID, x.connection, truncated, x.turn, x.currentReasoning)
			return x.stop("length", answerCeilingDetail(x.connection, time.Since(x.answerStarted)), x.turn)
		}
		if limit, sentence, matched := byteLimitError(x.callErr); matched {
			x.runByteLimit = limit
			x.connection.Capabilities.ObservedByteLimit = limit
			r.appendHarnessLine(x.ctx, x.connection, x.s, x.runID, x.turn, fmt.Sprintf("The server refused this run above %d bytes: %s. This limit applies only to this run.", limit, sentence))
			// a retry that would send the same bytes is not made.
			// The refusal is acted on once -- compacted against the limit it
			// named -- and only a genuinely smaller body is sent again.
			// each pass makes the body genuinely smaller -- a cut or
			// a summary -- so it is retried until nothing is left to cut; the
			// bound only guards against a server whose limit keeps moving.
			x.refusalSentence, x.refusalAttempt = sentence, x.refusalAttempt+1
			x.turn--
			return runAgainOutcome
		}
		if limit, sentence, matched := messageLimitError(x.callErr); matched {
			x.runMessageLimit = limit
			x.connection.Capabilities.ObservedMessageLimit = limit
			r.appendHarnessLine(x.ctx, x.connection, x.s, x.runID, x.turn, fmt.Sprintf("The server refused this run above %d messages: %s. This limit applies only to this run.", limit, sentence))
			x.refusalSentence, x.refusalAttempt = sentence, x.refusalAttempt+1
			x.turn--
			return runAgainOutcome
		}
		// the window is the server's. A refusal that names the
		// window it allows is learned, and the run compacts to it and retries
		// once rather than stopping (s48: 33,849 tokens against 32,768).
		if window, matched := windowLimitError(x.callErr); matched {
			x.runWindow = window
			r.appendHarnessLine(x.ctx, x.connection, x.s, x.runID, x.turn, fmt.Sprintf("The server refused this run above a %d-token window. This window applies only to this run.", window))
			x.refusalSentence, x.refusalAttempt = x.callErr.Error(), x.refusalAttempt+1
			x.turn--
			return runAgainOutcome
		}
		if tokenWindowRefusal(x.callErr) {
			x.refusalSentence, x.refusalAttempt = x.callErr.Error(), x.refusalAttempt+1
			x.turn--
			return runAgainOutcome
		}
		if kind := attachedPartKind(x.request); kind != "" && x.attachmentsAsTextTurn != x.turn && attachedPartRefusal(x.callErr) {
			x.attachmentsAsTextTurn = x.turn
			r.bus.Publish(events.New(events.ModelRetry, x.s.ID, x.runID, map[string]any{"turn": x.turn, "reason": "attachment_as_text", "kind": kind, "attempt": 1, "max_attempts": 1}))
			x.turn--
			return runAgainOutcome
		}
		if x.beginFallback(x.callErr) {
			x.turn--
			return runAgainOutcome
		}
		if r.publishModelUnreachable(x.s, x.runID, x.connection, x.callErr) {
			if !x.answerBegan && !x.answeredThisRun && r.returnMessage != nil && r.returnMessage(x.s, x.runID) {
				x.callErr = fmt.Errorf("%w\nyour message is back in the box", x.callErr)
			}
			x.publishFinalBudget = false
			return x.stop("model_unreachable", x.callErr.Error(), x.turn)
		}
		if !x.answerBegan && !x.answeredThisRun && r.returnMessage != nil && r.returnMessage(x.s, x.runID) {
			x.callErr = fmt.Errorf("%w\nyour message is back in the box", x.callErr)
		}
		return x.stop("model_error", x.callErr.Error(), x.turn)
	}
	return runGoOnOutcome
}

func (x *runState) beginFallback(err error) bool {
	if x.fallbackActive || x.connection.Fallback == "" || x.answerBegan || !fallbackEligible(err) {
		return false
	}
	fallback, ok := x.r.connection(x.connection.Fallback)
	if !ok || fallback.ID == x.connection.ID {
		return false
	}
	x.primaryLabel, x.primaryWindow = x.connection.Label, x.connection.Context.NCtx
	x.fallbackID, x.fallbackActive = fallback.ID, true
	notice := fmt.Sprintf("%s did not answer — continuing on %s", x.connection.Label, fallback.Label)
	x.r.bus.Publish(events.New(events.SessionUpdated, x.s.ID, x.runID, map[string]any{"session_id": x.s.ID, "notice": notice}))
	return true
}
func (x *runState) readAnswer() runOutcome {
	r := x.r
	r.budget.RecordUsage(x.s.ID, x.response.Usage.PromptTokens, x.response.Usage.CachedTokens)
	if r.modelAnswered != nil {
		r.modelAnswered(x.connection.ID)
	}
	x.toolCalls = make([]events.ToolCall, 0, len(x.response.ToolCalls))
	for _, call := range x.response.ToolCalls {
		x.toolCalls = append(x.toolCalls, events.ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
	}
	rawToolCalls := append([]events.ToolCall(nil), x.toolCalls...)
	if len(rawToolCalls) > 0 {
		x.announcedActionContinued = false
	}
	x.s.RecordModelTurn()
	x.answeredThisRun = true
	if retry, stop := malformedToolTurnAction(x.response.FinishReason, len(x.toolCalls), x.malformedToolRetried); retry {
		x.malformedToolRetried = true
		x.retryInstruction = "The previous response ended as tool_calls but contained no calls. Return one complete offered tool call or a final answer."
		r.bus.Publish(events.New(events.ModelRetry, x.s.ID, x.runID, map[string]any{"turn": x.turn, "next_turn": x.turn + 1, "reason": "malformed_tool_turn", "attempt": 1, "max_attempts": 1}))
		return runAgainOutcome
	} else if stop {
		r.appendHarnessLine(x.ctx, x.connection, x.s, x.runID, x.turn, "malformed tool turn: finish_reason tool_calls contained no calls twice")
		return x.stop("malformed_turn", "finish_reason tool_calls contained no calls after one retry", x.turn)
	}
	var guardLines []string
	for _, call := range x.toolCalls {
		if !x.enabled[call.Name] {
			r.bus.Publish(events.New(events.ToolUnoffered, x.s.ID, x.runID, map[string]any{"turn": x.turn, "name": call.Name}))
		}
	}
	x.toolCalls, guardLines = guardModelToolCalls(x.toolCalls, x.enabled)
	for _, line := range guardLines {
		r.appendHarnessLine(x.ctx, x.connection, x.s, x.runID, x.turn, line)
	}
	if len(x.response.ToolCalls) > 0 && len(x.toolCalls) == 0 {
		x.retryInstruction = "The previous response named no offered tool. Use only an offered tool or return a final answer."
		return runAgainOutcome
	}
	visibleReasoning := llm.VisibleReasoning(x.response.Reasoning)
	reasoningTokens, reasoningTokensEstimated := r.count(x.ctx, x.connection, visibleReasoning)
	if x.response.Usage.ReasoningTokens > 0 {
		reasoningTokens, reasoningTokensEstimated = x.response.Usage.ReasoningTokens, false
	}
	x.durableToolCalls = sanitizedToolCalls(x.toolCalls)
	responseData := map[string]any{"turn": x.turn, "finish_reason": x.response.FinishReason, "content": x.response.Content, "reasoning_tokens": reasoningTokens, "reasoning_tokens_estimated": reasoningTokensEstimated, "tool_calls": x.durableToolCalls, "usage": map[string]any{"prompt_tokens": x.response.Usage.PromptTokens, "completion_tokens": x.response.Usage.CompletionTokens, "cached_tokens": nullable(x.response.Usage.CachedTokens)}, "timings": x.response.Timings, "duration_ms": x.response.DurationMS}
	responseEvent := events.New(events.ModelResponse, x.s.ID, x.runID, responseData)
	responseEvent.Raw = redactToolCallHeaders(string(x.response.Raw), rawToolCalls)
	r.bus.Publish(responseEvent)
	if x.s.Role == "e" && x.s.ParentSessionID != "" {
		r.bus.Publish(events.New(events.DelegatedUsage, x.s.ParentSessionID, x.runID, map[string]any{"connection_id": x.connection.ID, "usage": responseData["usage"], "tool_calls": x.durableToolCalls}))
	}
	r.stage(x.s, x.runID, x.turn, "parse", func() {})
	if x.response.FinishReason == "length" && len(x.toolCalls) > 0 {
		if x.lengthSeen {
			return x.stop("length", fmt.Sprintf("truncated %s call hit the output limit twice", firstToolName(x.durableToolCalls)), x.turn)
		}
		if firstToolName(x.durableToolCalls) == "" {
			return x.stop("length", "truncated tool call had no function name and could not be retried safely", x.turn)
		}
		x.lengthSeen = true
		x.truncatedToolRetry = firstToolName(x.durableToolCalls)
		x.retryInstruction = "The previous " + x.truncatedToolRetry + " call was truncated. Return that complete offered tool call again, with valid JSON arguments."
		r.bus.Publish(events.New(events.ModelRetry, x.s.ID, x.runID, map[string]any{"turn": x.turn, "next_turn": x.turn + 1, "reason": "truncated_tool_call", "tool": x.truncatedToolRetry, "attempt": 1, "max_attempts": 1}))
		return runAgainOutcome
	}
	if x.truncatedToolRetry != "" {
		if len(x.toolCalls) == 0 || firstToolName(x.durableToolCalls) != x.truncatedToolRetry {
			return x.stop("length", fmt.Sprintf("truncated %s call retry did not return that tool call", x.truncatedToolRetry), x.turn)
		}
		x.truncatedToolRetry = ""
		x.retryInstruction = ""
		x.lengthSeen = false
	}
	if len(x.toolCalls) == 0 && x.response.FinishReason != "tool_calls" {
		if x.response.FinishReason == "length" {
			// the cap is named with its value and where it
			// is set, and what the model did produce is kept in the transcript
			// marked truncated, so the Continue action on the stop card has
			// something to continue from instead of the run being thrown away.
			detail := truncationDetail(x.connection, x.response)
			r.keepTruncatedAnswer(x.ctx, x.s, x.runID, x.connection, x.response, x.turn, x.currentReasoning)
			return x.stop("length", detail, x.turn)
		}
		finalContent := x.response.Content
		stopReason, stopDetail := "done", ""
		continueAnnouncedAction := false
		if strings.TrimSpace(finalContent) == "" {
			if reasoning := strings.TrimSpace(visibleReasoning); reasoning != "" {
				finalContent = reasoning + "\n\n[answer taken from the model's reasoning]"
				stopReason, stopDetail = "reply_empty_reasoning_shown", "reply empty — reasoning shown"
			} else {
				finalContent = "[model returned an empty reply]"
				stopReason, stopDetail = "reply_empty", "reply empty — no reasoning available"
			}
		} else if x.response.FinishReason == "stop" && announcedActionOnly(finalContent) {
			if x.announcedActionContinued {
				stopReason, stopDetail = "announced_action_and_stopped", "announced an action and stopped twice in a row"
			} else {
				continueAnnouncedAction = true
			}
		}
		r.stage(x.s, x.runID, x.turn, "append", func() {
			visible, proposals := planProposalsFor(x.s, finalContent)
			message, _ := r.makeMessage(x.ctx, x.connection, "assistant", visible, "history", x.turn)
			message.Reasoning = x.response.Reasoning
			message.PlanProposals = bindPlanProposalSources(x.s.MessagesCopy(), proposals, message.ID)
			x.currentReasoning[message.ID] = true
			x.s.Append(message)
			r.bus.Publish(events.New(events.MessageAppended, x.s.ID, x.runID, map[string]any{"message": message}))
		})
		if continueAnnouncedAction {
			x.announcedActionContinued, x.autoContinue = true, true
			r.bus.Publish(events.New(events.ModelRetry, x.s.ID, x.runID, map[string]any{"turn": x.turn, "next_turn": x.turn + 1, "reason": "announced_action", "attempt": 1, "max_attempts": 1}))
			return runAgainOutcome
		}
		if stopReason != "done" {
			return x.stop(stopReason, stopDetail, x.turn)
		}
		if r.skills != nil {
			if detail, err := r.skills.Proposal(x.ctx, x.s, x.runID); err != nil {
				return x.stop("done", "skill proposal: "+err.Error(), x.turn)
			} else if detail != "" {
				r.appendHarnessLine(x.ctx, x.connection, x.s, x.runID, x.turn, detail)
				return x.stop("done", detail, x.turn)
			}
		}
		return x.stop("done", "", x.turn)
	}
	visible, proposals := planProposalsFor(x.s, x.response.Content)
	x.assistant, _ = r.makeMessage(x.ctx, x.connection, "assistant", visible, "history", x.turn)
	x.assistant.Reasoning = x.response.Reasoning
	x.assistant.PlanProposals = bindPlanProposalSources(x.s.MessagesCopy(), proposals, x.assistant.ID)
	x.currentReasoning[x.assistant.ID] = true
	x.assistant.ToolCalls = x.durableToolCalls
	return runGoOnOutcome
}
func (x *runState) finishOrRunTools() runOutcome {
	return x.finishOrRunToolsStep()
}

func (x *runState) runRawTool(item *runToolResult) {
	r := x.r
	start := time.Now()
	if item.argErr != nil {
		item.content = "error: " + item.argErr.Error()
	} else if before := r.runHooks(x.ctx, x.s, x.runID, x.hooks, hookInput{event: "before_tool", tool: item.call.Name, arguments: item.args}); before.refusal != "" {
		item.content = before.refusal
	} else {
		r.setFlightTool(x.s.ID, x.runID, x.turn, item.durableCall, item.args)
		outcome := r.executeTool(x.ctx, x.s, x.runID, item.call.ID, item.call.Name, item.args)
		item.content, item.ok, item.operatorContext = outcome.Content, outcome.OK, outcome.OperatorContext
		item.category, item.untrusted, item.metadata = outcome.Category, outcome.Untrusted, outcome.Metadata
		r.setFlightToolOutput(x.s.ID, x.runID, item.content)
		if outcome.Untrusted {
			x.s.MarkUntrustedInTurn()
		}
	}
	if item.argErr == nil {
		after := r.runHooks(x.ctx, x.s, x.runID, x.hooks, hookInput{event: "after_tool", tool: item.call.Name, arguments: item.args, success: item.ok})
		for _, output := range after.output {
			item.content += "\n\n" + output
		}
	}
	item.ms = time.Since(start).Milliseconds()
}

func (x *runState) runRawTools() {
	r := x.r
	for start := 0; start < len(x.results); {
		end := start + 1
		if r.tools.Reads(x.results[start].call.Name, x.results[start].args) {
			for end < len(x.results) && r.tools.Reads(x.results[end].call.Name, x.results[end].args) {
				end++
			}
		}
		for base := start; base < end; base += 4 {
			limit := min(end, base+4)
			var wait sync.WaitGroup
			for index := base; index < limit; index++ {
				item := &x.results[index]
				r.bus.Publish(events.New(events.ToolCallEvent, x.s.ID, x.runID, map[string]any{"turn": x.turn, "call_id": item.call.ID, "name": item.call.Name, "args": sanitizedToolArguments(item.call.Name, item.args), "args_invalid": item.argErr != nil}))
				if x.s.ParentSessionID != "" {
					r.bus.PublishTransient(events.New(events.DelegatedUsage, x.s.ParentSessionID, x.runID, map[string]any{"child_id": x.s.ID, "status": "running", "tool": item.call.Name, "target": shortActivityTarget(item.args), "turn": x.turn, "live": true}))
				}
				wait.Add(1)
				go func(item *runToolResult) { defer wait.Done(); x.runRawTool(item) }(item)
			}
			wait.Wait()
		}
		start = end
	}
}

func (x *runState) finishOrRunToolsStep() runOutcome {
	r := x.r
	x.results = []runToolResult{}
	r.stage(x.s, x.runID, x.turn, "dispatch", func() {
		for index, call := range x.toolCalls {
			var args map[string]any
			decoded, err := tools.DecodeArgs(call.Arguments)
			if err == nil {
				args = decoded
			} else {
				args = map[string]any{}
			}
			x.results = append(x.results, runToolResult{call: call, durableCall: x.durableToolCalls[index], args: args, argErr: err})
		}
	})
	r.stage(x.s, x.runID, x.turn, "execute", func() {
		// 1 means the window is unknown and nothing is clamped;
		// 0 means no room is left, which clamps every later window read.
		remainingResultTokens := -1
		if x.budget.NCtx > 0 {
			remainingResultTokens = max(0, x.budget.NCtx-x.budget.Reserve-x.budget.UsedEst-toolResultContextMargin)
		}
		x.runRawTools()
		for index := range x.results {
			item := &x.results[index]
			if item.argErr == nil {
				if item.ok && item.call.Name == "read_file" && (untrustedAttachmentRead(x.s, item.args) || savedToolResultRead(item.args)) {
					item.untrusted = true
				}
				if item.ok {
					fileMetadata := producedFileMetadata(x.s, item.call.Name, item.args)
					item.metadata = mergeResultMetadata(item.metadata, fileMetadata)
					item.metadata = mergeResultMetadata(item.metadata, browserScreenshotMetadata(x.s, item.call.Name, item.content))
					if file, ok := item.metadata["file"].(map[string]any); ok {
						path, _ := file["path"].(string)
						bytes, _ := file["bytes"].(int64)
						x.produced[strings.ToLower(path)] = delivery.Source{Path: path, Bytes: bytes}
					}
					if files, ok := item.metadata["files"].([]any); ok {
						for _, value := range files {
							file, _ := value.(map[string]any)
							path, _ := file["path"].(string)
							bytes, _ := file["bytes"].(int64)
							if path != "" {
								x.produced[strings.ToLower(path)] = delivery.Source{Path: path, Bytes: bytes}
							}
						}
					}
				}
			}
			if x.ctx.Err() != nil {
				break
			}
			resultTokens := r.textTokens(x.ctx, x.connection, item.content)
			allowance := remainingResultTokens
			if allowance >= 0 {
				allowance /= len(x.results) - index
			}
			item.content, item.ok, item.metadata, resultTokens = r.fitWindowResult(
				x.ctx, x.s, x.connection, item.call.Name, item.args, item.content, item.ok, item.metadata, resultTokens, allowance, item.operatorContext,
			)
			if limit := x.connection.Capabilities.ObservedByteLimit; limit > 0 && len(item.content) > limit/4 {
				item.content, item.ok, item.metadata = r.byteCapResult(item.call.Name, item.args, item.content, item.ok, item.metadata, limit)
				resultTokens = r.textTokens(x.ctx, x.connection, item.content)
			}
			if _, batch := item.args["windows"]; item.call.Name == "read_file" && !batch {
				if tooLarge, _ := item.metadata["result_too_large"].(bool); tooLarge {
					if x.refusedTurn != x.turn {
						x.refusedTurn = x.turn
					}
				} else if item.ok {
					x.refusedTurn = -1
				}
			}
			if remainingResultTokens >= 0 {
				remainingResultTokens = max(0, remainingResultTokens-resultTokens)
			}
			data := toolResultEventData(x.turn, item.call.ID, item.call.Name, item.content, item.ok, item.operatorContext, item.untrusted, item.ms, resultTokens, item.metadata)
			x.s.IncrementToolCall(item.call.Name)
			r.bus.Publish(events.New(events.ToolResult, x.s.ID, x.runID, data))
		}
	})
	if x.ctx.Err() != nil {
		return x.stop(x.contextStop(x.turn, "tool execution canceled"))
	}
	r.stage(x.s, x.runID, x.turn, "append", func() {
		x.s.Append(x.assistant)
		r.bus.Publish(events.New(events.MessageAppended, x.s.ID, x.runID, map[string]any{"message": x.assistant}))
		for _, item := range x.results {
			if item.ok && item.call.Name == "read_file" && r.skills != nil {
				if path, ok := item.args["path"].(string); ok {
					r.skills.Read(path, x.s.AgentID)
				}
			}
			category := item.category
			if category == "" {
				category = "results"
			}
			if item.call.Name == "read_file" && item.category == "" {
				category = "files"
			}
			message, _ := r.makeMessage(x.ctx, x.connection, "tool", item.content, category, x.turn)
			message.ToolCallID = item.call.ID
			message.Name = item.call.Name
			message.OK = boolPointer(item.ok)
			if file, ok := item.metadata["file"].(map[string]any); ok && x.connection.NativeImageInput() {
				path, _ := file["path"].(string)
				bytes, _ := file["bytes"].(int64)
				if strings.EqualFold(filepath.Ext(path), ".png") {
					message.Attachments = []events.Attachment{{Path: path, Bytes: bytes}}
				}
			}
			x.s.Append(message)
			r.bus.Publish(events.New(events.MessageAppended, x.s.ID, x.runID, map[string]any{"message": message}))
			if item.ok && item.call.Name == "remember" && strings.HasPrefix(item.content, "ok: noted;") {
				r.appendHarnessLine(x.ctx, x.connection, x.s, x.runID, x.turn, "Memory was saved for new chats; this chat's system prompt remains unchanged.")
			}
		}
	})
	for _, item := range x.results {
		eventArgs := sanitizedToolArguments(item.call.Name, item.args)
		reason, detail, prior := x.guards.Observe(item.call.ID, item.call.Name, eventArgs, item.content, item.ok)
		if reason == "steer" {
			r.appendHarnessLine(x.ctx, x.connection, x.s, x.runID, x.turn, detail)
			r.bus.Publish(events.New(events.CycleDetected, x.s.ID, x.runID, map[string]any{"stage": "steer", "call_id": item.call.ID, "name": item.call.Name, "line": detail}))
			continue
		}
		if reason == "cycle" {
			r.bus.Publish(events.New(events.CycleDetected, x.s.ID, x.runID, map[string]any{"call_id": item.call.ID, "name": item.call.Name, "args": eventArgs, "prior_call_id": prior}))
			decision, err := r.gate.WaitCycleDecision(x.ctx, x.s, x.runID, item.call.ID+"-cycle", map[string]any{"tool": item.call.Name, "detail": detail})
			if err != nil {
				if x.ctx.Err() != nil {
					return x.stop(x.contextStop(x.turn, "approval wait canceled"))
				}
				return x.stop("cycle", err.Error(), x.turn)
			}
			if decision == "continue" {
				if r.gate.noticeOnly(x.s) {
					r.appendHarnessLine(x.ctx, x.connection, x.s, x.runID, x.turn, cycleNotice(x.turn))
				}
				x.guards.ResetCycle()
				continue
			}
			return x.stop("cycle", detail, x.turn)
		}
		if reason != "" {
			return x.stop(reason, detail, x.turn)
		}
	}
	return runGoOnOutcome
}
func (x *runState) endTurn() runOutcome {
	r := x.r
	r.stage(x.s, x.runID, x.turn, "compact", func() {
		// after a refused window the elide runs even on a cold
		// prefill, so a second refusal means nothing elidable was left.
	})
	return runGoOnOutcome
}
func (x *runState) cleanupFinalBudget() {
	if !x.budgetReady || !x.publishFinalBudget {
		return
	}
	if x.ctx.Err() == nil {
		x.r.PublishBudget(x.ctx, x.s)
		return
	}
	go func() {
		closing, cancel := context.WithTimeout(context.WithoutCancel(x.ctx), 30*time.Second)
		defer cancel()
		x.r.PublishBudget(closing, x.s)
	}()
}
func (x *runState) cleanupFileGrant() {
	if x.fileReady {
		x.r.lapseFileRunGrant(x.s, x.runID)
	}
}
func (x *runState) cleanupShellGrants() {
	if x.shellReady {
		x.r.lapseShellGrants(x.s, x.runID)
	}
}
func (x *runState) cleanupDelivery(reason, detail *string, turns int) {
	if !x.deliveryReady {
		return
	}
	for key, source := range workspaceFileChanges(x.s.Workspace, x.workspaceBefore) {
		x.produced[key] = source
	}
	x.r.stage(x.s, x.runID, turns, "append", func() {
		if x.r.deliver != nil {
			x.r.deliver(x.s, x.runID, delivery.SortedSources(x.produced))
		}
	})
}
func (x *runState) cleanupWall() {
	if x.wallReady {
		x.wallTimer.Stop()
		x.cancelWall()
	}
}
func (x *runState) cleanupFlight() {
	if x.flightReady {
		x.r.endFlight(x.s.ID, x.runID)
	}
}
func (x *runState) cleanupPin() {
	if x.pinReady {
		x.s.SetRunPin("")
	}
}
