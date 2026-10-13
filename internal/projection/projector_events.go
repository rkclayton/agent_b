package projection

import (
	"strconv"
	"strings"

	"harness/internal/events"
	workspaceinfo "harness/internal/workspace"
)

func prepareProjection(previous Snapshot, record Record) (Snapshot, map[string]any) {
	next := previous
	next.SchemaVersion = SchemaVersion
	next.Cursor = record.Cursor
	if next.ID == "" {
		next.ID = record.Event.SessionID
		next.Label = record.Event.SessionID
	}
	data := eventMap(record.Event.Data)
	if value := data["_retained"]; value != nil {
		var kept struct {
			Chat    ChatEntry        `json:"chat"`
			Stream  *StreamTelemetry `json:"stream"`
			Partial string           `json:"partial"`
		}
		if decode(value, &kept) == nil {
			next.Activity.Stream, next.Run.Partial = kept.Stream, kept.Partial
			if kept.Chat.Key != "" && chatTurn(next.Chat, kept.Chat.RunID, kept.Chat.Turn) != nil {
				next.Chat = cloneChat(next.Chat)
				*chatTurn(next.Chat, kept.Chat.RunID, kept.Chat.Turn) = kept.Chat
			}
		}
	}
	return next, data
}

func finishProjection(next Snapshot, record Record, data map[string]any, live bool) Snapshot {
	appendNotice := chatNotice(record.Event.Type)
	if appendNotice && record.Event.Type == events.Compaction && stringValue(data["trigger"]) == "byte_limit_trim" {
		appendNotice = intValue(data["trimmed_results"]) > 0
	}
	if appendNotice {
		event := stripDiagnostic(record.Event)
		role := ""
		if value := stringValue(data["role"]); value == "c" || value == "aux" {
			role = "c"
		}
		entry := ChatEntry{Type: "notice", Key: "event:" + strconv.FormatInt(record.Event.Seq, 10), RunID: record.Event.RunID, Event: &event, AgentRole: role}
		if record.Event.Type == events.RunStopped && stringValue(data["reason"]) == "model_unreachable" && next.ModelUnreachable != nil {
			entry.Text = next.ModelUnreachable.Host
			next.Chat = upsertModelUnreachableNotice(next.Chat, entry)
		} else {
			next.Chat = appendChat(next.Chat, entry)
		}
	}
	if record.Event.Type == events.RunStopped {
		next.Chat = cloneChat(next.Chat)
		for index := range next.Chat {
			if next.Chat[index].Type == "agent" && next.Chat[index].RunID == record.Event.RunID {
				next.Chat[index].Done = true
			}
		}
	}
	// Timeline is an unbounded durable operational view. Streaming fragments are
	// retained only while their turn is live; completed response content lives in
	// messages/model.response and cannot evict operational history.
	retainTimeline := record.Event.Type != events.ToolProgress && !(record.Event.Type == events.DelegatedUsage && boolValue(data["live"]))
	if retainTimeline {
		if live {
			next.Timeline = append(next.Timeline, stripDiagnostic(record.Event))
		} else {
			next.Timeline = append(append([]events.Event(nil), next.Timeline...), stripDiagnostic(record.Event))
		}
	}
	if record.Event.Type == events.ModelResponse {
		next.Timeline = discardStream(next.Timeline, record.Event.RunID, intValue(data["turn"]))
	} else if record.Event.Type == events.RunStopped {
		next.Timeline = discardRunStream(next.Timeline, record.Event.RunID)
	}
	next.Cursor = record.Cursor
	return next
}

func projectSessionRenamed(next Snapshot, data map[string]any) Snapshot {
	next.Label = stringValue(data["label"])
	if stringValue(data["by"]) == "user" {
		next.NamePinned = true
	}
	return next
}

func projectPolicyRevoked(next Snapshot) Snapshot {
	next.RepoPolicy = nil
	return next
}

func projectPolicyDenied(next Snapshot) Snapshot {
	next.PendingRepoPolicy = nil
	return next
}

