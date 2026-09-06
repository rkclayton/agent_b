// Package projection folds durable session events into the versioned state served to clients.
// It has no runtime dependencies: a snapshot is a pure function of a prior snapshot and a
// durably located JSONL record.
package projection

import (
	"encoding/json"
	"reflect"
	"strings"

	"harness/internal/events"
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
	MaxTurns       int    `json:"max_turns"`
	QueuePosition  int    `json:"queue_position"`
	Partial        string `json:"partial"`
	LastStopReason string `json:"last_stop_reason"`
}

type Tool struct {
	Name           string `json:"name"`
	Enabled        bool   `json:"enabled"`
	Calls          int    `json:"calls"`
	SchemaTokens   int    `json:"schema_tokens"`
	MarginalTokens int    `json:"marginal_tokens"`
}

type Activity struct {
	Stage           string         `json:"stage"`
	StageState      string         `json:"stage_state"`
	CompletedStages []string       `json:"completed_stages"`
	ActiveTool      string         `json:"active_tool"`
	AlarmTool       string         `json:"alarm_tool"`
	DispatchAlarm   bool           `json:"dispatch_alarm"`
	Progress        map[string]any `json:"progress,omitempty"`
	LastTimings     map[string]any `json:"last_timings,omitempty"`
}

// Snapshot is the serializable session projection. Complete is false when the log has no
// session.created seed, so callers cannot mistake guessed defaults for reconstructed state.
type Snapshot struct {
	SchemaVersion        int              `json:"schema_version"`
	Cursor               Cursor           `json:"cursor"`
	Complete             bool             `json:"complete"`
	ID                   string           `json:"id"`
	Label                string           `json:"label"`
	ServerID             string           `json:"server_id"`
	Workspace            string           `json:"workspace"`
	Run                  Run              `json:"run"`
	Tools                []Tool           `json:"tools"`
	Messages             []events.Message `json:"messages"`
	Budget               events.Budget    `json:"budget"`
	QueuedMessages       int              `json:"queued_messages"`
	Runnable             bool             `json:"runnable"`
	NotRunnableReason    string           `json:"not_runnable_reason"`
	MemoryPath           string           `json:"memory_path"`
	MemoryContent        string           `json:"memory_content"`
	LogPath              string           `json:"log_path"`
	ModelTurns           int              `json:"model_turns"`
	CompactionCount      int              `json:"compaction_count"`
	CompactionTokenDelta int              `json:"compaction_token_delta"`
	CompactionModelCalls int              `json:"compaction_model_calls"`
	CompactionPrompt     int              `json:"compaction_prompt_tokens"`
	CompactionCompletion int              `json:"compaction_completion_tokens"`
	Activity             Activity         `json:"activity"`
	Closed               bool             `json:"closed"`
}

type Operation struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value,omitempty"`
}

type Patch struct {
	SchemaVersion int         `json:"schema_version"`
	SessionID     string      `json:"session_id"`
	Cursor        Cursor      `json:"cursor"`
	Operations    []Operation `json:"operations"`
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
	}
}

