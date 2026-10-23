// Package projection folds durable session events into the versioned state served to clients.
// It has no runtime dependencies: a snapshot is a pure function of a prior snapshot and a
// durably located JSONL record.
package projection

import (
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"

	"harness/internal/events"
	workspaceinfo "harness/internal/workspace"
)

const SchemaVersion = 1

type Cursor struct {
	Generation string `json:"generation"`
	Offset     int64  `json:"offset"`
}

type Record struct {
	Cursor Cursor       `json:"cursor"`
	Event  events.Event `json:"event"`
}

type Run struct {
	Status         string `json:"status"`
	RunID          string `json:"run_id"`
	Turn           int    `json:"turn"`
	QueuePosition  int    `json:"queue_position"`
	Partial        string `json:"partial"`
	LastStopReason string `json:"last_stop_reason"`
	LastStopDetail string `json:"last_stop_detail,omitempty"`
	LastRunID      string `json:"last_run_id,omitempty"`
	ResultLabel    string `json:"result_label,omitempty"`
	// WaitingBehind names the role holding a queued run's model connection
	// for "waiting for model · behind <role>".
	WaitingBehind string `json:"waiting_behind,omitempty"`
}

type Tool struct {
	Name           string `json:"name"`
	Enabled        bool   `json:"enabled"`
	Calls          int    `json:"calls"`
	SchemaTokens   int    `json:"schema_tokens"`
	MarginalTokens int    `json:"marginal_tokens"`
}

type Activity struct {
	Stage            string            `json:"stage"`
	StageState       string            `json:"stage_state"`
	CompletedStages  []string          `json:"completed_stages"`
	ActiveTool       string            `json:"active_tool"`
	AlarmTool        string            `json:"alarm_tool"`
	DispatchAlarm    bool              `json:"dispatch_alarm"`
	Progress         map[string]any    `json:"progress,omitempty"`
	LastTimings      map[string]any    `json:"last_timings,omitempty"`
	Stream           *StreamTelemetry  `json:"stream,omitempty"`
	CompactionSerial int               `json:"compaction_serial"`
	CompactionReason string            `json:"compaction_reason,omitempty"`
	Delegate         *DelegateActivity `json:"delegate,omitempty"`
	StartedAt        int64             `json:"started_at,omitempty"`
	StateStartedAt   int64             `json:"state_started_at,omitempty"`
	ToolStartedAt    int64             `json:"tool_started_at,omitempty"`
	ToolTarget       string            `json:"tool_target,omitempty"`
	ToolLastLine     string            `json:"tool_last_line,omitempty"`
}