func projectMemoryCleared(next Snapshot, data map[string]any) Snapshot {
	if strings.EqualFold(filepathClean(stringValue(data["dir"])), filepathClean(next.WorkspaceDir)) {
		next.MemoryContent = ""
	}
	return next
}

func projectSessionClosed(next Snapshot) Snapshot {
	next.Closed = true
	return next
}

func projectSessionReopened(next Snapshot) Snapshot {
	next.Closed = false
	next.RunAsYou = false
	return next
}

func projectRunQueued(next Snapshot, record Record, data map[string]any) Snapshot {
	next.Activity.StateStartedAt = eventMillis(record.Event)
	next.Run.Status = "queued"
	next.Run.RunID = firstString(data["run_id"], record.Event.RunID)
	next.Run.QueuePosition = intValue(data["position"])
	next.Run.WaitingBehind = stringValue(data["behind"])
	return next
}

func projectRunResumed(next Snapshot, record Record) Snapshot {
	next.Activity.StateStartedAt = eventMillis(record.Event)
	// an answered run took back the model slot it released on its
	// card; unlike run.started nothing about the run resets.
	next.Run.Status = "running"
	next.Run.QueuePosition = 0
	next.Run.WaitingBehind = ""
	return next
}

func projectRunStopping(next Snapshot, record Record) Snapshot {
	next.Activity.StateStartedAt = eventMillis(record.Event)
	next.Run.Status = "stopping"
	return next
}

func projectRunLabeled(next Snapshot, record Record, data map[string]any) Snapshot {
	if record.Event.RunID == next.Run.LastRunID {
		next.Run.ResultLabel = stringValue(data["label"])
	}
	return next
}

func projectModelUnreachable(next Snapshot, data map[string]any) Snapshot {
	next.ModelUnreachable = &ModelAvailability{Host: stringValue(data["host"]), Detail: stringValue(data["detail"])}
	next.ModelBusy = nil
	return next
}

func projectModelBusy(next Snapshot, data map[string]any) Snapshot {
	next.ModelBusy = &ModelAvailability{Host: stringValue(data["host"]), Detail: stringValue(data["detail"])}
	return next
}

func projectModelReachable(next Snapshot) Snapshot {
	if next.ModelUnreachable != nil {
		next.Chat = resolveModelUnreachableNotice(next.Chat, next.ModelUnreachable.Host)
	}
	next.ModelUnreachable = nil
	next.ModelBusy = nil
	return next
}

func projectShellGrant(next Snapshot, data map[string]any) Snapshot {
	return projectOperatorGrant(next, data, true)
}

func projectFileGrant(next Snapshot, data map[string]any) Snapshot {
	return projectOperatorGrant(next, data, true)
}

func projectShellGrantLapsed(next Snapshot, data map[string]any) Snapshot {
	return projectOperatorGrant(next, data, false)
}

func projectFileGrantLapsed(next Snapshot, data map[string]any) Snapshot {
	return projectOperatorGrant(next, data, false)
}

func projectOperatorGrant(next Snapshot, data map[string]any, granted bool) Snapshot {
	if stringValue(data["scope"]) == "session" && stringValue(data["identity"]) == "operator" {
		next.RunAsYou = granted
	}
	return next
}

func projectDelegatedUsage(next Snapshot, data map[string]any) Snapshot {
	if status := stringValue(data["status"]); status != "" {
		next.Activity.Delegate = &DelegateActivity{ID: stringValue(data["child_id"]), Status: status, ToolCalls: intValue(data["tool_calls"]), Stage: stringValue(data["stage"]), Tool: stringValue(data["tool"]), Target: stringValue(data["target"]), Turn: intValue(data["turn"])}
	} else if next.Activity.Delegate != nil {
		calls, _ := data["tool_calls"].([]any)
		next.Activity.Delegate = &DelegateActivity{ID: next.Activity.Delegate.ID, Status: next.Activity.Delegate.Status, ToolCalls: next.Activity.Delegate.ToolCalls + len(calls)}
	}
	return next
}