// Next is pure: it does not mutate previous, record, or package state.
func Next(previous Snapshot, record Record) (Snapshot, Patch, error) {
	before := previous
	next := previous
	next.SchemaVersion = SchemaVersion
	next.Cursor = record.Cursor
	if next.ID == "" {
		next.ID = record.Event.SessionID
		next.Label = record.Event.SessionID
	}
	data := eventMap(record.Event.Data)

	switch record.Event.Type {
	case events.SessionCreated:
		var wrapper struct {
			Session seed `json:"session"`
		}
		if err := decode(record.Event.Data, &wrapper); err != nil {
			return previous, Patch{}, err
		}
		next = wrapper.Session.snapshot(record.Cursor)
	case events.SessionRenamed:
		next.Label = stringValue(data["label"])
	case events.SessionUpdated:
		next.ServerID = stringValue(data["server_id"])
		next.Runnable = boolValue(data["runnable"])
		next.NotRunnableReason = stringValue(data["not_runnable_reason"])
		next.MemoryPath = stringValue(data["memory_path"])
		next.MemoryContent = stringValue(data["memory_content"])
	case events.SessionReset:
		next.Messages = []events.Message{}
		next.Tools = cloneTools(next.Tools)
		for index := range next.Tools {
			next.Tools[index].Calls = 0
		}
		next.QueuedMessages = 0
		next.ModelTurns = 0
		next.CompactionCount = 0
		next.CompactionTokenDelta = 0
		next.CompactionModelCalls = 0
		next.CompactionPrompt = 0
		next.CompactionCompletion = 0
		next.Run = Run{Status: "idle", MaxTurns: next.Run.MaxTurns}
		next.Activity = Activity{CompletedStages: []string{}}
		if value := stringValue(data["log_path"]); value != "" {
			next.LogPath = value
		}
	case events.SessionClosed:
		next.Closed = true
	case events.RunQueued:
		next.Run.Status = "queued"
		next.Run.RunID = firstString(data["run_id"], record.Event.RunID)
		next.Run.QueuePosition = intValue(data["position"])
	case events.RunStarted:
		next.Run.Status = "running"
		next.Run.RunID = firstString(data["run_id"], record.Event.RunID)
		next.Run.Turn = 0
		next.Run.QueuePosition = 0
		next.Run.Partial = ""
		next.Run.LastStopReason = ""
		next.QueuedMessages = max(0, next.QueuedMessages-1)
		next.Activity.DispatchAlarm = false
	case events.RunStopped:
		next.Run = Run{Status: "idle", MaxTurns: next.Run.MaxTurns, LastStopReason: stringValue(data["reason"])}
		next.Activity.Stage = "wait_user"
		next.Activity.StageState = "enter"
		next.Activity.ActiveTool = ""
		if stringValue(data["reason"]) == "tool_errors" {
			next.Activity.DispatchAlarm = true
		}
	case events.Stage:
		stage, state := stringValue(data["stage"]), stringValue(data["state"])
		next.Run.Turn = intValue(data["turn"])
		next.Activity.StageState = state
		if state == "enter" {
			next.Activity.Stage = stage
			if stage == "assemble" {
				next.Activity.CompletedStages = []string{}
			}
		} else if !contains(next.Activity.CompletedStages, stage) {
			next.Activity.CompletedStages = appendCopy(next.Activity.CompletedStages, stage)
		}
	case events.ModelProgress:
		next.Activity.Progress = cloneMap(data)
	case events.ModelDelta:
		if stringValue(data["kind"]) == "content" {
			next.Run.Partial += stringValue(data["text"])
		}
	case events.ModelResponse:
		next.Run.Partial = ""
		next.ModelTurns++
		next.Activity.LastTimings = mapValue(data["timings"])
	case events.ToolCallEvent:
		next.Activity.ActiveTool = stringValue(data["name"])
	case events.ToolResult:
		next.Activity.ActiveTool = ""
		next.Tools = cloneTools(next.Tools)
		for index := range next.Tools {
			if next.Tools[index].Name == stringValue(data["name"]) {
				next.Tools[index].Calls++
				break
			}
		}
	case events.ToolToggled:
		next.Tools = cloneTools(next.Tools)
		for index := range next.Tools {
			if next.Tools[index].Name == stringValue(data["name"]) {
				next.Tools[index].Enabled = boolValue(data["enabled"])
				break
			}
		}
	case events.MessageAppended:
		var wrapper struct {
			Message events.Message `json:"message"`
		}
		if err := decode(record.Event.Data, &wrapper); err != nil {
			return previous, Patch{}, err
		}
		next.Messages = cloneMessages(next.Messages)
		if wrapper.Message.Category == "summary" {
			at := min(1, len(next.Messages))
			next.Messages = append(next.Messages[:at], append([]events.Message{wrapper.Message}, next.Messages[at:]...)...)
		} else {
			next.Messages = append(next.Messages, wrapper.Message)
		}
	case events.MessageUpdated:
		next.Messages = cloneMessages(next.Messages)
		patch := eventMap(data["patch"])
		for index := range next.Messages {
			if next.Messages[index].ID != stringValue(data["id"]) {
				continue
			}
			if value, ok := patch["content"]; ok {
				next.Messages[index].Content = stringValue(value)
			}
			if value, ok := patch["tokens"]; ok {
				next.Messages[index].Tokens = intValue(value)
			}
			if value, ok := patch["elided"]; ok {
				next.Messages[index].Elided = boolValue(value)
			}
		}
	case events.MessageQueued:
		next.QueuedMessages++
	case events.BudgetEvent:
		var budget events.Budget
		if err := decode(record.Event.Data, &budget); err != nil {
			return previous, Patch{}, err
		}
		next.Budget = budget
		next.Tools = cloneTools(next.Tools)
		for index := range next.Tools {
			if value, ok := next.Budget.ToolSchemaTokens[next.Tools[index].Name]; ok {
				next.Tools[index].SchemaTokens = value
			}
			if value, ok := next.Budget.ToolMarginalTokens[next.Tools[index].Name]; ok {
				next.Tools[index].MarginalTokens = value
			}
		}
	case events.ApprovalRequired:
		next.Run.Status = "paused"
	case events.ApprovalDecided:
		next.Run.Status = "running"
	case events.CycleDetected:
		next.Activity.DispatchAlarm = true
	case events.WorkspaceConflict:
		next.Activity.AlarmTool = next.Activity.ActiveTool
	case events.Compaction:
		next.CompactionCount++
		next.CompactionTokenDelta += intValue(data["after"]) - intValue(data["before"])
		if stringValue(data["kind"]) == "summarize" {
			removed := map[string]bool{}
			for _, id := range stringValues(data["affected_ids"]) {
				removed[id] = true
			}
			kept := make([]events.Message, 0, len(next.Messages))
			for _, message := range next.Messages {
				if !removed[message.ID] {
					kept = append(kept, message)
				}
			}
			next.Messages = kept
		}
	case events.CompactionSummary:
		var attempt events.CompactionSummaryData
		if err := decode(record.Event.Data, &attempt); err != nil {
			return previous, Patch{}, err
		}
		if attempt.Dispatched {
			next.CompactionModelCalls++
			next.CompactionPrompt += attempt.Usage.PromptTokens
			next.CompactionCompletion += attempt.Usage.CompletionTokens
		}
	case events.MemoryNoted:
		next.MemoryPath = firstString(data["path"], next.MemoryPath)
		next.MemoryContent = strings.TrimRight(next.MemoryContent, "\r\n") + "\n- " + stringValue(data["note"]) + "\n"
	}

	next.Cursor = record.Cursor
	return next, diff(before, next), nil
}