type DelegateActivity struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	ToolCalls int    `json:"tool_calls"`
	Stage     string `json:"stage,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Target    string `json:"target,omitempty"`
	Turn      int    `json:"turn,omitempty"`
}

type StreamTelemetry struct {
	Key             string              `json:"key"`
	StartedAt       int64               `json:"started_at"`
	LastChunkAt     int64               `json:"last_chunk_at"`
	HasChunk        bool                `json:"has_chunk"`
	ReasoningChars  int                 `json:"reasoning_chars"`
	TotalChars      int                 `json:"total_chars"`
	Rate            int                 `json:"rate"`
	RateStartedAt   int64               `json:"rate_started_at"`
	RateChars       int                 `json:"rate_chars"`
	Done            bool                `json:"done"`
	ReasoningTokens int                 `json:"reasoning_tokens"`
	Timings         map[string]any      `json:"timings,omitempty"`
	ToolCalls       []StreamingToolCall `json:"tool_calls,omitempty"`
}

type StreamingToolCall struct {
	Index          int    `json:"index"`
	CallID         string `json:"call_id,omitempty"`
	Name           string `json:"name,omitempty"`
	ArgumentBytes  int    `json:"argument_bytes"`
	ArgumentTokens int    `json:"argument_tokens"`
	StartedAt      int64  `json:"started_at"`
	LastChunkAt    int64  `json:"last_chunk_at"`
}

type ChatEntry struct {
	Type                     string              `json:"type"`
	Key                      string              `json:"key"`
	RunID                    string              `json:"run_id,omitempty"`
	Turn                     int                 `json:"turn,omitempty"`
	Text                     string              `json:"text,omitempty"`
	Reasoning                string              `json:"reasoning,omitempty"`
	ReasoningTokens          int                 `json:"reasoningTokens,omitempty"`
	ReasoningTokensEstimated bool                `json:"reasoningTokensEstimated,omitempty"`
	ThinkingStartedMS        int64               `json:"thinkingStartedMS,omitempty"`
	ThinkingEndedMS          int64               `json:"thinkingEndedMS,omitempty"`
	ThinkingMS               *int64              `json:"thinkingMS,omitempty"`
	Done                     bool                `json:"done,omitempty"`
	ToolCallIDs              []string            `json:"toolCallIDs,omitempty"`
	CallID                   string              `json:"callID,omitempty"`
	Name                     string              `json:"name,omitempty"`
	Args                     *map[string]any     `json:"args,omitempty"`
	Result                   map[string]any      `json:"result,omitempty"`
	Content                  string              `json:"content,omitempty"`
	Event                    *events.Event       `json:"event,omitempty"`
	Decision                 string              `json:"decision,omitempty"`
	Attachments              []events.Attachment `json:"attachments,omitempty"`
	AgentRole                string              `json:"agent_role,omitempty"`
}

// Snapshot is the serializable session projection. Complete is false when the log has no
// session.created seed, so callers cannot mistake guessed defaults for reconstructed state.
type Snapshot struct {
	SchemaVersion     int                        `json:"schema_version"`
	Cursor            Cursor                     `json:"cursor"`
	Complete          bool                       `json:"complete"`
	ID                string                     `json:"id"`
	Label             string                     `json:"label"`
	AgentID           string                     `json:"agent_id"`
	ConnectionID      string                     `json:"connection_id"`
	AgentName         string                     `json:"agent_name"`
	BConnection       string                     `json:"b_connection"`
	Role              string                     `json:"role"`
	PlanID            string                     `json:"plan_id,omitempty"`
	PlanName          string                     `json:"plan_name,omitempty"`
	PlanDir           string                     `json:"plan_dir,omitempty"`
	PlanRepo          string                     `json:"plan_repo,omitempty"`
	CreatedAt         string                     `json:"created_at"`
	Folder            string                     `json:"folder"`
	LastActivity      string                     `json:"last_activity,omitempty"`
	Workspace         string                     `json:"workspace"`
	WorkspaceDir      string                     `json:"workspace_dir"`
	WorkspaceMissing  bool                       `json:"workspace_missing"`
	Scratch           bool                       `json:"scratch,omitempty"`
	ProjectContent    string                     `json:"project_content"`
	ProjectFiles      []string                   `json:"project_files"`
	ProjectNotes      []string                   `json:"project_notes"`
	ProjectRoots      []string                   `json:"project_roots,omitempty"`
	PendingRepoPolicy *workspaceinfo.PolicyState `json:"pending_repo_policy,omitempty"`
	RepoPolicy        *workspaceinfo.PolicyState `json:"repo_policy,omitempty"`
	Run               Run                        `json:"run"`
	Tools             []Tool                     `json:"tools"`
	Messages          []events.Message           `json:"messages"`
	Budget            events.Budget              `json:"budget"`
	QueuedMessages    int                        `json:"queued_messages"`
	// WHICH messages are queued, not only how many. The count
	// survived a restart and the queue did not: the messages were in the journal,
	// their place in it was in memory. These ids are what the scheduler rebuilds
	// the queue from, in order, after a restart.
	QueuedMessageIDs     []string           `json:"queued_message_ids,omitempty"`
	Runnable             bool               `json:"runnable"`
	NotRunnableReason    string             `json:"not_runnable_reason"`
	MemoryPath           string             `json:"memory_path"`
	MemoryContent        string             `json:"memory_content"`
	AgentMemoryPath      string             `json:"agent_memory_path"`
	AgentMemoryContent   string             `json:"agent_memory_content"`
	LogPath              string             `json:"log_path"`
	ModelTurns           int                `json:"model_turns"`
	CompactionCount      int                `json:"compaction_count"`
	CompactionTokenDelta int                `json:"compaction_token_delta"`
	CompactionModelCalls int                `json:"compaction_model_calls"`
	CompactionPrompt     int                `json:"compaction_prompt_tokens"`
	CompactionCompletion int                `json:"compaction_completion_tokens"`
	Activity             Activity           `json:"activity"`
	Timeline             []events.Event     `json:"timeline"`
	Chat                 []ChatEntry        `json:"chat"`
	HistoryStart         int                `json:"history_start,omitempty"`
	HistoryEnd           int                `json:"history_end,omitempty"`
	HistoryTotal         int                `json:"history_total,omitempty"`
	PendingApproval      *ChatEntry         `json:"pending_approval,omitempty"`
	ModelUnreachable     *ModelAvailability `json:"model_unreachable,omitempty"`
	ModelBusy            *ModelAvailability `json:"model_busy,omitempty"`
	RunAsYou             bool               `json:"run_as_you,omitempty"`
	Closed               bool               `json:"closed"`
	NamePinned           bool               `json:"name_pinned,omitempty"`
	Origin               string             `json:"origin,omitempty"`
	Owner                string             `json:"owner,omitempty"`
	Stale                bool               `json:"projection_stale,omitempty"`
	StaleReason          string             `json:"projection_stale_reason,omitempty"`
}

type ModelAvailability struct {
	Host   string `json:"host"`
	Detail string `json:"detail,omitempty"`
}
type Operation struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value,omitempty"`
}

type Patch struct {
	SchemaVersion  int         `json:"schema_version"`
	SessionID      string      `json:"session_id"`
	PreviousCursor Cursor      `json:"previous_cursor"`
	Cursor         Cursor      `json:"cursor"`
	Operations     []Operation `json:"operations"`
	Transient      bool        `json:"transient,omitempty"`
}

// connectionFieldAliases is the single compatibility table for endpoint
// fields written by older journals. New projections emit only canonical
// connection names; aliases are read-only and are never written back.
var connectionFieldAliases = map[string][]string{
	"connection_id": {"connection_id", "ser" + "ver_id", "pro" + "file_id"},
	"b_connection":  {"b_connection", "b_" + "pro" + "file", "main_" + "pro" + "file", "main_connection"},
}

func connectionField(data map[string]any, canonical string) string {
	for _, name := range connectionFieldAliases[canonical] {
		if value := stringValue(data[name]); value != "" {
			return value
		}
	}
	return ""
}

func Empty(sessionID string) Snapshot {
	return Snapshot{
		SchemaVersion: SchemaVersion,
		ID:            sessionID,
		Label:         sessionID,
		Run:           Run{Status: "idle"},
		Messages:      []events.Message{},
		Tools:         []Tool{},
		Activity:      Activity{CompletedStages: []string{}},
		Timeline:      []events.Event{},
		Chat:          []ChatEntry{},
	}
}

type RetainedReducer map[string]Snapshot

func NewRetainedReducer() RetainedReducer { return RetainedReducer{} }