func projectModelRequest(next Snapshot, record Record, data map[string]any) Snapshot {
	at := eventMillis(record.Event)
	next.Activity.Stream = &StreamTelemetry{Key: turnKey(record.Event, data), StartedAt: at, LastChunkAt: at, RateStartedAt: at}
	next.Chat = appendChat(next.Chat, ChatEntry{Type: "agent", Key: "turn:" + turnKey(record.Event, data), RunID: record.Event.RunID, Turn: intValue(data["turn"]), AgentRole: "b"})
	return next
}

func projectModelProgress(next Snapshot, record Record, data map[string]any) Snapshot {
	next.Activity.Progress = cloneMap(data)
	next.Activity.Stream = touchStream(next.Activity.Stream, record.Event, data, false)
	return next
}

func projectToolCallEvent(next Snapshot, record Record, data map[string]any) Snapshot {
	next.Activity.StateStartedAt = eventMillis(record.Event)
	next.Activity.ActiveTool = stringValue(data["name"])
	args := mapValue(data["args"])
	next.Chat = appendChat(next.Chat, ChatEntry{Type: "tool", Key: "tool:" + stringValue(data["call_id"]), CallID: stringValue(data["call_id"]), Name: stringValue(data["name"]), Args: &args})
	return next
}

func projectToolToggled(next Snapshot, data map[string]any) Snapshot {
	next.Tools = cloneTools(next.Tools)
	for index := range next.Tools {
		if next.Tools[index].Name == stringValue(data["name"]) {
			next.Tools[index].Enabled = boolValue(data["enabled"])
			break
		}
	}
	return next
}

func projectApprovalRequired(next Snapshot, record Record) Snapshot {
	next.Activity.StateStartedAt = eventMillis(record.Event)
	next.Run.Status = "paused"
	event := stripDiagnostic(record.Event)
	next.PendingApproval = &ChatEntry{Type: "notice", Key: "event:" + strconv.FormatInt(record.Event.Seq, 10), RunID: record.Event.RunID, Event: &event}
	return next
}

func projectCycleDetected(next Snapshot) Snapshot {
	next.Activity.DispatchAlarm = true
	return next
}

func projectWorkspaceConflict(next Snapshot) Snapshot {
	next.Activity.AlarmTool = next.Activity.ActiveTool
	return next
}

func projectCompactionSummary(next, previous Snapshot, record Record) (Snapshot, error) {
	var attempt events.CompactionSummaryData
	if err := decode(record.Event.Data, &attempt); err != nil {
		return previous, err
	}
	if attempt.Dispatched {
		next.CompactionModelCalls++
		next.CompactionPrompt += attempt.Usage.PromptTokens
		next.CompactionCompletion += attempt.Usage.CompletionTokens
	}
	return next, nil
}

func projectMemoryNoted(next Snapshot, data map[string]any) Snapshot {
	if stringValue(data["target"]) == "agent" {
		next.AgentMemoryPath = firstString(data["path"], next.AgentMemoryPath)
		next.AgentMemoryContent = strings.TrimRight(next.AgentMemoryContent, "\r\n") + "\n- " + stringValue(data["note"]) + "\n"
	} else {
		next.MemoryPath = firstString(data["path"], next.MemoryPath)
		next.MemoryContent = strings.TrimRight(next.MemoryContent, "\r\n") + "\n- " + stringValue(data["note"]) + "\n"
	}
	return next
}

func projectSessionCreated(next, previous Snapshot, record Record) (Snapshot, error) {
	var wrapper struct {
		Session seed        `json:"session"`
		Chat    []ChatEntry `json:"chat"`
	}
	if err := decode(record.Event.Data, &wrapper); err != nil {
		return previous, err
	}
	next = wrapper.Session.snapshot(record.Cursor)
	if record.Event.SessionID != "" {
		next.ID = record.Event.SessionID
	}
	// a retained chat restored at startup is announced again with
	// its original created_at. It keeps the transcript already projected
	// from its earlier segments; reseeding it empty made a chat with twenty
	// messages of model context look like a new one.
	if len(wrapper.Chat) > 0 {
		next.Chat = wrapper.Chat
	} else if previous.ID == next.ID && previous.CreatedAt != "" && previous.CreatedAt == next.CreatedAt {
		next.Chat, next.Timeline = previous.Chat, previous.Timeline
	}
	return next, nil
}

