package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"harness/internal/credential"
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
	modelAnswered      func(string)
	ids                atomic.Int64
	nameAttempts       sync.Map
	windows            sync.Map
	identityInvitation atomic.Bool
	trustFolders       func([]string) error
	skills             SkillHost
	takeQueued         func(*session.Session, string)
	returnMessage      func(*session.Session, string) bool
	modelRetryDelays   []time.Duration
}

var resolveNamedPath = tools.ResolveNamedPath

type SkillHost interface {
	Index(string) string
	Proposal(context.Context, *session.Session, string) (string, error)
	Read(string, string)
}

type BoundaryAction struct {
	Stop     bool
	Revision string
	Delay    time.Duration
	Err      error
}

const minimumOutputFloor = 4096

func NewRunner(bus *events.Bus, registry *tools.Registry, prompt *PromptRenderer, connection func(string) (*config.Connection, bool), cfg func() config.Config) *Runner {
	if prompt != nil {
		prompt.SetTrustedFolders(func() []config.TrustedFolder { return cfg().Shell.TrustedFolders })
	}
	return &Runner{bus: bus, tools: registry, prompt: prompt, connection: connection, cfg: cfg, gate: NewGate(bus, cfg), budget: NewBudgeter(), compact: contextmgr.New(bus), flights: newFlightBook(), modelRetryDelays: []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}}
}
func (r *Runner) Configure(cfg config.Config) {
	r.tools.Configure(cfg)
	r.budget.InvalidateToolCosts()
}

func (r *Runner) refreshAgent(s *session.Session) (*config.Connection, bool) {
	snapshot := s.Snapshot()
	connection, ok := r.connection(snapshot.ConnectionID)
	if !ok {
		return nil, false
	}
	cfg := r.cfg()
	agentID := snapshot.AgentID
	agent, ok := cfg.Agent(agentID)
	if snapshot.Role == "" || snapshot.Role == "b" {
		agent, ok = cfg.ProfileForConnection(snapshot.ConnectionID)
		if snapshot.AgentID == "" || !ok {
			// Direct runner fixtures and released callers can provide a connection
			// without a complete Config. Routing remains connection-authoritative;
			// only profile refresh is unavailable in that compatibility case.
			return connection, true
		}
		agentID = config.AgentID(agent.Name)
	} else if !ok {
		return nil, false
	}
	changedProfile := snapshot.AgentID != "" && snapshot.AgentID != agentID
	s.ApplyAgentConfig(agentID, *agent, *connection)
	if changedProfile && agentID == "agent_b" {
		r.bus.Publish(events.New(events.SessionUpdated, snapshot.ID, "", map[string]any{"session_id": snapshot.ID, "agent_id": agentID, "connection_id": snapshot.ConnectionID, "notice": "Agent profile deleted; using agent_b."}))
	}
	return connection, true
}

func (r *Runner) currentConnection(s *session.Session) (*config.Connection, bool) {
	return r.connection(s.Snapshot().ConnectionID)
}