func (r RetainedReducer) Prepare(event events.Event) events.Event {
	state := r[event.SessionID]
	if state.ID == "" {
		state = Empty(event.SessionID)
	}
	retained := stripDiagnostic(event)
	completed := event.Type == events.ModelResponse || event.Type == events.RunStopped
	if completed && (event.Type == events.ModelResponse || state.Activity.Stream != nil) {
		retained.Data = retainedData(event.Data, state)
		delete(r, event.SessionID)
	} else if event.Type == events.ModelRequest || event.Type == events.ModelProgress || event.Type == events.ModelDelta {
		r[event.SessionID], _ = NextState(state, Record{Event: event})
	}
	if event.Type == events.ModelDelta {
		return events.Event{}
	}
	return retained
}

func retainedData(original any, state Snapshot) any {
	data := cloneMap(eventMap(original))
	value := map[string]any{"stream": state.Activity.Stream, "partial": state.Run.Partial}
	if len(state.Chat) > 0 {
		value["chat"] = state.Chat[len(state.Chat)-1]
	}
	data["_retained"] = value
	return data
}

// Next is pure: it does not mutate previous, record, or package state.
// Next folds one record into the snapshot AND computes the patch describing
// the change, for the clients that follow a live stream.
func Next(previous Snapshot, record Record) (Snapshot, Patch, error) {
	before := previous
	next, err := NextState(previous, record)
	if err != nil {
		return previous, Patch{}, err
	}
	return next, diff(before, next), nil
}

// NextState folds one record into the snapshot and computes NO patch.
//
// diff() runs reflect.DeepEqual over the whole Messages,
// Chat and Timeline slices, which grow with the chat, so computing a patch for
// every record is quadratic in the length of the journal. Restoring retained
// chats at startup never uses those patches — it wants only the final state —
// and paid 96.6 s of a 101.6 s launch for them on the operator's 19 MB of
// journals. Anything that only needs the state calls this.
func NextState(previous Snapshot, record Record) (Snapshot, error) {
	return nextState(previous, record, false)
}

// nextLive is Store's single-owner fold. It may reuse transcript and timeline
// storage because exposed snapshots isolate the mutable chat entries and all
// other retained values are immutable after publication. Next/NextState stay pure.
func nextLive(previous Snapshot, record Record) (Snapshot, Patch, error) {
	next, err := nextState(previous, record, true)
	if err != nil {
		return previous, Patch{}, err
	}
	comparison := previous
	if record.Event.Type == events.ModelDelta || record.Event.Type == events.ToolResult {
		comparison.Chat = next.Chat
	}
	patch := diff(comparison, next)
	data := eventMap(record.Event.Data)
	if record.Event.Type == events.ModelDelta {
		if entry := chatTurn(next.Chat, record.Event.RunID, intValue(data["turn"])); entry != nil && stringValue(data["text"]) != "" {
			field := "text"
			if stringValue(data["kind"]) == "reasoning" {
				field = "reasoning"
			}
			value, _ := json.Marshal(stringValue(data["text"]))
			patch.Operations = append(patch.Operations, Operation{Op: "append", Path: "/chat/" + pointer(entry.Key) + "/" + field, Value: value})
		}
	} else if record.Event.Type == events.ToolResult {
		if entry := chatCall(next.Chat, stringValue(data["call_id"])); entry != nil {
			value, _ := json.Marshal(entry)
			patch.Operations = append(patch.Operations, Operation{Op: "upsert", Path: "/chat/" + pointer(entry.Key), Value: value})
		}
	}
	return next, patch, nil
}

func nextState(previous Snapshot, record Record, live bool) (Snapshot, error) {
	next, data := prepareProjection(previous, record)
	switch record.Event.Type {
	case events.SessionCreated:
		var err error
		if next, err = projectSessionCreated(next, previous, record); err != nil {
			return previous, err
		}
	case events.SessionRenamed:
		next = projectSessionRenamed(next, data)
	case events.SessionUpdated:
		next = projectSessionUpdated(next, record, data)
	case events.ProjectInstructions:
		next = projectProjectInstructions(next, data)
	case events.PolicyApproved:
		next = projectPolicyApproved(next, record, data)
	case events.PolicyRevoked:
		next = projectPolicyRevoked(next)
	case events.PolicyDenied:
		next = projectPolicyDenied(next)
	case events.WorkspaceBound:
		next = projectWorkspaceBound(next, data)
	case events.MemoryCleared:
		next = projectMemoryCleared(next, data)
	case events.SessionReset:
		next = projectSessionReset(next, data)
	case events.SessionClosed:
		next = projectSessionClosed(next)
	case events.SessionReopened:
		next = projectSessionReopened(next)
	case events.RunQueued:
		next = projectRunQueued(next, record, data)
	case events.RunResumed:
		next = projectRunResumed(next, record)
	case events.RunStarted:
		next = projectRunStarted(next, record, data)
	case events.RunStopping:
		next = projectRunStopping(next, record)
	case events.RunStopped:
		next = projectRunStopped(next, record, data)
	case events.RunLabeled:
		next = projectRunLabeled(next, record, data)
	case events.ModelUnreachable:
		next = projectModelUnreachable(next, data)
	case events.ModelBusy:
		next = projectModelBusy(next, data)
	case events.ModelReachable:
		next = projectModelReachable(next)
	case events.ShellGrant:
		next = projectShellGrant(next, data)
	case events.FileGrant:
		next = projectFileGrant(next, data)
	case events.ShellGrantLapsed:
		next = projectShellGrantLapsed(next, data)
	case events.FileGrantLapsed:
		next = projectFileGrantLapsed(next, data)
	case events.DelegatedUsage:
		next = projectDelegatedUsage(next, data)
	case events.Stage:
		next = projectStage(next, record, data)
	case events.ModelRequest:
		next = projectModelRequest(next, record, data)
	case events.ModelProgress:
		next = projectModelProgress(next, record, data)
	case events.ModelDelta:
		next = projectModelDelta(next, record, data, live)
	case events.ModelResponse:
		next = projectModelResponse(next, record, data)
	case events.ToolCallEvent:
		next = projectToolCallEvent(next, record, data)
	case events.ToolProgress:
		next = projectToolProgress(next, record, data)
	case events.ToolResult:
		next = projectToolResult(next, record, data, live)
	case events.ToolToggled:
		next = projectToolToggled(next, data)
	case events.MessageAppended:
		var err error
		if next, err = projectMessageAppended(next, previous, record); err != nil {
			return previous, err
		}
	case events.MessageUpdated:
		next = projectMessageUpdated(next, data)
	case events.MessagesReminted:
		next = projectMessagesReminted(next, data)
	case events.MessageRemoved:
		next = projectMessageRemoved(next, data)
	case events.MessageQueued:
		next = projectMessageQueued(next, data)
	case events.BudgetEvent:
		var err error
		if next, err = projectBudgetEvent(next, previous, record); err != nil {
			return previous, err
		}
	case events.ApprovalRequired:
		next = projectApprovalRequired(next, record)
	case events.ApprovalDecided:
		next = projectApprovalDecided(next, record, data)
	case events.CycleDetected:
		next = projectCycleDetected(next, data)
	case events.WorkspaceConflict:
		next = projectWorkspaceConflict(next)
	case events.Compaction:
		next = projectCompaction(next, data)
	case events.CompactionSummary:
		var err error
		if next, err = projectCompactionSummary(next, previous, record); err != nil {
			return previous, err
		}
	case events.MemoryNoted:
		next = projectMemoryNoted(next, data)
	}
	return finishProjection(next, record, data, live), nil
}