func projectSessionUpdated(next Snapshot, record Record, data map[string]any) Snapshot {
	if value := stringValue(data["workspace"]); value != "" {
		next.Workspace, next.WorkspaceDir, next.WorkspaceMissing = value, value, false
	}
	next.AgentID = firstString(stringValue(data["agent_id"]), next.AgentID)
	next.ConnectionID = firstString(connectionField(data, "connection_id"), next.ConnectionID)
	next.Role = firstString(stringValue(data["role"]), next.Role)
	next.PlanID = firstString(stringValue(data["plan_id"]), next.PlanID)
	next.PlanName = firstString(stringValue(data["plan_name"]), next.PlanName)
	next.PlanDir = firstString(stringValue(data["plan_dir"]), next.PlanDir)
	if value := stringValue(data["agent_name"]); value != "" {
		next.AgentName = value
	}
	if value := connectionField(data, "b_connection"); value != "" {
		next.BConnection = value
	}
	if _, ok := data["runnable"]; ok {
		next.Runnable = boolValue(data["runnable"])
	}
	if _, ok := data["not_runnable_reason"]; ok {
		next.NotRunnableReason = stringValue(data["not_runnable_reason"])
	}
	if !next.Runnable && strings.TrimSpace(next.NotRunnableReason) == "" {
		next.NotRunnableReason = "chat is not runnable"
	}
	if _, ok := data["memory_path"]; ok {
		next.MemoryPath = stringValue(data["memory_path"])
	}
	if _, ok := data["memory_content"]; ok {
		next.MemoryContent = stringValue(data["memory_content"])
	}
	if value := stringValue(data["owner"]); value != "" {
		next.Owner = value
	}
	if notice := stringValue(data["notice"]); notice != "" {
		next.Chat = appendChat(next.Chat, ChatEntry{Type: "notice", Key: "event:" + strconv.FormatInt(record.Event.Seq, 10), Text: notice})
	}
	return next
}

func projectProjectInstructions(next Snapshot, data map[string]any) Snapshot {
	if boolValue(data["lazy"]) {
		if block := stringValue(data["block"]); block != "" {
			if next.ProjectContent != "" {
				next.ProjectContent += "\n\n"
			}
			next.ProjectContent += block
		}
		next.ProjectFiles = appendUniqueStrings(next.ProjectFiles, stringSlice(data["files"])...)
		next.ProjectNotes = append(next.ProjectNotes, stringSlice(data["notes"])...)
		next.ProjectRoots = appendUniqueStrings(next.ProjectRoots, stringSlice(data["roots"])...)
	}
	return next
}

func projectPolicyApproved(next Snapshot, record Record, data map[string]any) Snapshot {
	var policy workspaceinfo.PolicyState
	if decode(record.Event.Data, &policy) == nil {
		next.RepoPolicy = &policy
		next.PendingRepoPolicy = nil
	}
	if data["tools"] != nil {
		var tools []Tool
		if decode(data["tools"], &tools) == nil {
			next.Tools = tools
		}
	}
	return next
}

func projectWorkspaceBound(next Snapshot, data map[string]any) Snapshot {
	next.Workspace = stringValue(data["workspace_dir"])
	next.WorkspaceDir = next.Workspace
	next.WorkspaceMissing = boolValue(data["workspace_missing"])
	next.ProjectContent = stringValue(data["project_content"])
	next.ProjectFiles = stringSlice(data["project_files"])
	next.ProjectNotes = stringSlice(data["project_notes"])
	next.MemoryPath = stringValue(data["memory_path"])
	next.MemoryContent = stringValue(data["memory_content"])
	_ = decode(data["pending_repo_policy"], &next.PendingRepoPolicy)
	_ = decode(data["repo_policy"], &next.RepoPolicy)
	if data["tools"] != nil {
		_ = decode(data["tools"], &next.Tools)
	}
	return next
}