type seed struct {
	ID                   string           `json:"id"`
	Label                string           `json:"label"`
	ServerID             string           `json:"server_id"`
	Workspace            string           `json:"workspace"`
	Run                  Run              `json:"run"`
	Tools                []Tool           `json:"tools"`
	Messages             []events.Message `json:"messages"`
	Budget               events.Budget    `json:"budget"`
	QueuedMessages       int              `json:"queued_messages"`
	Runnable             bool             `json:"runnable"`
	NotRunnableReason    string           `json:"not_runnable_reason"`
	MemoryPath           string           `json:"memory_path"`
	MemoryContent        string           `json:"memory_content"`
	LogPath              string           `json:"log_path"`
	ModelTurns           int              `json:"model_turns"`
	CompactionCount      int              `json:"compaction_count"`
	CompactionTokenDelta int              `json:"compaction_token_delta"`
	CompactionModelCalls int              `json:"compaction_model_calls"`
	CompactionPrompt     int              `json:"compaction_prompt_tokens"`
	CompactionCompletion int              `json:"compaction_completion_tokens"`
}

func (value seed) snapshot(cursor Cursor) Snapshot {
	return Snapshot{
		SchemaVersion: SchemaVersion, Cursor: cursor, Complete: true,
		ID: value.ID, Label: value.Label, ServerID: value.ServerID, Workspace: value.Workspace,
		Run: value.Run, Tools: cloneTools(value.Tools), Messages: cloneMessages(value.Messages), Budget: value.Budget,
		QueuedMessages: value.QueuedMessages, Runnable: value.Runnable, NotRunnableReason: value.NotRunnableReason,
		MemoryPath: value.MemoryPath, MemoryContent: value.MemoryContent, LogPath: value.LogPath,
		ModelTurns: value.ModelTurns, CompactionCount: value.CompactionCount, CompactionTokenDelta: value.CompactionTokenDelta,
		CompactionModelCalls: value.CompactionModelCalls, CompactionPrompt: value.CompactionPrompt,
		CompactionCompletion: value.CompactionCompletion, Activity: Activity{CompletedStages: []string{}},
	}
}

func diff(before, after Snapshot) Patch {
	patch := Patch{SchemaVersion: SchemaVersion, SessionID: after.ID, Cursor: after.Cursor, Operations: []Operation{}}
	fields := []struct {
		path        string
		before, now any
	}{
		{"complete", before.Complete, after.Complete}, {"id", before.ID, after.ID}, {"label", before.Label, after.Label},
		{"server_id", before.ServerID, after.ServerID}, {"workspace", before.Workspace, after.Workspace}, {"run", before.Run, after.Run},
		{"tools", before.Tools, after.Tools}, {"messages", before.Messages, after.Messages}, {"budget", before.Budget, after.Budget},
		{"queued_messages", before.QueuedMessages, after.QueuedMessages}, {"runnable", before.Runnable, after.Runnable},
		{"not_runnable_reason", before.NotRunnableReason, after.NotRunnableReason}, {"memory_path", before.MemoryPath, after.MemoryPath},
		{"memory_content", before.MemoryContent, after.MemoryContent}, {"log_path", before.LogPath, after.LogPath},
		{"model_turns", before.ModelTurns, after.ModelTurns}, {"compaction_count", before.CompactionCount, after.CompactionCount},
		{"compaction_token_delta", before.CompactionTokenDelta, after.CompactionTokenDelta},
		{"compaction_model_calls", before.CompactionModelCalls, after.CompactionModelCalls},
		{"compaction_prompt_tokens", before.CompactionPrompt, after.CompactionPrompt},
		{"compaction_completion_tokens", before.CompactionCompletion, after.CompactionCompletion},
		{"activity", before.Activity, after.Activity}, {"closed", before.Closed, after.Closed},
	}
	for _, field := range fields {
		if reflect.DeepEqual(field.before, field.now) {
			continue
		}
		value, _ := json.Marshal(field.now)
		patch.Operations = append(patch.Operations, Operation{Op: "replace", Path: "/" + field.path, Value: value})
	}
	return patch
}

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