// compactibleCategories are the only ones compaction can shrink. system, project,
// tools and the two memory layers are the fixed prefix and stay where they are.
var compactibleCategories = []string{"files", "results", "fetched", "history", "summary"}

func compactedBudget(budget events.Budget, before, after int) events.Budget {
	freed := before - after
	if freed <= 0 || budget.UsedEst <= 0 {
		return budget
	}
	budget.UsedEst = max(0, budget.UsedEst-freed)
	if len(budget.Categories) == 0 {
		return budget
	}
	categories := make(map[string]int, len(budget.Categories))
	for key, value := range budget.Categories {
		categories[key] = value
	}
	remaining := freed
	for _, key := range compactibleCategories {
		if remaining <= 0 {
			break
		}
		take := min(categories[key], remaining)
		categories[key] -= take
		remaining -= take
	}
	budget.Categories = categories
	return budget
}

func summaryTranscript(content string) string {
	// The header now names the turns the note covers, so strip the whole first
	// line whenever it is one of ours rather than one fixed string.
	if strings.HasPrefix(content, "Progress note (auto-summary of ") {
		if index := strings.Index(content, "\n"); index >= 0 {
			content = content[index+1:]
		}
	}
	if index := strings.Index(content, "\n\n[BEGIN COMPACTION EVIDENCE]"); index >= 0 {
		content = content[:index]
	}
	return strings.TrimSpace(content)
}

func touchStream(current *StreamTelemetry, event events.Event, data map[string]any, delta bool) *StreamTelemetry {
	value := &StreamTelemetry{}
	if current != nil {
		encoded, _ := json.Marshal(current)
		_ = json.Unmarshal(encoded, value)
	}
	at, key := eventMillis(event), turnKey(event, data)
	if value.Key != key {
		value = &StreamTelemetry{Key: key, StartedAt: at, LastChunkAt: at, RateStartedAt: at}
	}
	value.LastChunkAt, value.HasChunk = at, true
	if delta {
		chars := len([]rune(stringValue(data["text"])))
		if stringValue(data["kind"]) == "reasoning" {
			value.ReasoningChars += chars
		}
		value.TotalChars += chars
		if at-value.RateStartedAt > 5000 {
			value.RateStartedAt, value.RateChars = at, 0
		}
		value.RateChars += chars
		seconds := math.Max(.25, float64(at-value.RateStartedAt)/1000)
		value.Rate = int(math.Round(float64(value.RateChars) / 3.6 / seconds))
		if stringValue(data["kind"]) == "tool_call" {
			index := intValue(data["index"])
			position := -1
			for candidate := range value.ToolCalls {
				if value.ToolCalls[candidate].Index == index {
					position = candidate
					break
				}
			}
			if position < 0 {
				value.ToolCalls = append(value.ToolCalls, StreamingToolCall{Index: index, StartedAt: at})
				position = len(value.ToolCalls) - 1
			}
			call := &value.ToolCalls[position]
			if id := stringValue(data["call_id"]); id != "" {
				call.CallID = id
			}
			if name := stringValue(data["name"]); name != "" {
				call.Name = name
			}
			call.ArgumentBytes = intValue(data["argument_bytes"])
			call.ArgumentTokens = intValue(data["argument_tokens"])
			call.LastChunkAt = at
		}
	}
	return value
}

func eventMillis(event events.Event) int64 {
	value, err := time.Parse(time.RFC3339Nano, event.TS)
	if err != nil {
		return 0
	}
	return value.UnixMilli()
}
func turnKey(event events.Event, data map[string]any) string {
	return event.RunID + ":" + strconv.Itoa(intValue(data["turn"]))
}