func projectSessionReset(next Snapshot, data map[string]any) Snapshot {
	next.Messages = []events.Message{}
	next.Tools = cloneTools(next.Tools)
	for index := range next.Tools {
		next.Tools[index].Calls = 0
	}
	next.QueuedMessages = 0
	next.QueuedMessageIDs = nil
	next.ModelTurns = 0
	next.CompactionCount = 0
	next.CompactionTokenDelta = 0
	next.CompactionModelCalls = 0
	next.CompactionPrompt = 0
	next.CompactionCompletion = 0
	next.Run = Run{Status: "idle", MaxTurns: next.Run.MaxTurns}
	next.Activity = Activity{CompletedStages: []string{}}
	next.Timeline = []events.Event{}
	next.Chat = []ChatEntry{}
	next.PendingApproval = nil
	if value := stringValue(data["log_path"]); value != "" {
		next.LogPath = value
	}
	return next
}

func projectRunStarted(next Snapshot, record Record, data map[string]any) Snapshot {
	next.Activity.StartedAt = eventMillis(record.Event)
	next.Activity.StateStartedAt = next.Activity.StartedAt
	next.Run.Status = "running"
	next.Run.RunID = firstString(data["run_id"], record.Event.RunID)
	next.Run.Turn = 0
	next.Run.QueuePosition = 0
	next.Run.WaitingBehind = ""
	next.Run.Partial = ""
	next.Run.LastStopReason = ""
	next.Run.LastStopDetail = ""
	next.Run.LastRunID = ""
	next.Run.ResultLabel = ""
	next.QueuedMessages = max(0, next.QueuedMessages-1)
	// The run that starts takes its own message out of the queue. Removing it by
	// id rather than from the front keeps the two in step even when a run starts
	// out of order.
	next.QueuedMessageIDs = withoutQueuedID(next.QueuedMessageIDs, stringValue(data["user_message_id"]))
	next.Activity.DispatchAlarm = false
	return next
}

func projectRunStopped(next Snapshot, record Record, data map[string]any) Snapshot {
	next.Activity.StateStartedAt = eventMillis(record.Event)
	next.ModelBusy = nil
	status := "idle"
	if boolValue(data["queue_held"]) {
		status = "held"
	}
	next.Run = Run{Status: status, MaxTurns: next.Run.MaxTurns, QueuePosition: next.QueuedMessages, LastStopReason: stringValue(data["reason"]), LastStopDetail: stringValue(data["detail"]), LastRunID: record.Event.RunID}
	next.Activity.Stage = "wait_user"
	next.Activity.StageState = "enter"
	next.Activity.ActiveTool = ""
	if stringValue(data["reason"]) == "tool_errors" {
		next.Activity.DispatchAlarm = true
	}
	if next.PendingApproval != nil && next.PendingApproval.RunID == record.Event.RunID {
		next.PendingApproval = nil
	}
	return next
}

func projectStage(next Snapshot, record Record, data map[string]any) Snapshot {
	stage, state := stringValue(data["stage"]), stringValue(data["state"])
	if stage != next.Activity.Stage || state != next.Activity.StageState {
		next.Activity.StateStartedAt = eventMillis(record.Event)
	}
	next.Run.Turn = intValue(data["turn"])
	next.Activity.StageState = state
	if stage == "compact" {
		next.Activity.CompactionReason = stringValue(data["reason"])
	}
	if state == "enter" {
		next.Activity.Stage = stage
		if stage == "assemble" {
			next.Activity.CompletedStages = []string{}
		}
	} else if !contains(next.Activity.CompletedStages, stage) {
		next.Activity.CompletedStages = appendCopy(next.Activity.CompletedStages, stage)
	}
	return next
}