func connectionWithProfileThinking(connection config.Connection, profile config.Agent) config.Connection {
	control := connection.Reasoning.Control
	if control == "auto" {
		control = connection.Capabilities.ReasoningControl
	}
	connection.Reasoning.Enabled = profile.Thinking && control != "" && control != "none"
	return connection
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
func (r *Runner) SetModelUnreachable(fn func(string, string))             { r.modelUnreachable = fn }
func (r *Runner) SetModelAnswered(fn func(string))                        { r.modelAnswered = fn }
func (r *Runner) SetTrustedFolderWriter(fn func([]string) error)          { r.trustFolders = fn }
func (r *Runner) SetSkillHost(host SkillHost)                             { r.skills = host }
func (r *Runner) SetQueuedBoundary(fn func(*session.Session, string))     { r.takeQueued = fn }
func (r *Runner) SetMessageReturn(fn func(*session.Session, string) bool) { r.returnMessage = fn }
func (r *Runner) BindDelegate(tool *tools.Delegate)                       { tool.SetRunner(r.runDelegate) }
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
	p, ok := r.currentConnection(s)
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
	parentRoom := max(0, parentState.Budget.NCtx-guardedPromptTokens(parentState.Budget))
	child := &session.Session{
		ID: parent.ID + "-delegate-" + r.id("e"), Label: "delegate", Role: "e", AgentID: parentState.AgentID,
		ParentSessionID: parent.ID,
		ConnectionID:    parentState.ConnectionID, Workspace: parentState.WorkspaceDir, PlanID: parentState.PlanID,
		PlanDir: parent.PlanDir, PlanRepo: parent.PlanRepo, PlansRoot: parent.PlansRoot, SkillsRoot: parent.SkillsRoot, SkillStateRoot: parent.SkillStateRoot, PlanRepos: parent.PlanRepos,
		NetworkBoundary: parentState.NetworkBoundary, NetworkBoundarySet: true,
		MediaCapabilities: parentState.MediaCapabilities, MediaCapabilitiesSet: true, Runnable: true,
		Run: session.RunState{Status: "idle", MaxTurns: turns}, ToolsEnabled: map[string]bool{
			"read_file": true, "list_dir": true, "search": true, "fetch_url": true, "recall": true,
		}, ToolCalls: map[string]int{}, LastSeen: map[string]time.Time{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{},
	}
	r.bus.Publish(events.New(events.DelegatedUsage, parent.ID, "", map[string]any{"child_id": child.ID, "status": "running"}))
	defer r.bus.Publish(events.New(events.DelegatedUsage, parent.ID, "", map[string]any{"child_id": child.ID, "status": "completed"}))
	connection, ok := r.currentConnection(child)
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
	if partial {
		instruction := "Tool access is now withdrawn. Return the best concise evidence-based summary from what you found; do not request or call another tool."
		if parentRoom > 0 {
			instruction += fmt.Sprintf(" Keep the answer within %d tokens and end on a complete sentence.", parentRoom)
		}
		message, _ := r.makeMessage(ctx, connection, "user", instruction, "history", 0)
		child.Append(message)
		child.DisableTools()
		child.SetRun(session.RunState{Status: "idle", MaxTurns: 1})
		finalReason, finalDetail, _ := r.Run(ctx, child, r.id("delegate-summary"))
		if finalReason != "done" {
			detail = strings.TrimSpace(detail + "; final summary: " + finalReason + ": " + finalDetail)
		}
	}
	transcript := child.MessagesCopy()
	summary := lastAssistantSummary(transcript)
	if summary == "" {
		summary = "No summary was produced before the child stopped."
		partial = true
	}
	if tokens, _ := r.count(ctx, connection, summary); !partial && parentRoom > 0 && tokens > parentRoom {
		message, _ := r.makeMessage(ctx, connection, "user", fmt.Sprintf("Tool access is now withdrawn. Rewrite your final answer within %d tokens and end on a complete sentence; do not request or call another tool.", parentRoom), "history", 0)
		child.Append(message)
		child.DisableTools()
		child.SetRun(session.RunState{Status: "idle", MaxTurns: 1})
		if finalReason, finalDetail, _ := r.Run(ctx, child, r.id("delegate-fit")); finalReason != "done" {
			return tools.DelegateResult{}, fmt.Errorf("delegate shorten stopped: %s: %s", finalReason, finalDetail)
		}
		transcript = child.MessagesCopy()
		summary = "[helper answer shortened to fit the parent context]\n" + lastAssistantSummary(transcript)
		partial = true
	}
	toolCalls := 0
	for _, tool := range child.Snapshot().Tools {
		toolCalls += tool.Calls
	}
	return tools.DelegateResult{Summary: summary, Partial: partial, DurationMS: time.Since(started).Milliseconds(), ToolCalls: toolCalls, Transcript: transcript}, nil
}

func lastAssistantSummary(transcript []events.Message) string {
	for index := len(transcript) - 1; index >= 0; index-- {
		if transcript[index].Role == "assistant" && strings.TrimSpace(transcript[index].Content) != "" {
			return strings.TrimSpace(transcript[index].Content)
		}
	}
	return ""
}

// ReserveIDs moves the id counter past floor, so ids minted after a restart
// never repeat one a restored chat already holds.
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
	connection, ok := r.refreshAgent(s)
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
	// the chat takes its name from this message if it has none yet.
	// Once, from the user's own words, and never again by the harness.
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
	return r.runInPhases(ctx, s, runID)
}

var messageLimitPattern = regexp.MustCompile(`(?i)(conversation too long:\s*\d+ messages\s*\(limit\s*(\d+)\)?|[^\r\n\"]*message[^\r\n\"]*limit[^\r\n\"]*)`)