type seed struct {
	ID                   string                     `json:"id"`
	Label                string                     `json:"label"`
	AgentID              string                     `json:"agent_id"`
	ConnectionID         string                     `json:"connection_id"`
	AgentName            string                     `json:"agent_name"`
	BConnection          string                     `json:"b_connection"`
	Role                 string                     `json:"role"`
	PlanID               string                     `json:"plan_id,omitempty"`
	PlanName             string                     `json:"plan_name,omitempty"`
	PlanDir              string                     `json:"plan_dir,omitempty"`
	PlanRepo             string                     `json:"plan_repo,omitempty"`
	CreatedAt            string                     `json:"created_at"`
	Closed               bool                       `json:"closed"`
	NamePinned           bool                       `json:"name_pinned,omitempty"`
	Origin               string                     `json:"origin,omitempty"`
	Owner                string                     `json:"owner,omitempty"`
	Workspace            string                     `json:"workspace"`
	WorkspaceDir         string                     `json:"workspace_dir"`
	WorkspaceMissing     bool                       `json:"workspace_missing"`
	Scratch              bool                       `json:"scratch,omitempty"`
	ProjectContent       string                     `json:"project_content"`
	ProjectFiles         []string                   `json:"project_files"`
	ProjectNotes         []string                   `json:"project_notes"`
	ProjectRoots         []string                   `json:"project_roots,omitempty"`
	PendingRepoPolicy    *workspaceinfo.PolicyState `json:"pending_repo_policy,omitempty"`
	RepoPolicy           *workspaceinfo.PolicyState `json:"repo_policy,omitempty"`
	Run                  Run                        `json:"run"`
	Tools                []Tool                     `json:"tools"`
	Messages             []events.Message           `json:"messages"`
	Budget               events.Budget              `json:"budget"`
	QueuedMessages       int                        `json:"queued_messages"`
	QueuedMessageIDs     []string                   `json:"queued_message_ids,omitempty"`
	Runnable             bool                       `json:"runnable"`
	NotRunnableReason    string                     `json:"not_runnable_reason"`
	MemoryPath           string                     `json:"memory_path"`
	MemoryContent        string                     `json:"memory_content"`
	AgentMemoryPath      string                     `json:"agent_memory_path"`
	AgentMemoryContent   string                     `json:"agent_memory_content"`
	LogPath              string                     `json:"log_path"`
	ModelTurns           int                        `json:"model_turns"`
	CompactionCount      int                        `json:"compaction_count"`
	CompactionTokenDelta int                        `json:"compaction_token_delta"`
	CompactionModelCalls int                        `json:"compaction_model_calls"`
	CompactionPrompt     int                        `json:"compaction_prompt_tokens"`
	CompactionCompletion int                        `json:"compaction_completion_tokens"`
}