func projectModelDelta(next Snapshot, record Record, data map[string]any, live bool) Snapshot {
	next.Activity.Stream = touchStream(next.Activity.Stream, record.Event, data, true)
	// a delta mutates exactly ONE entry — the turn it belongs to
	// — so only that entry is copied. Deep-copying the whole transcript
	// per delta was the rest of the startup cost once the JSON round-trip
	// was gone. The other entries are shared with the previous snapshot
	// and are never mutated without being cloned first, so Next stays pure.
	var entry *ChatEntry
	if live {
		entry = chatTurn(next.Chat, record.Event.RunID, intValue(data["turn"]))
	} else {
		next.Chat, entry = chatWithMutableTurn(next.Chat, record.Event.RunID, intValue(data["turn"]))
	}
	if entry != nil {
		if stringValue(data["kind"]) == "reasoning" {
			if entry.ThinkingStartedMS == 0 {
				entry.ThinkingStartedMS = eventMillis(record.Event)
			}
			entry.Reasoning += stringValue(data["text"])
		}
		if stringValue(data["kind"]) == "content" {
			if entry.ThinkingStartedMS > 0 && entry.ThinkingEndedMS == 0 {
				entry.ThinkingEndedMS = eventMillis(record.Event)
			}
			entry.Text += stringValue(data["text"])
		}
	}
	if stringValue(data["kind"]) == "content" {
		next.Run.Partial += stringValue(data["text"])
	}
	return next
}

func projectModelResponse(next Snapshot, record Record, data map[string]any) Snapshot {
	next.ModelBusy = nil
	next.Run.Partial = ""
	next.ModelTurns++
	next.Activity.LastTimings = mapValue(data["timings"])
	next.Activity.Stream = touchStream(next.Activity.Stream, record.Event, data, false)
	next.Activity.Stream.Done = true
	next.Activity.Stream.ReasoningTokens = intValue(data["reasoning_tokens"])
	next.Activity.Stream.Timings = mapValue(data["timings"])
	// A response changes several fields on one entry. It is one event per
	// turn, not the streamed-token path, and must not mutate a snapshot a
	// reader may already hold.
	next.Chat = cloneChat(next.Chat)
	entry := chatTurn(next.Chat, record.Event.RunID, intValue(data["turn"]))
	if entry == nil {
		next.Chat = append(next.Chat, ChatEntry{Type: "agent", Key: "turn:" + turnKey(record.Event, data), RunID: record.Event.RunID, Turn: intValue(data["turn"]), AgentRole: "b"})
		entry = &next.Chat[len(next.Chat)-1]
	}
	if content := stringValue(data["content"]); content != "" {
		entry.Text = content
	}
	entry.ReasoningTokens = intValue(data["reasoning_tokens"])
	entry.ReasoningTokensEstimated = boolValue(data["reasoning_tokens_estimated"])
	entry.Done = true
	if entry.ThinkingStartedMS > 0 {
		if entry.ThinkingEndedMS == 0 {
			entry.ThinkingEndedMS = eventMillis(record.Event)
		}
		duration := entry.ThinkingEndedMS - entry.ThinkingStartedMS
		entry.ThinkingMS = &duration
	}
	for _, call := range toolCalls(data["tool_calls"]) {
		entry.ToolCallIDs = append(entry.ToolCallIDs, call.ID)
	}
	return next
}

func projectToolProgress(next Snapshot, record Record, data map[string]any) Snapshot {
	if boolValue(data["run_started"]) && next.Activity.StartedAt == 0 {
		next.Activity.StartedAt = eventMillis(record.Event)
		next.Activity.StateStartedAt = next.Activity.StartedAt
	}
	if boolValue(data["tool_started"]) {
		next.Activity.ToolStartedAt = eventMillis(record.Event)
		next.Activity.StateStartedAt = next.Activity.ToolStartedAt
		next.Activity.ToolTarget = shortToolTarget(mapValue(data["args"]))
		next.Activity.ToolLastLine = ""
	}
	if line := stringValue(data["line"]); line != "" {
		next.Activity.ToolLastLine = line
	}
	return next
}

func projectToolResult(next Snapshot, record Record, data map[string]any, live bool) Snapshot {
	next.Activity.StateStartedAt = eventMillis(record.Event)
	next.Activity.ActiveTool = ""
	next.Activity.ToolStartedAt, next.Activity.ToolTarget, next.Activity.ToolLastLine = 0, "", ""
	next.Tools = cloneTools(next.Tools)
	for index := range next.Tools {
		if next.Tools[index].Name == stringValue(data["name"]) {
			next.Tools[index].Calls++
			break
		}
	}
	// Store owns its live snapshot. A tool result changes one already-indexable
	// row, so copying every retained chat would make this event grow with history.
	if !live {
		next.Chat = cloneChat(next.Chat)
	}
	if entry := chatCall(next.Chat, stringValue(data["call_id"])); entry != nil {
		entry.Result = cloneMap(data)
		entry.Content = stringValue(data["preview"])
	}
	return next
}