// the status the refusal arrives with is not the test. A server that
// refuses an over-large request with 500 or 413 says the same thing, and gating on
// HTTP 400 first meant the connection learned nothing, compacted nothing and
// retried nothing. The pattern remains the whole test, so an ordinary 400 or 500
// with unrelated text is handled exactly as before.
func messageLimitError(err error) (int, string, bool) {
	if err == nil {
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

// the byte cap a server names in its refusal. Two currencies appear in
// the user's own evidence -- `prompt too large: 401628 bytes (limit 400000)`
// and `conversation too long: 61 messages (limit 60)` -- and this reads the first.
var byteLimitPattern = regexp.MustCompile(`(?i)([^\r\n"]*?(?:too large|too long|exceeds)[^\r\n"]*?\d+\s*bytes[^\r\n"]*?\(limit\s*(\d+)\))`)

func byteLimitError(err error) (int, string, bool) {
	if err == nil {
		return 0, "", false
	}
	match := byteLimitPattern.FindStringSubmatch(err.Error())
	if len(match) < 3 {
		return 0, "", false
	}
	limit := 0
	fmt.Sscanf(match[2], "%d", &limit)
	if limit <= 1 {
		return 0, "", false
	}
	return limit, strings.TrimSpace(strings.Trim(match[1], `\"`)), true
}

type workspaceFileState struct {
	Path    string
	Size    int64
	ModTime int64
}

func workspaceFileSnapshot(root string) map[string]workspaceFileState {
	out := map[string]workspaceFileState{}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.IsDir() && path != root && strings.EqualFold(filepath.Base(path), savedToolResultDir) {
			return filepath.SkipDir
		}
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
	// a failed tool result carries a CLASS from a closed
	// vocabulary, so telemetry has something to send that is not free text. A
	// successful call has no class and the field is absent, not empty.
	if !ok {
		data["class"] = ToolErrorClass(content)
	}
	for key, value := range metadata {
		data[key] = value
	}
	return data
}

func (r *Runner) executeTool(ctx context.Context, s *session.Session, runID, callID, name string, args map[string]any) tools.CallOutcome {
	processStart := name == "process" && args["action"] == "start"
	shellStart := name == "shell" || processStart
	if name == "shell" || name == "run_script" {
		lastPublished := time.Time{}
		var progressMu sync.Mutex
		ctx = tools.WithOutputObserver(ctx, func(line string) {
			progressMu.Lock()
			defer progressMu.Unlock()
			if time.Since(lastPublished) < 250*time.Millisecond {
				return
			}
			lastPublished = time.Now()
			r.bus.PublishTransient(events.New(events.ToolProgress, s.ID, runID, map[string]any{"name": name, "line": line}))
			if s.ParentSessionID != "" {
				r.bus.PublishTransient(events.New(events.DelegatedUsage, s.ParentSessionID, runID, map[string]any{"child_id": s.ID, "status": "running", "tool": name, "target": shortActivityTarget(args), "turn": s.Snapshot().Run.Turn, "live": true}))
			}
		})
	}
	if name == "remember" && rememberEchoesPreviousTool(s, args) {
		return tools.CallOutcome{Content: "error: remember refused: this restates the immediately preceding tool result; the chat already records it"}
	}
	if path, ok := args["path"].(string); ok {
		if resolved := resolveNamedPath(path); resolved != path {
			args = cloneMetadata(args)
			args["path"] = resolved
		}
	}
	cfg := r.cfg()
	eventArgs := sanitizedToolArguments(name, args)
	if name == "call_service" {
		eventArgs["approval_action"] = callServiceApprovalAction(args)
	}
	path, _ := args["path"].(string)
	skillRead := (name == "read_file" || name == "list_dir" || name == "search" || name == "search_text" || name == "find_files") && s.IsSkillPath(path)
	if cfg.Shell.ServiceAccount.Enabled && !cfg.Shell.OperatorContext {
		if err := r.tools.PreflightServiceIdentity(); err != nil {
			if s.Role == "e" {
				return r.tools.CallDetailed(ctx, s, name, args)
			}
			switch name {
			case "read_file", "list_dir", "search", "search_text", "find_files":
				return r.callFileAsOperator(ctx, s, name, args)
			case "write_file", "edit_file", "shell", "process", "run_script", "call_service":
				if name == "process" && !processStart {
					break
				}
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
		if shellStart || name == "run_script" {
			return r.callShellAsOperator(ctx, s, name, args)
		}
	}
	if fileGrantTool(name) && cfg.Shell.ServiceAccount.Enabled && !cfg.Shell.OperatorContext && r.hasFileGrant(s.ID, runID) {
		return r.callFileAsOperator(ctx, s, name, args)
	}
	if shellStart && cfg.Shell.ServiceAccount.Enabled && !cfg.Shell.OperatorContext {
		if r.hasShellGrant(s.ID, runID, shellGrantBoundary, "") {
			return r.callShellAsOperator(ctx, s, name, args)
		}
		if command, matched := r.tools.OperatorCommandWith(name, args, s.Policy().Shell.OperatorCommandsAdd); matched {
			return r.executeOperatorCommand(ctx, s, runID, callID, name, args, command)
		}
	}
	if shellStart && repoRunGrantMatches(s.Policy().Shell.RunGrantDefaults, args) && !r.hasShellGrant(s.ID, runID, shellGrantPolicy, "") {
		identity := "operator"
		if cfg.Shell.ServiceAccount.Enabled {
			identity = "service"
		}
		r.grantShellRun(s, runID, shellRunGrant{Rule: shellGrantPolicy, Identity: identity})
	}
	if name == "ask" {
		question, choices, err := r.tools.Question(s, name, args)
		if err != nil {
			return tools.CallOutcome{Content: "error: " + err.Error()}
		}
		answer, err := r.gate.WaitQuestion(ctx, s, runID, callID, question, choices)
		if err != nil {
			return tools.CallOutcome{Content: "error: question canceled"}
		}
		return tools.CallOutcome{Content: answer, OK: true}
	}
	decision := "approve"
	var gateErr error
	proposedConnector, connectorChange, connectorErr := tools.ParseConnectorChange(args)
	if connectorChange {
		if connectorErr != nil {
			eventArgs["validation_error"] = connectorErr.Error()
		}
		// a proposal that BINDS A CREDENTIAL, or that moves a bound
		// connector to another address, says so on this card in those words — the
		// credential's name and the exact origin it would be sent to. Nothing binds or
		// moves silently, and the card is the existing one.
		if connectorErr == nil && (strings.HasPrefix(strings.TrimSpace(proposedConnector.Service.Auth), "stored:") || strings.HasPrefix(strings.TrimSpace(proposedConnector.Service.Auth), "entra:")) {
			auth := strings.TrimSpace(proposedConnector.Service.Auth)
			kind := "stored"
			if strings.HasPrefix(auth, "entra:") {
				kind = "entra"
			}
			eventArgs["connector_credential"] = strings.TrimSpace(strings.TrimPrefix(auth, kind+":"))
			eventArgs["connector_credential_kind"] = kind
			if proposedConnector.Entra != nil {
				eventArgs["connector_entra_tenant"] = proposedConnector.Entra.Tenant
				eventArgs["connector_entra_client_id"] = proposedConnector.Entra.ClientID
				eventArgs["connector_entra_scopes"] = proposedConnector.Entra.Scopes
			}
			if origin, originErr := credential.NormalizeOrigin(proposedConnector.Service.BaseURL); originErr == nil {
				eventArgs["connector_origin"] = origin
				if existing, present := cfg.Services[proposedConnector.Name]; present {
					if previous, previousErr := credential.NormalizeOrigin(existing.BaseURL); previousErr == nil && previous != origin {
						eventArgs["connector_previous_origin"] = previous
					}
				}
			} else {
				eventArgs["validation_error"] = originErr.Error()
			}
		}
		// a proposal that names an OpenAPI document is shown on THIS card,
		// with the operations it would enable and what the document says its own auth
		// wants. The fetch happens once and is read again when the operator approves.
		if connectorErr == nil && proposedConnector.Service.OpenAPI != nil {
			_, document, loadErr := tools.LoadServiceDocument(ctx, proposedConnector.Service.OpenAPI.Source)
			if loadErr != nil {
				eventArgs["validation_error"] = loadErr.Error()
			} else if enabled, enabledErr := document.Enabled(proposedConnector.Service.OpenAPI.Operations); enabledErr != nil {
				eventArgs["validation_error"] = enabledErr.Error()
			} else {
				lines := make([]string, 0, len(enabled))
				for _, operation := range enabled {
					lines = append(lines, operation.Line())
				}
				eventArgs["connector_operations"] = lines
				eventArgs["connector_document_auth"] = document.Auth
			}
		}
		decision, gateErr = r.gate.WaitPolicyDecision(ctx, s, runID, callID, name, eventArgs)
	} else if name == "run_script" && cfg.Shell.ServiceAccount.Enabled && !r.hasPolicyChatGrant(s.ID, name) {
		decision, gateErr = r.gate.WaitPolicyDecision(ctx, s, runID, callID, name, eventArgs)
	} else if shellStart && r.hasShellGrant(s.ID, runID, shellGrantPolicy, "") {
		// An operator-approved repository default grants this displayed command
		// pattern for the run under the already-configured identity.
	} else if shellStart && cfg.Shell.ServiceAccount.Enabled && !cfg.Shell.OperatorContext && r.gate.requiredFor(s, "shell") {
		if !r.hasShellGrant(s.ID, runID, shellGrantPolicy, "") {
			decision, gateErr = r.gate.WaitPolicyDecision(ctx, s, runID, callID, name, eventArgs)
		}
	} else if name == "process" && !processStart && r.gate.modeFor(s) != config.ApprovalModeAll {
		// Managing a process already approved for this run is not a new execution.
	} else if !r.hasPolicyChatGrant(s.ID, name) && r.tools.PolicyApprovalRequired(name, r.gate.modeFor(s), args) {
		decision, gateErr = r.gate.WaitPolicyDecision(ctx, s, runID, callID, name, eventArgs)
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
	if shellStart && decision == "run" {
		r.grantShellRun(s, runID, shellRunGrant{Rule: shellGrantPolicy, Identity: "service"})
	}
	if shellStart && decision == "session" {
		r.grantShellSession(s, runID, shellRunGrant{Rule: shellGrantPolicy, Identity: "service"})
	}
	if decision == "session" && !connectorChange {
		r.grantPolicyChat(s.ID, name)
	}
	if shellStart && repoRunGrantMatches(s.Policy().Shell.RunGrantDefaults, args) {
		args = resolveRepoGrantedExecutable(cfg, args)
	}
	var outcome tools.CallOutcome
	if skillRead {
		outcome = r.callFileAsOperator(ctx, s, name, args)
	} else {
		outcome = r.callDetailed(ctx, s, name, args)
	}
	if !outcome.OperatorOverrideAvailable {
		return outcome
	}
	command, _ := args["command"].(string)
	subject := "tool call"
	scope := "rerun this exact tool call once"
	if shellStart || name == "run_script" {
		subject = "command"
		scope = "rerun this exact command once"
	}
	overrideID := callID + ":operator"
	overrideArgs := map[string]any{
		"identity": "Agent_b user (not Administrator)",
		"reason":   outcome.OperatorOverrideReason,
		"scope":    scope,
	}
	if command != "" {
		overrideArgs["command"] = command
	}
	if path != "" {
		overrideArgs["path"] = path
	}
	outsideFolders, _ := outcome.Metadata["outside_folders"].([]string)
	if len(outsideFolders) > 0 {
		overrideArgs["outside_folders"] = outsideFolders
	}
	outsideCard, _ := outcome.Metadata["outside_folder_card"].(bool)
	if outsideCard {
		overrideArgs["outside_folder_card"] = true
	}
	// cold review: run_script has no command or path argument, so
	// its card showed nothing of what would run as the operator.
	if source, _ := args["source"].(string); name == "run_script" && source != "" {
		overrideArgs["source"] = source
		if language, _ := args["language"].(string); language != "" {
			overrideArgs["language"] = language
		}
	}
	// With no service identity, the outside-folder card: its "Yes,
	// for this chat" holds for this chat's outside-folder calls in this posture.
	// The identity grants below apply only with the service identity on, so
	// storing them here left a grant that woke up if the posture changed. The
	// sandbox card keeps its own handling.
	overrideDecision := "deny"
	var overrideErr error
	if outsideCard && r.hasPolicyChatGrant(s.ID, outsideFolderChatGrant) {
		overrideDecision = "once"
	} else {
		overrideDecision, overrideErr = r.gate.WaitBoundaryDecision(ctx, s, runID, overrideID, name+".operator_override", overrideArgs)
	}
	if overrideErr != nil {
		outcome.OK, outcome.OperatorContext = false, false
		outcome.Metadata = withHarnessNote(outcome.Metadata, "user-identity override canceled")
		outcome.Content = withModelNote(outcome.Content, "user-identity override canceled")
		return outcome
	}
	if !approvalGranted(overrideDecision) {
		log.Printf("%s operator-identity override denied: session=%s call=%s command=%q path=%q", name, s.ID, callID, command, path)
		outcome.OK, outcome.OperatorContext = false, false
		outcome.Metadata = withHarnessNote(outcome.Metadata, "user-identity override was offered and denied by the user")
		outcome.Content = withModelNote(outcome.Content, "user-identity override was offered and denied by the user")
		return outcome
	}
	outsideScope := overrideDecision
	if len(outsideFolders) > 0 {
		// a yes is acted on. A folder that cannot be kept as
		// trusted still runs this once; the note says it was not kept.
		if outsideScope == "folder" && (r.trustFolders == nil || r.trustFolders(outsideFolders) != nil) {
			outsideScope = "folder not kept"
		}
		if outsideCard && overrideDecision == "session" {
			r.grantPolicyChat(s.ID, outsideFolderChatGrant)
		}
		if outsideCard || strings.HasPrefix(outsideScope, "folder") {
			overrideDecision = "once"
		}
	}
	if shellStart && overrideDecision == "run" {
		r.grantShellRun(s, runID, shellRunGrant{Rule: shellGrantBoundary, Identity: "operator"})
	}
	if shellStart && overrideDecision == "session" {
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
		note := "user-identity override succeeded; exact " + subject + " rerun once"
		if outsideCard || strings.HasPrefix(outsideScope, "folder") {
			scope := map[string]string{"approve": "just once", "once": "just once", "session": "for this chat", "folder": "folder trusted", "folder not kept": "just once; the folder could not be kept as trusted"}[outsideScope]
			note = "outside-folder access allowed (" + scope + ")"
		}
		outcome.Metadata = withHarnessNote(mergeResultMetadata(outcome.Metadata, sandboxResultMetadata(cfg, s, name, args)), note)
		return outcome
	}
	// A shell or script that started as the operator and returned a non-zero
	// exit is an execution failure, not a failed identity override. Preserve the
	// interpreter's own error without teaching the model that the retry itself
	// could not be made. Launch/identity failures do not have this shape and keep
	// the harness note below.
	if (shellStart || name == "run_script") && strings.HasPrefix(overrideContent, "error: command failed\nexit=") {
		outcome.Content, outcome.OK, outcome.OperatorContext = overrideContent, false, true
		return outcome
	}
	outcome.Content, outcome.OK, outcome.OperatorContext = overrideContent, false, true
	outcome.Metadata = withHarnessNote(outcome.Metadata, "user-identity override was attempted but failed")
	outcome.Content = withModelNote(outcome.Content, "user-identity override was attempted but failed")
	return outcome
}

func callServiceApprovalAction(args map[string]any) string {
	service, _ := args["service"].(string)
	method, _ := args["method"].(string)
	path, _ := args["path"].(string)
	if operation, _ := args["operation"].(string); strings.TrimSpace(operation) != "" {
		return fmt.Sprintf("Service %s operation %s", service, operation)
	}
	return fmt.Sprintf("Service %s %s %s", service, strings.ToUpper(method), path)
}

func rememberEchoesPreviousTool(s *session.Session, args map[string]any) bool {
	note, _ := args["note"].(string)
	messages := s.MessagesCopy()
	if strings.TrimSpace(note) == "" || len(messages) == 0 || messages[len(messages)-1].Role != "tool" {
		return false
	}
	words := func(value string) map[string]bool {
		out := map[string]bool{}
		for _, word := range regexp.MustCompile(`[\pL\pN]+`).FindAllString(strings.ToLower(value), -1) {
			if len([]rune(word)) > 1 {
				out[word] = true
			}
		}
		return out
	}
	noteWords, resultWords := words(note), words(messages[len(messages)-1].Content)
	if len(noteWords) == 0 {
		return false
	}
	shared := 0
	for word := range noteWords {
		if resultWords[word] {
			shared++
		}
	}
	return strings.Contains(strings.ToLower(messages[len(messages)-1].Content), strings.ToLower(strings.TrimSpace(note))) || shared*5 >= len(noteWords)*3
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
// (cold review): a denied, canceled or failed override is an error,
// not file content, and a model that is not told retries and prompts again. A
// successful rerun's content stays exactly what the tool returned.
func withModelNote(content, note string) string {
	return content + "\n\n[harness: " + note + "]"
}

// withHarnessNote records what the harness did around a tool call as a result
// field, rendered as its own harness line. prepended to the content,
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
	if s.ParentSessionID != "" {
		r.bus.PublishTransient(events.New(events.DelegatedUsage, s.ParentSessionID, runID, map[string]any{"child_id": s.ID, "status": "running", "stage": name, "turn": turn, "live": true}))
	}
	fn()
	r.bus.Publish(events.New(events.Stage, s.ID, runID, map[string]any{"stage": name, "state": "exit", "turn": turn, "ms": time.Since(start).Milliseconds()}))
}
func shortActivityTarget(args map[string]any) string {
	for _, key := range []string{"path", "pattern", "command", "service", "task"} {
		if value, _ := args[key].(string); strings.TrimSpace(value) != "" {
			value = strings.Join(strings.Fields(value), " ")
			if len(value) > 80 {
				value = value[:77] + "…"
			}
			return value
		}
	}
	return ""
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
	// a canceled request says nothing about the model.
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

func watchModelLoading(ctx context.Context, client *llm.Client, model string, every time.Duration, report func(bool)) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		raw, status, err := client.DoJSON(ctx, "GET", "/v1/models", nil)
		var listed struct {
			Data []struct {
				ID     string `json:"id"`
				Status struct {
					Value string `json:"value"`
				} `json:"status"`
			} `json:"data"`
		}
		if err == nil && status == 200 && json.Unmarshal(raw, &listed) == nil {
			for _, item := range listed.Data {
				if item.ID == model {
					report(item.Status.Value == "loading")
					break
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func watchConnectionModelLoading(ctx context.Context, client *llm.Client, connection *config.Connection, every time.Duration, report func(bool)) {
	if connection.Capabilities.Server == "llama.cpp" {
		watchModelLoading(ctx, client, connection.Model, every, report)
	}
}

func (r *Runner) textTokens(ctx context.Context, p *config.Connection, text string) int {
	value, _ := r.count(ctx, p, text)
	return value
}
func (r *Runner) PublishBudget(ctx context.Context, s *session.Session) {
	p, ok := r.currentConnection(s)
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
	base := r.prompt.RenderSkillParts(p, s, toolNames, "", "")
	project := r.prompt.RenderSkillParts(p, s, toolNames, s.ProjectBlock, "")
	skillsBlock := ""
	if r.skills != nil {
		skillsBlock = r.skills.Index(s.AgentID)
	}
	skillsSystem := r.prompt.RenderSkillParts(p, s, toolNames, s.ProjectBlock, skillsBlock)
	memoryBlock := s.WorkspaceMemory()
	workspaceMemory := r.prompt.RenderSkillParts(p, s, toolNames, s.ProjectBlock, skillsBlock, memoryBlock, "")
	system := r.prompt.RenderSkillParts(p, s, toolNames, s.ProjectBlock, skillsBlock, memoryBlock, s.AgentMemoryBlock, s.MachineMemoryBlock)
	// As the run's own assembly does, harness abort records lead the list: a
	// system-role record mid-history is refused by chat templates.
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
	return r.budget.Measure(ctx, p, s, r.cfg().Context, budgetInput{SystemBase: base, SystemProject: project, SystemSkills: skillsSystem, SystemWorkspaceMemory: workspaceMemory, System: system, WithoutToolSystems: r.withoutToolSystems(p, s, enabled, memoryBlock), Schemas: schemas, AllSchemas: r.tools.AllSchemas(), Messages: messages, Records: records}, mark)
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

func (r *Runner) operationalError(s *session.Session, runID, where string, err error) {
	r.bus.Publish(events.New(events.Error, s.ID, runID, map[string]any{"where": where, "message": err.Error()}))
}

// connectionKind is /2q6's provider word: local, or somebody's api.
func connectionKind(connection *config.Connection) string {
	if isLoopbackEndpoint(connection.BaseURL) {
		return "local"
	}
	return "api"
}

var (
	refusalStatus = regexp.MustCompile(`HTTP (\d{3})`)
	refusalType   = regexp.MustCompile(`"type"\s*:\s*"([A-Za-z_]{1,48})"`)
)

// serverRefusal reads an HTTP refusal's status and the server's error type
// word out of a model call's error; zero when the error was not a refusal.
func serverRefusal(err error) (int, string) {
	text := err.Error()
	match := refusalStatus.FindStringSubmatch(text)
	if match == nil {
		return 0, ""
	}
	status := 0
	fmt.Sscanf(match[1], "%d", &status)
	kind := ""
	if typed := refusalType.FindStringSubmatch(text); typed != nil {
		kind = typed[1]
	}
	return status, kind
}

func retryableModelCall(err error) bool {
	if llm.TransportKindOf(err) != "" {
		return true
	}
	status, _ := serverRefusal(err)
	return status == 502 || status == 503 || status == 504
}

// shortHash names a text without carrying it: eight hex characters.
func shortHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:4])
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

var windowLimitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)exceeds the available context size \((\d+) tokens\)`),
	regexp.MustCompile(`(?i)maximum context length is (\d+) tokens`),
}

func windowLimitError(err error) (int, bool) {
	for _, pattern := range windowLimitPatterns {
		match := pattern.FindStringSubmatch(fmt.Sprint(err))
		window := 0
		if len(match) == 2 {
			fmt.Sscanf(match[1], "%d", &window)
		}
		if window > 0 {
			return window, true
		}
	}
	return 0, false
}

func tokenWindowRefusal(err error) bool {
	text := strings.ToLower(fmt.Sprint(err))
	return strings.Contains(text, "context") && (strings.Contains(text, "window") || strings.Contains(text, "context length") || strings.Contains(text, "context size")) &&
		(strings.Contains(text, "exceed") || strings.Contains(text, "too long") || strings.Contains(text, "maximum"))
}

// SetServerWindow records the window a server gives one request, from its
// health check or a refusal; zero forgets it.
func (r *Runner) SetServerWindow(connectionID string, window int) {
	if window <= 0 {
		r.windows.Delete(connectionID)
		return
	}
	r.windows.Store(connectionID, window)
}

// resolveWindow is resolveContextWindow, narrowed to the server's own window
// when it gives a request less than the connection is set to.
func (r *Runner) resolveWindow(ctx context.Context, connection *config.Connection) (*config.Connection, string, error) {
	resolved, source, err := resolveContextWindow(ctx, connection)
	if connection.Context.NCtx > 0 {
		return resolved, source, err
	}
	value, known := r.windows.Load(connection.ID)
	if err != nil || !known || value.(int) >= resolved.Context.NCtx {
		return resolved, source, err
	}
	narrowed := *resolved
	narrowed.Context.NCtx = value.(int)
	narrowed.Context.ReserveOutput = min(narrowed.Context.ReserveOutput, narrowed.Context.NCtx/2)
	return &narrowed, fmt.Sprintf("the server allows %d tokens per request", narrowed.Context.NCtx), nil
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
	if budget.Mode == "estimated" && budget.UsedMeasured <= 0 {
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

// truncationDetail names the cap the answer hit, its value and where it is set,
// and distinguishes thinking having used the budget from the answer being cut
// short. "The run stopped because of length" told the
// operator nothing, least of all that the limit was his own setting.
func truncationDetail(connection *config.Connection, response llm.Response) string {
	reserve := connection.Context.ReserveOutput
	if reserve <= 0 {
		reserve = config.DefaultReserveOutput
	}
	where := fmt.Sprintf("the %s connection's output reserve (Settings → Connections → reserve output, context.reserve_output = %d tokens)", connection.Label, reserve)
	// Thinking used the budget when the model produced reasoning and no answer at
	// all — the shape of the user's own case — rather than a token comparison
	// this function has no numbers for.
	if connection.Reasoning.Enabled && connection.Reasoning.MaxTokens == 0 && strings.TrimSpace(response.Content) == "" && strings.TrimSpace(response.Reasoning) != "" {
		return fmt.Sprintf("thinking used the output allowance of %d tokens and no answer was left; thinking now gets its own share of %d tokens. The limit is %s", reserve, config.ReasoningShareFor(reserve), where)
	}
	return fmt.Sprintf("the answer reached the output limit of %d tokens and was cut short; what it produced is kept above, marked truncated. The limit is %s", reserve, where)
}

// keepTruncatedAnswer appends what the model did produce, marked truncated, so
// the stop card's existing Continue action has something to continue from.
func (r *Runner) keepTruncatedAnswer(ctx context.Context, s *session.Session, runID string, connection *config.Connection, response llm.Response, turn int, currentReasoning map[string]bool) {
	kept := strings.TrimSpace(response.Content)
	if kept == "" {
		kept = strings.TrimSpace(response.Reasoning)
		if kept == "" {
			return
		}
		kept += "\n\n[answer taken from the model's reasoning]"
	}
	kept += "\n\n[the answer reached the output limit and was cut short here]"
	r.stage(s, runID, turn, "append", func() {
		message, _ := r.makeMessage(ctx, connection, "assistant", kept, "history", turn)
		message.Reasoning = response.Reasoning
		currentReasoning[message.ID] = true
		s.Append(message)
		r.bus.Publish(events.New(events.MessageAppended, s.ID, runID, map[string]any{"message": message, "truncated": true}))
	})
}

// answerCeiling is the wall-clock bound on ONE answer ). Zero, the
// value every existing configuration carries, means no ceiling.
func answerCeiling(connection *config.Connection) time.Duration {
	if connection.Context.AnswerCeilingSeconds <= 0 {
		return 0
	}
	return time.Duration(connection.Context.AnswerCeilingSeconds) * time.Second
}

// answerCeilingDetail is the same treatment as a token truncation: it names the
// ceiling, its value and where to change it, and the output already produced is
// kept.
func answerCeilingDetail(connection *config.Connection, elapsed time.Duration) string {
	return fmt.Sprintf("the answer ran past the %d-second ceiling for one answer (%.0f seconds elapsed) and was stopped; what it produced is kept above, marked truncated. The ceiling is the %s connection's context.answer_ceiling_seconds and is yours to change",
		connection.Context.AnswerCeilingSeconds, elapsed.Seconds(), connection.Label)
}

func cycleNotice(turn int) string {
	return fmt.Sprintf("stopped a loop after %d turns, continuing differently", turn)
}