func (value *seed) UnmarshalJSON(data []byte) error {
	type plain seed
	if err := json.Unmarshal(data, (*plain)(value)); err != nil {
		return err
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if value.ConnectionID == "" {
		value.ConnectionID = connectionField(fields, "connection_id")
	}
	if value.BConnection == "" {
		value.BConnection = connectionField(fields, "b_connection")
	}
	return nil
}

func (value seed) snapshot(cursor Cursor) Snapshot {
	return Snapshot{
		SchemaVersion: SchemaVersion, Cursor: cursor, Complete: true,
		ID: value.ID, Label: value.Label, AgentID: value.AgentID, ConnectionID: value.ConnectionID, AgentName: value.AgentName, BConnection: value.BConnection, Role: firstString(value.Role, "b"), PlanID: value.PlanID, PlanName: value.PlanName, PlanDir: value.PlanDir, PlanRepo: value.PlanRepo, CreatedAt: value.CreatedAt, Closed: value.Closed, NamePinned: value.NamePinned, Origin: value.Origin, Owner: value.Owner, Workspace: value.Workspace, WorkspaceDir: firstString(value.WorkspaceDir, value.Workspace), WorkspaceMissing: value.WorkspaceMissing, Scratch: value.Scratch, ProjectContent: value.ProjectContent, ProjectFiles: append([]string(nil), value.ProjectFiles...), ProjectNotes: append([]string(nil), value.ProjectNotes...), ProjectRoots: append([]string(nil), value.ProjectRoots...), PendingRepoPolicy: value.PendingRepoPolicy, RepoPolicy: value.RepoPolicy,
		Run: value.Run, Tools: cloneTools(value.Tools), Messages: cloneMessages(value.Messages), Budget: value.Budget,
		QueuedMessages: value.QueuedMessages, QueuedMessageIDs: append([]string(nil), value.QueuedMessageIDs...), Runnable: value.Runnable, NotRunnableReason: value.NotRunnableReason,
		MemoryPath: value.MemoryPath, MemoryContent: value.MemoryContent, AgentMemoryPath: value.AgentMemoryPath, AgentMemoryContent: value.AgentMemoryContent, LogPath: value.LogPath,
		ModelTurns: value.ModelTurns, CompactionCount: value.CompactionCount, CompactionTokenDelta: value.CompactionTokenDelta,
		CompactionModelCalls: value.CompactionModelCalls, CompactionPrompt: value.CompactionPrompt,
		CompactionCompletion: value.CompactionCompletion, Activity: Activity{CompletedStages: []string{}},
		Timeline: []events.Event{},
		Chat:     []ChatEntry{},
	}
}

func diff(before, after Snapshot) Patch {
	patch := Patch{SchemaVersion: SchemaVersion, SessionID: after.ID, PreviousCursor: before.Cursor, Cursor: after.Cursor, Operations: []Operation{}}
	fields := []struct {
		path        string
		before, now any
	}{
		{"complete", before.Complete, after.Complete}, {"id", before.ID, after.ID}, {"label", before.Label, after.Label},
		{"agent_id", before.AgentID, after.AgentID}, {"connection_id", before.ConnectionID, after.ConnectionID}, {"agent_name", before.AgentName, after.AgentName},
		{"b_connection", before.BConnection, after.BConnection}, {"role", before.Role, after.Role}, {"plan_id", before.PlanID, after.PlanID}, {"plan_name", before.PlanName, after.PlanName}, {"plan_dir", before.PlanDir, after.PlanDir}, {"plan_repo", before.PlanRepo, after.PlanRepo}, {"created_at", before.CreatedAt, after.CreatedAt}, {"workspace", before.Workspace, after.Workspace}, {"workspace_dir", before.WorkspaceDir, after.WorkspaceDir}, {"workspace_missing", before.WorkspaceMissing, after.WorkspaceMissing}, {"scratch", before.Scratch, after.Scratch}, {"project_content", before.ProjectContent, after.ProjectContent}, {"project_files", before.ProjectFiles, after.ProjectFiles}, {"project_notes", before.ProjectNotes, after.ProjectNotes}, {"project_roots", before.ProjectRoots, after.ProjectRoots}, {"pending_repo_policy", before.PendingRepoPolicy, after.PendingRepoPolicy}, {"repo_policy", before.RepoPolicy, after.RepoPolicy},
		{"tools", before.Tools, after.Tools}, {"messages", before.Messages, after.Messages}, {"budget", before.Budget, after.Budget},
		{"queued_messages", before.QueuedMessages, after.QueuedMessages}, {"queued_message_ids", before.QueuedMessageIDs, after.QueuedMessageIDs}, {"runnable", before.Runnable, after.Runnable},
		{"not_runnable_reason", before.NotRunnableReason, after.NotRunnableReason}, {"memory_path", before.MemoryPath, after.MemoryPath},
		{"memory_content", before.MemoryContent, after.MemoryContent}, {"agent_memory_path", before.AgentMemoryPath, after.AgentMemoryPath}, {"agent_memory_content", before.AgentMemoryContent, after.AgentMemoryContent}, {"log_path", before.LogPath, after.LogPath},
		{"model_turns", before.ModelTurns, after.ModelTurns}, {"compaction_count", before.CompactionCount, after.CompactionCount},
		{"compaction_token_delta", before.CompactionTokenDelta, after.CompactionTokenDelta},
		{"compaction_model_calls", before.CompactionModelCalls, after.CompactionModelCalls},
		{"compaction_prompt_tokens", before.CompactionPrompt, after.CompactionPrompt},
		{"compaction_completion_tokens", before.CompactionCompletion, after.CompactionCompletion},
		{"activity", before.Activity, after.Activity}, {"pending_approval", before.PendingApproval, after.PendingApproval}, {"model_unreachable", before.ModelUnreachable, after.ModelUnreachable}, {"model_busy", before.ModelBusy, after.ModelBusy}, {"run_as_you", before.RunAsYou, after.RunAsYou}, {"closed", before.Closed, after.Closed}, {"name_pinned", before.NamePinned, after.NamePinned}, {"origin", before.Origin, after.Origin}, {"owner", before.Owner, after.Owner},
		{"projection_stale", before.Stale, after.Stale}, {"projection_stale_reason", before.StaleReason, after.StaleReason},
	}
	patch.Operations = append(patch.Operations, diffRun(before.Run, after.Run)...)
	patch.Operations = append(patch.Operations, diffChat(before.Chat, after.Chat)...)
	for _, field := range fields {
		if reflect.DeepEqual(field.before, field.now) {
			continue
		}
		value, _ := json.Marshal(field.now)
		patch.Operations = append(patch.Operations, Operation{Op: "replace", Path: "/" + field.path, Value: value})
	}
	if len(after.Timeline) == len(before.Timeline)+1 {
		value, _ := json.Marshal(after.Timeline[len(after.Timeline)-1])
		patch.Operations = append(patch.Operations, Operation{Op: "append", Path: "/timeline", Value: value})
	} else if !reflect.DeepEqual(before.Timeline, after.Timeline) {
		value, _ := json.Marshal(after.Timeline)
		patch.Operations = append(patch.Operations, Operation{Op: "replace", Path: "/timeline", Value: value})
	}
	return patch
}

func diffRun(before, after Run) []Operation {
	if reflect.DeepEqual(before, after) {
		return nil
	}
	withoutBefore, withoutAfter := before, after
	withoutBefore.Partial, withoutAfter.Partial = "", ""
	if reflect.DeepEqual(withoutBefore, withoutAfter) && strings.HasPrefix(after.Partial, before.Partial) {
		value, _ := json.Marshal(strings.TrimPrefix(after.Partial, before.Partial))
		return []Operation{{Op: "append", Path: "/run/partial", Value: value}}
	}
	value, _ := json.Marshal(after)
	return []Operation{{Op: "replace", Path: "/run", Value: value}}
}

func diffChat(before, after []ChatEntry) []Operation {
	if reflect.DeepEqual(before, after) {
		return nil
	}
	if len(after) == len(before)+1 && reflect.DeepEqual(before, after[:len(before)]) {
		value, _ := json.Marshal(after[len(after)-1])
		return []Operation{{Op: "append", Path: "/chat", Value: value}}
	}
	if len(after) == len(before) && len(after) > 0 && reflect.DeepEqual(before[:len(before)-1], after[:len(after)-1]) && before[len(before)-1].Key == after[len(after)-1].Key {
		left, right := before[len(before)-1], after[len(after)-1]
		leftReason, rightReason := left.Reasoning, right.Reasoning
		left.Reasoning, right.Reasoning = "", ""
		if reflect.DeepEqual(left, right) && strings.HasPrefix(rightReason, leftReason) {
			value, _ := json.Marshal(strings.TrimPrefix(rightReason, leftReason))
			return []Operation{{Op: "append", Path: "/chat/" + pointer(right.Key) + "/reasoning", Value: value}}
		}
		left, right = before[len(before)-1], after[len(after)-1]
		leftText, rightText := left.Text, right.Text
		left.Text, right.Text = "", ""
		if reflect.DeepEqual(left, right) && strings.HasPrefix(rightText, leftText) {
			value, _ := json.Marshal(strings.TrimPrefix(rightText, leftText))
			return []Operation{{Op: "append", Path: "/chat/" + pointer(right.Key) + "/text", Value: value}}
		}
		value, _ := json.Marshal(after[len(after)-1])
		return []Operation{{Op: "upsert", Path: "/chat/" + pointer(after[len(after)-1].Key), Value: value}}
	}
	if len(after) == len(before) {
		operations := make([]Operation, 0, len(after))
		for index := range after {
			if before[index].Key != after[index].Key {
				operations = nil
				break
			}
			if reflect.DeepEqual(before[index], after[index]) {
				continue
			}
			value, _ := json.Marshal(after[index])
			operations = append(operations, Operation{Op: "upsert", Path: "/chat/" + pointer(after[index].Key), Value: value})
		}
		if operations != nil {
			return operations
		}
	}
	value, _ := json.Marshal(after)
	return []Operation{{Op: "replace", Path: "/chat", Value: value}}
}
func pointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

func stripDiagnostic(event events.Event) events.Event {
	event.Body = nil
	event.Raw = nil
	if data := eventMap(event.Data); data["_retained"] != nil {
		clean := cloneMap(data)
		delete(clean, "_retained")
		event.Data = clean
	}
	return event
}

func discardStream(values []events.Event, runID string, turn int) []events.Event {
	out := make([]events.Event, 0, len(values))
	for _, event := range values {
		if (event.Type == events.ModelDelta || event.Type == events.ModelProgress) && event.RunID == runID && intValue(eventMap(event.Data)["turn"]) == turn {
			continue
		}
		out = append(out, event)
	}
	return out
}

func discardRunStream(values []events.Event, runID string) []events.Event {
	out := make([]events.Event, 0, len(values))
	for _, event := range values {
		if (event.Type == events.ModelDelta || event.Type == events.ModelProgress) && event.RunID == runID {
			continue
		}
		out = append(out, event)
	}
	return out
}

func appendChat(values []ChatEntry, entry ChatEntry) []ChatEntry {
	result := cloneChat(values)
	return append(result, entry)
}
func upsertModelUnreachableNotice(values []ChatEntry, entry ChatEntry) []ChatEntry {
	result := cloneChat(values)
	for index := len(result) - 1; index >= 0; index-- {
		current := result[index]
		if current.Type == "notice" && current.Event != nil && current.Event.Type == events.RunStopped &&
			stringValue(eventMap(current.Event.Data)["reason"]) == "model_unreachable" && current.Text == entry.Text && current.Decision != "resolved" {
			entry.Key = current.Key
			result[index] = entry
			return result
		}
	}
	return append(result, entry)
}
func resolveModelUnreachableNotice(values []ChatEntry, host string) []ChatEntry {
	result := cloneChat(values)
	for index := len(result) - 1; index >= 0; index-- {
		entry := &result[index]
		if entry.Type == "notice" && entry.Event != nil && entry.Event.Type == events.RunStopped &&
			stringValue(eventMap(entry.Event.Data)["reason"]) == "model_unreachable" && entry.Text == host && entry.Decision != "resolved" {
			entry.Decision = "resolved"
			break
		}
	}
	return result
}
func removeChatKey(values []ChatEntry, key string) []ChatEntry {
	result := make([]ChatEntry, 0, len(values))
	for _, entry := range values {
		if entry.Key != key {
			result = append(result, entry)
		}
	}
	return result
}
func removeAssistantTurn(values []ChatEntry, turn int) []ChatEntry {
	for index := len(values) - 1; index >= 0; index-- {
		if values[index].Type == "agent" && values[index].Turn == turn {
			return append(values[:index], values[index+1:]...)
		}
	}
	return values
}

// cloneChat copies the transcript so the caller may mutate it without touching
// the snapshot it came from. Next is pure and live clients depend on that.
//
// this used to marshal the whole transcript to JSON and
// unmarshal it back, on EVERY event that touches the chat — and model.delta,
// which is about 99.6% of the events in a real journal, is one of them. The
// cost is the length of the transcript so far, per delta, through the JSON
// encoder: 74 s of a 75 s startup on the operator's 19 MB of journals. The
// copy below is the same deep copy done directly, which leaves the contract
// unchanged and the encoder out of it.
func cloneChat(values []ChatEntry) []ChatEntry {
	result := make([]ChatEntry, len(values))
	copy(result, values)
	for index := range result {
		entry := &result[index]
		if entry.ToolCallIDs != nil {
			entry.ToolCallIDs = append([]string(nil), entry.ToolCallIDs...)
		}
		if entry.Attachments != nil {
			entry.Attachments = append([]events.Attachment(nil), entry.Attachments...)
		}
		if entry.ThinkingMS != nil {
			value := *entry.ThinkingMS
			entry.ThinkingMS = &value
		}
		if entry.Args != nil {
			value := cloneAnyMap(*entry.Args)
			entry.Args = &value
		}
		if entry.Result != nil {
			entry.Result = cloneAnyMap(entry.Result)
		}
		if entry.Event != nil {
			value := *entry.Event
			value.Data = cloneAny(value.Data)
			entry.Event = &value
		}
	}
	return result
}

// cloneAnyMap and cloneAny copy the decoded-JSON shapes the entries carry:
// maps, slices and scalars. A scalar needs no copy; anything else is rebuilt
// so a mutation through one snapshot cannot be seen through another.
func cloneAnyMap(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = cloneAny(item)
	}
	return result
}

func cloneAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneAnyMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneAny(item)
		}
		return result
	default:
		return value
	}
}

// chatWithMutableTurn returns the transcript with exactly one entry safe to
// mutate: the newest agent entry for this run and turn. The slice itself is
// copied so the previous snapshot keeps its own length and ordering, and only
// the target entry is deep-copied.
func chatWithMutableTurn(values []ChatEntry, runID string, turn int) ([]ChatEntry, *ChatEntry) {
	index := -1
	for at := len(values) - 1; at >= 0; at-- {
		if values[at].Type == "agent" && values[at].RunID == runID && values[at].Turn == turn {
			index = at
			break
		}
	}
	result := make([]ChatEntry, len(values))
	copy(result, values)
	if index < 0 {
		return result, nil
	}
	only := cloneChat(result[index : index+1])
	result[index] = only[0]
	return result, &result[index]
}

func chatTurn(values []ChatEntry, runID string, turn int) *ChatEntry {
	for index := len(values) - 1; index >= 0; index-- {
		if values[index].Type == "agent" && values[index].RunID == runID && values[index].Turn == turn {
			return &values[index]
		}
	}
	return nil
}
func chatTurnAny(values []ChatEntry, turn int) *ChatEntry {
	for index := len(values) - 1; index >= 0; index-- {
		if values[index].Type == "agent" && values[index].Turn == turn {
			return &values[index]
		}
	}
	return nil
}
func chatCall(values []ChatEntry, callID string) *ChatEntry {
	for index := len(values) - 1; index >= 0; index-- {
		if values[index].Type == "tool" && values[index].CallID == callID {
			return &values[index]
		}
	}
	return nil
}
func toolCalls(value any) []events.ToolCall {
	var result []events.ToolCall
	_ = decode(value, &result)
	return result
}
func chatNotice(value string) bool {
	switch value {
	case events.RunStopping, events.RunStopped, events.RunAborted, events.RunQueued, events.MessageQueued, events.ModelRetry, events.Compaction, events.WorkspaceConflict, events.ApprovalRequired, events.MemoryCleared, events.ProjectInstructions, events.PolicyApproved, events.PolicyDenied, events.PolicyRevoked, events.OperatorContext, events.SigningApplied, events.FilesDelivered, events.ShellGrant, events.ShellGrantLapsed, events.FileGrant, events.FileGrantLapsed, events.ServiceIdentityUnavailable, events.WorkerJob:
		return true
	}
	return false
}