func projectMessageAppended(next, previous Snapshot, record Record) (Snapshot, error) {
	var wrapper struct {
		Message events.Message `json:"message"`
	}
	if err := decode(record.Event.Data, &wrapper); err != nil {
		return previous, err
	}
	next.Messages = cloneMessages(next.Messages)
	if wrapper.Message.Category == "summary" {
		at := min(1, len(next.Messages))
		if strings.HasPrefix(wrapper.Message.Content, "Fresh-context hand-off:\n") {
			at = 0
		}
		next.Messages = append(next.Messages[:at], append([]events.Message{wrapper.Message}, next.Messages[at:]...)...)
	} else {
		next.Messages = append(next.Messages, wrapper.Message)
	}
	next.Chat = cloneChat(next.Chat)
	if wrapper.Message.Category == "summary" && !strings.HasPrefix(wrapper.Message.Content, "Fresh-context hand-off:\n") {
		next.Chat = append(next.Chat, ChatEntry{Type: "summary", Key: "message:" + wrapper.Message.ID, Text: summaryTranscript(wrapper.Message.Content)})
	} else if wrapper.Message.Role == "user" {
		next.Chat = append(next.Chat, ChatEntry{Type: "user", Key: "message:" + wrapper.Message.ID, Text: wrapper.Message.Content, Attachments: append([]events.Attachment(nil), wrapper.Message.Attachments...)})
	}
	if wrapper.Message.Role == "assistant" {
		if entry := chatTurnAny(next.Chat, wrapper.Message.Turn); entry != nil {
			if wrapper.Message.Content != "" {
				entry.Text = wrapper.Message.Content
			}
			entry.Reasoning = wrapper.Message.Reasoning
			entry.ToolCallIDs = entry.ToolCallIDs[:0]
			for _, call := range wrapper.Message.ToolCalls {
				entry.ToolCallIDs = append(entry.ToolCallIDs, call.ID)
			}
			entry.Done = true
		}
	}
	if wrapper.Message.Role == "tool" {
		if entry := chatCall(next.Chat, wrapper.Message.ToolCallID); entry != nil {
			entry.Content = wrapper.Message.Content
		}
	}
	return next, nil
}

func projectMessageUpdated(next Snapshot, data map[string]any) Snapshot {
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
		if value, ok := patch["tool_calls"]; ok {
			next.Messages[index].ToolCalls = toolCalls(value)
			next.Chat = cloneChat(next.Chat)
			if entry := chatTurnAny(next.Chat, next.Messages[index].Turn); entry != nil {
				entry.ToolCallIDs = nil
			}
		}
	}
	return next
}

func projectMessagesReminted(next Snapshot, data map[string]any) Snapshot {
	// rule 7: restore gave later copies of a repeated id new ids.
	// Each change names its position, so only that copy is renamed, and a
	// replay applies it once and never needs it again.
	next.Messages = cloneMessages(next.Messages)
	changes, _ := data["reminted"].([]any)
	for _, raw := range changes {
		change := eventMap(raw)
		index := intValue(change["index"])
		if index >= 0 && index < len(next.Messages) && next.Messages[index].ID == stringValue(change["from"]) {
			next.Messages[index].ID = stringValue(change["to"])
		}
	}
	return next
}

func projectMessageRemoved(next Snapshot, data map[string]any) Snapshot {
	id := stringValue(data["id"])
	next.Messages = cloneMessages(next.Messages)
	removed := events.Message{}
	kept := next.Messages[:0]
	for _, message := range next.Messages {
		if message.ID == id {
			removed = message
			continue
		}
		kept = append(kept, message)
	}
	next.Messages = kept
	next.Chat = cloneChat(next.Chat)
	if removed.Category == "summary" || removed.Role == "user" {
		next.Chat = removeChatKey(next.Chat, "message:"+removed.ID)
	} else if removed.Role == "tool" {
		next.Chat = removeChatKey(next.Chat, "tool:"+removed.ToolCallID)
	} else if removed.Role == "assistant" {
		next.Chat = removeAssistantTurn(next.Chat, removed.Turn)
	}
	return next
}