func stringSlice(value any) []string { var out []string; _ = decode(value, &out); return out }
func shortToolTarget(args map[string]any) string {
	for _, key := range []string{"path", "pattern", "command", "service", "task"} {
		if value := strings.TrimSpace(stringValue(args[key])); value != "" {
			value = strings.Join(strings.Fields(value), " ")
			if len(value) > 80 {
				value = value[:77] + "…"
			}
			return value
		}
	}
	return ""
}
func appendUniqueStrings(values []string, additions ...string) []string {
	for _, addition := range additions {
		found := false
		for _, value := range values {
			if value == addition {
				found = true
				break
			}
		}
		if !found {
			values = append(values, addition)
		}
	}
	return values
}
func filepathClean(value string) string { return strings.ToLower(strings.ReplaceAll(value, "/", "\\")) }

func decode(value, target any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func eventMap(value any) map[string]any {
	if result, ok := value.(map[string]any); ok {
		return result
	}
	data, _ := json.Marshal(value)
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	return result
}

func mapValue(value any) map[string]any { return cloneMap(eventMap(value)) }
func stringValue(value any) string      { result, _ := value.(string); return result }
func boolValue(value any) bool          { result, _ := value.(bool); return result }
func intValue(value any) int {
	switch number := value.(type) {
	case int:
		return number
	case float64:
		return int(number)
	case json.Number:
		result, _ := number.Int64()
		return int(result)
	default:
		return 0
	}
}
func firstString(values ...any) string {
	for _, value := range values {
		if result := stringValue(value); result != "" {
			return result
		}
	}
	return ""
}
func stringValues(value any) []string {
	if values, ok := value.([]string); ok {
		return append([]string(nil), values...)
	}
	raw, _ := value.([]any)
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		result = append(result, stringValue(item))
	}
	return result
}
func cloneMessages(values []events.Message) []events.Message {
	return append([]events.Message(nil), values...)
}

// withoutQueuedID drops one id from the queue, or the first entry when the event
// named none - an older journal, where the position was only ever a count.
func withoutQueuedID(ids []string, id string) []string {
	if len(ids) == 0 {
		return nil
	}
	if id == "" {
		return append([]string(nil), ids[1:]...)
	}
	result := make([]string, 0, len(ids))
	removed := false
	for _, one := range ids {
		if !removed && one == id {
			removed = true
			continue
		}
		result = append(result, one)
	}
	if !removed {
		return append([]string(nil), ids[1:]...)
	}
	return result
}

func cloneTools(values []Tool) []Tool { return append([]Tool(nil), values...) }
func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}
func appendCopy(values []string, value string) []string {
	return append(append([]string(nil), values...), value)
}
func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