func projectMessageQueued(next Snapshot, data map[string]any) Snapshot {
	position := intValue(data["position"])
	if position == 0 {
		next.QueuedMessages = max(0, next.QueuedMessages-1)
		next.QueuedMessageIDs = withoutQueuedID(next.QueuedMessageIDs, stringValue(data["message_id"]))
		return next
	}
	next.QueuedMessages++
	// the id, so a restart can rebuild the queue and not just
	// its length.
	if id := stringValue(data["message_id"]); id != "" {
		next.QueuedMessageIDs = append(append([]string(nil), next.QueuedMessageIDs...), id)
	}
	return next
}

func projectBudgetEvent(next, previous Snapshot, record Record) (Snapshot, error) {
	var budget events.Budget
	if err := decode(record.Event.Data, &budget); err != nil {
		return previous, err
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
	return next, nil
}

func projectApprovalDecided(next Snapshot, record Record, data map[string]any) Snapshot {
	next.Activity.StateStartedAt = eventMillis(record.Event)
	decision := stringValue(data["decision"])
	if decision != "superseded" && decision != "dismissed" {
		next.Run.Status = "running"
	}
	next.Chat = cloneChat(next.Chat)
	for index := len(next.Chat) - 1; index >= 0; index-- {
		if next.Chat[index].Type == "notice" && next.Chat[index].Event != nil && stringValue(eventMap(next.Chat[index].Event.Data)["call_id"]) == stringValue(data["call_id"]) {
			next.Chat[index].Decision = stringValue(data["decision"])
			// he reads in the morning what was asked and why it was refused.
			if data["unanswered"] == true {
				next.Chat[index].Decision = "unanswered"
			}
			break
		}
	}
	if next.PendingApproval != nil && next.PendingApproval.Event != nil && stringValue(eventMap(next.PendingApproval.Event.Data)["call_id"]) == stringValue(data["call_id"]) {
		next.PendingApproval = nil
	}
	return next
}

func projectCompaction(next Snapshot, data map[string]any) Snapshot {
	next.Activity.CompactionSerial++
	next.CompactionCount++
	next.CompactionTokenDelta += intValue(data["after"]) - intValue(data["before"])
	// The meter follows the compaction immediately rather than waiting for
	// the next measured request. The freed tokens come off the compactible
	// categories only; the fixed prefix is a floor, never zero.
	next.Budget = compactedBudget(next.Budget, intValue(data["before"]), intValue(data["after"]))
	affected := map[string]bool{}
	for _, id := range stringValues(data["affected_ids"]) {
		affected[id] = true
	}
	if len(affected) > 0 {
		next.Chat = cloneChat(next.Chat)
		for _, message := range next.Messages {
			if !affected[message.ID] || message.Role != "tool" {
				continue
			}
			if entry := chatCall(next.Chat, message.ToolCallID); entry != nil && entry.Result != nil {
				entry.Result = cloneMap(entry.Result)
				entry.Result["model_shortened"] = true
				entry.Result["model_kept_bytes"] = 0
			}
		}
	}
	var cuts []map[string]any
	if decode(data["trimmed_calls"], &cuts) == nil && len(cuts) > 0 {
		next.Chat = cloneChat(next.Chat)
		for _, cut := range cuts {
			if entry := chatCall(next.Chat, stringValue(cut["call_id"])); entry != nil && entry.Result != nil {
				entry.Result = cloneMap(entry.Result)
				entry.Result["model_shortened"] = true
				entry.Result["model_kept_bytes"] = intValue(cut["kept_bytes"])
				entry.Result["model_removed_bytes"] = intValue(cut["removed_bytes"])
			}
		}
	}
	if kind := stringValue(data["kind"]); kind == "summarize" || kind == "fresh" {
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
	return next
}
