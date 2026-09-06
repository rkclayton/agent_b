// Command projectiondiff compares the new durable projector with the legacy Go
// replay reducer and live Session.Snapshot assembly. It is a staging tool and is
// not linked into the harness binary.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"harness/internal/events"
	"harness/internal/projection"
	"harness/internal/session"
)

type disagreement struct {
	Path       string `json:"path"`
	Consumer   string `json:"consumer"`
	Field      string `json:"field"`
	Generation string `json:"generation"`
	Offset     int64  `json:"offset"`
	Seq        int64  `json:"seq"`
	EventType  string `json:"event_type"`
	Projected  string `json:"projected"`
	Legacy     string `json:"legacy"`
}

type report struct {
	Files         int            `json:"files"`
	Records       int            `json:"records"`
	Incomplete    []string       `json:"incomplete_logs"`
	Disagreements []disagreement `json:"disagreements"`
}

type core struct {
	ID                   string            `json:"id"`
	Label                string            `json:"label"`
	ServerID             string            `json:"server_id"`
	Workspace            string            `json:"workspace"`
	Run                  projection.Run    `json:"run"`
	Tools                []projection.Tool `json:"tools"`
	Messages             []events.Message  `json:"messages"`
	Budget               events.Budget     `json:"budget"`
	QueuedMessages       int               `json:"queued_messages"`
	Runnable             bool              `json:"runnable"`
	NotRunnableReason    string            `json:"not_runnable_reason"`
	MemoryPath           string            `json:"memory_path"`
	MemoryContent        string            `json:"memory_content"`
	ModelTurns           int               `json:"model_turns"`
	CompactionCount      int               `json:"compaction_count"`
	CompactionTokenDelta int               `json:"compaction_token_delta"`
	CompactionModelCalls int               `json:"compaction_model_calls"`
	CompactionPrompt     int               `json:"compaction_prompt_tokens"`
	CompactionCompletion int               `json:"compaction_completion_tokens"`
}

type issueCount struct {
	Consumer  string `json:"consumer"`
	Field     string `json:"field"`
	EventType string `json:"event_type"`
	Count     int    `json:"count"`
}

type summaryReport struct {
	Files       int          `json:"files"`
	Records     int          `json:"records"`
	Incomplete  []string     `json:"incomplete_logs"`
	IssueCounts []issueCount `json:"issue_counts"`
}

type browserTrace struct {
	Path     string            `json:"path"`
	Cursor   projection.Cursor `json:"cursor"`
	Event    events.Event      `json:"event"`
	Complete bool              `json:"complete"`
	Fields   map[string]string `json:"fields"`
}

func main() {
	summaryOnly := flag.Bool("summary", false, "print grouped disagreement counts")
	traceBrowser := flag.Bool("browser-trace", false, "stream projected field hashes and source events as NDJSON")
	flag.Parse()
	paths, err := collect(flag.Args())
	if err != nil {
		fatal(err)
	}
	if *traceBrowser {
		if err := writeBrowserTrace(paths); err != nil {
			fatal(err)
		}
		return
	}
	result := report{Incomplete: []string{}, Disagreements: []disagreement{}}
	for _, path := range paths {
		records, _, readErr := projection.ReadFile(path, 0)
		if readErr != nil {
			fatal(fmt.Errorf("%s: %w", path, readErr))
		}
		if !sessionLog(records) {
			continue
		}
		result.Files++
		result.Records += len(records)
		compareFile(path, records, &result)
	}
	var output any = result
	if *summaryOnly {
		output = summarize(result)
	}
	encoded, _ := json.MarshalIndent(output, "", "  ")
	fmt.Println(string(encoded))
}

func writeBrowserTrace(paths []string) error {
	output := bufio.NewWriterSize(os.Stdout, 256*1024)
	defer output.Flush()
	encoder := json.NewEncoder(output)
	for _, path := range paths {
		records, _, err := projection.ReadFile(path, 0)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if !sessionLog(records) {
			continue
		}
		state := projection.Empty(firstSessionID(records))
		for _, record := range records {
			state, _, err = projection.Next(state, record)
			if err != nil {
				return fmt.Errorf("%s byte %d: %w", path, record.Cursor.Offset, err)
			}
			values := fields(projectionCore(state))
			values["activity"] = state.Activity
			hashes := make(map[string]string, len(values))
			for name, value := range values {
				encoded, marshalErr := canonicalJSON(value)
				if marshalErr != nil {
					return marshalErr
				}
				hashes[name] = fmt.Sprintf("%x", sha256.Sum256(encoded))
			}
			if err := encoder.Encode(browserTrace{Path: path, Cursor: record.Cursor, Event: record.Event, Complete: state.Complete, Fields: hashes}); err != nil {
				return err
			}
		}
	}
	return nil
}

func canonicalJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		return nil, err
	}
	return json.Marshal(generic)
}

func summarize(value report) summaryReport {
	counts := map[string]issueCount{}
	for _, item := range value.Disagreements {
		key := item.Consumer + "\x00" + item.Field + "\x00" + item.EventType
		count := counts[key]
		count.Consumer, count.Field, count.EventType = item.Consumer, item.Field, item.EventType
		count.Count++
		counts[key] = count
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	issues := make([]issueCount, 0, len(keys))
	for _, key := range keys {
		issues = append(issues, counts[key])
	}
	return summaryReport{Files: value.Files, Records: value.Records, Incomplete: value.Incomplete, IssueCounts: issues}
}

func compareFile(path string, records []projection.Record, result *report) {
	id := firstSessionID(records)
	projected := projection.Empty(id)
	replayState := initialReplay(path, id, records)
	replaySessions := map[string]events.ReplaySession{id: replayState}
	live := newLegacyLive(records)
	active := map[string]bool{}
	completeSeen := false
	for _, record := range records {
		var err error
		projected, _, err = projection.Next(projected, record)
		if err != nil {
			fatal(fmt.Errorf("%s byte %d: %w", path, record.Cursor.Offset, err))
		}
		completeSeen = completeSeen || projected.Complete
		events.ReduceReplay(replaySessions, record.Event)
		compareStates(path, "ReduceReplay", record, projectionCore(projected), replayCore(replaySessions[id]), active, result)
		if live != nil {
			live.apply(record.Event)
			compareStates(path, "Session.Snapshot", record, projectionCore(projected), live.core(), active, result)
		}
	}
	if !completeSeen {
		result.Incomplete = append(result.Incomplete, path)
	}
}

func compareStates(path, consumer string, record projection.Record, expected, actual core, active map[string]bool, result *report) {
	left := fields(expected)
	right := fields(actual)
	keys := make([]string, 0, len(left))
	for key := range left {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, field := range keys {
		key := consumer + ":" + field
		different := !reflect.DeepEqual(left[field], right[field])
		if different && !active[key] {
			result.Disagreements = append(result.Disagreements, disagreement{
				Path: path, Consumer: consumer, Field: field,
				Generation: record.Cursor.Generation, Offset: record.Cursor.Offset,
				Seq: record.Event.Seq, EventType: record.Event.Type,
				Projected: compact(left[field]), Legacy: compact(right[field]),
			})
		}
		active[key] = different
	}
}

func fields(value core) map[string]any {
	return map[string]any{
		"identity":                    []any{value.ID, value.Label, value.ServerID, value.Workspace},
		"run.status":                  value.Run.Status,
		"run.run_id":                  value.Run.RunID,
		"run.turn":                    value.Run.Turn,
		"run.max_turns":               value.Run.MaxTurns,
		"run.queue_position":          value.Run.QueuePosition,
		"run.partial":                 value.Run.Partial,
		"run.last_stop_reason":        value.Run.LastStopReason,
		"tools.order":                 toolField(value.Tools, func(tool projection.Tool) any { return tool.Name }),
		"tools.enabled":               toolField(value.Tools, func(tool projection.Tool) any { return tool.Enabled }),
		"tools.calls":                 toolField(value.Tools, func(tool projection.Tool) any { return tool.Calls }),
		"tools.schema_tokens":         toolField(value.Tools, func(tool projection.Tool) any { return tool.SchemaTokens }),
		"tools.marginal_tokens":       toolField(value.Tools, func(tool projection.Tool) any { return tool.MarginalTokens }),
		"messages":                    emptySlice(value.Messages),
		"budget.n_ctx":                value.Budget.NCtx,
		"budget.reserve":              value.Budget.Reserve,
		"budget.ceiling":              value.Budget.Ceiling,
		"budget.used_est":             value.Budget.UsedEst,
		"budget.used_measured":        value.Budget.UsedMeasured,
		"budget.drift":                value.Budget.Drift,
		"budget.cached_last":          value.Budget.CachedLast,
		"budget.mode":                 value.Budget.Mode,
		"budget.estimated":            value.Budget.Estimated,
		"budget.estimated_categories": emptySlice(value.Budget.EstimatedCategories),
		"budget.categories":           emptyMap(value.Budget.Categories),
		"budget.tool_schema_tokens":   emptyMap(value.Budget.ToolSchemaTokens),
		"budget.tool_marginal_tokens": emptyMap(value.Budget.ToolMarginalTokens),
		"queued_messages":             value.QueuedMessages,
		"runnable":                    []any{value.Runnable, value.NotRunnableReason},
		"memory":                      []any{value.MemoryPath, value.MemoryContent},
		"model_turns":                 value.ModelTurns,
		"compaction":                  []int{value.CompactionCount, value.CompactionTokenDelta, value.CompactionModelCalls, value.CompactionPrompt, value.CompactionCompletion},
	}
}

func toolField(values []projection.Tool, extract func(projection.Tool) any) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		result = append(result, []any{value.Name, extract(value)})
	}
	return result
}

func projectionCore(value projection.Snapshot) core {
	return core{
		ID: value.ID, Label: value.Label, ServerID: value.ServerID, Workspace: value.Workspace,
		Run: value.Run, Tools: value.Tools, Messages: value.Messages, Budget: value.Budget,
		QueuedMessages: value.QueuedMessages, Runnable: value.Runnable, NotRunnableReason: value.NotRunnableReason,
		MemoryPath: value.MemoryPath, MemoryContent: value.MemoryContent, ModelTurns: value.ModelTurns,
		CompactionCount: value.CompactionCount, CompactionTokenDelta: value.CompactionTokenDelta,
		CompactionModelCalls: value.CompactionModelCalls, CompactionPrompt: value.CompactionPrompt,
		CompactionCompletion: value.CompactionCompletion,
	}
}

func replayCore(value events.ReplaySession) core {
	tools := make([]projection.Tool, len(value.Tools))
	for index, tool := range value.Tools {
		tools[index] = projection.Tool{Name: tool.Name, Enabled: tool.Enabled, Calls: tool.Calls, SchemaTokens: tool.SchemaTokens, MarginalTokens: tool.MarginalTokens}
	}
	return core{
		ID: value.ID, Label: value.Label, ServerID: value.ServerID, Workspace: value.Workspace,
		Run:   projection.Run{Status: value.Run.Status, RunID: value.Run.RunID, Turn: value.Run.Turn, MaxTurns: value.Run.MaxTurns, QueuePosition: value.Run.QueuePosition, Partial: value.Run.Partial, LastStopReason: value.Run.LastStopReason},
		Tools: tools, Messages: value.Messages, Budget: value.Budget, QueuedMessages: value.QueuedMessages,
		Runnable: value.Runnable, NotRunnableReason: value.NotRunnableReason, MemoryPath: value.MemoryPath,
		MemoryContent: value.MemoryContent, ModelTurns: value.ModelTurns, CompactionCount: value.CompactionCount,
		CompactionTokenDelta: value.CompactionTokenDelta, CompactionModelCalls: value.CompactionModelCalls,
		CompactionPrompt: value.CompactionPrompt, CompactionCompletion: value.CompactionCompletion,
	}
}

func initialReplay(path, id string, records []projection.Record) events.ReplaySession {
	initial := events.ReplaySession{ID: id, Label: id, Run: events.ReplayRun{Status: "replay"}, Runnable: true, Messages: []events.Message{}, Tools: []events.ReplayTool{}, Timeline: []events.Event{}}
	for _, record := range records {
		if record.Event.Type != events.SessionCreated {
			continue
		}
		var wrapper struct {
			Session events.ReplaySession `json:"session"`
		}
		if decode(record.Event.Data, &wrapper) == nil {
			initial = wrapper.Session
			initial.ID = id
			initial.Run.Status = "replay"
			initial.Messages = []events.Message{}
			initial.Timeline = []events.Event{}
			initial.LogPath = path
		}
	}
	return initial
}

type legacyLive struct {
	item *session.Session
	bus  *events.Bus
}

func newLegacyLive(records []projection.Record) *legacyLive {
	for _, record := range records {
		if record.Event.Type != events.SessionCreated {
			continue
		}
		var wrapper struct {
			Session session.Snapshot `json:"session"`
		}
		if decode(record.Event.Data, &wrapper) != nil {
			return nil
		}
		return &legacyLive{item: sessionFromSnapshot(wrapper.Session), bus: events.NewBus()}
	}
	return nil
}

func sessionFromSnapshot(value session.Snapshot) *session.Session {
	enabled, calls, schemas, marginals := map[string]bool{}, map[string]int{}, map[string]int{}, map[string]int{}
	for _, tool := range value.Tools {
		enabled[tool.Name], calls[tool.Name], schemas[tool.Name], marginals[tool.Name] = tool.Enabled, tool.Calls, tool.SchemaTokens, tool.MarginalTokens
	}
	item := &session.Session{
		ID: value.ID, Label: value.Label, ServerID: value.ServerID, Workspace: value.Workspace,
		Messages: append([]events.Message(nil), value.Messages...), Budget: value.Budget,
		Run: value.Run, ToolsEnabled: enabled, ToolCalls: calls, LastSeen: map[string]time.Time{},
		LogPath: value.LogPath, Runnable: value.Runnable, NotRunnableReason: value.NotRunnableReason,
		MemoryBlock: value.MemoryContent, MemoryPath: value.MemoryPath, SchemaTokens: schemas, MarginalTokens: marginals,
	}
	item.SetQueuedMessages(value.QueuedMessages)
	for range value.ModelTurns {
		item.RecordModelTurn()
	}
	for index := 0; index < value.CompactionCount; index++ {
		delta := 0
		if index == 0 {
			delta = value.CompactionTokenDelta
		}
		item.RecordCompaction(delta)
	}
	for index := 0; index < value.CompactionModelCalls; index++ {
		prompt, completion := 0, 0
		if index == 0 {
			prompt, completion = value.CompactionPrompt, value.CompactionCompletion
		}
		item.RecordCompactionModel(prompt, completion)
	}
	return item
}

func (legacy *legacyLive) apply(event events.Event) {
	data := mapData(event.Data)
	if event.Type == events.SessionReset {
		snapshot := legacy.item.Snapshot(nil)
		legacy.item = sessionFromSnapshot(session.Snapshot{
			ID: snapshot.ID, Label: snapshot.Label, ServerID: snapshot.ServerID, Workspace: snapshot.Workspace,
			Run: session.RunState{Status: "idle", MaxTurns: snapshot.Run.MaxTurns}, Tools: resetTools(snapshot.Tools),
			Messages: []events.Message{}, Budget: snapshot.Budget, Runnable: snapshot.Runnable,
			NotRunnableReason: snapshot.NotRunnableReason, MemoryPath: snapshot.MemoryPath,
			MemoryContent: snapshot.MemoryContent, LogPath: stringValue(data["log_path"]),
		})
		legacy.bus.ResetSession(snapshot.ID)
	}
	switch event.Type {
	case events.SessionRenamed:
		legacy.item.Label = stringValue(data["label"])
	case events.SessionUpdated:
		legacy.item.ServerID = stringValue(data["server_id"])
		legacy.item.Runnable = boolValue(data["runnable"])
		legacy.item.NotRunnableReason = stringValue(data["not_runnable_reason"])
		legacy.item.MemoryPath = stringValue(data["memory_path"])
		legacy.item.MemoryBlock = stringValue(data["memory_content"])
	case events.RunQueued:
		legacy.item.SetRun(session.RunState{Status: "queued", RunID: event.RunID, MaxTurns: legacy.item.Snapshot(nil).Run.MaxTurns, QueuePosition: intValue(data["position"])})
	case events.RunStarted:
		legacy.item.SetRun(session.RunState{Status: "running", RunID: event.RunID, MaxTurns: legacy.item.Snapshot(nil).Run.MaxTurns})
		legacy.item.SetQueuedMessages(max(0, legacy.item.Snapshot(nil).QueuedMessages-1))
	case events.RunStopped:
		legacy.item.SetRun(session.RunState{Status: "idle", MaxTurns: legacy.item.Snapshot(nil).Run.MaxTurns, LastStopReason: stringValue(data["reason"])})
	case events.Stage:
		state := legacy.item.Snapshot(nil).Run
		state.Turn = intValue(data["turn"])
		legacy.item.SetRun(state)
	case events.ModelDelta:
		if stringValue(data["kind"]) == "content" {
			legacy.item.UpdatePartial(legacy.item.Snapshot(nil).Run.Partial + stringValue(data["text"]))
		}
	case events.ModelResponse:
		legacy.item.UpdatePartial("")
		legacy.item.RecordModelTurn()
	case events.ToolResult:
		legacy.item.IncrementToolCall(stringValue(data["name"]))
	case events.ToolToggled:
		legacy.item.ToggleTool(stringValue(data["name"]), boolValue(data["enabled"]))
	case events.MessageAppended:
		var wrapper struct {
			Message events.Message `json:"message"`
		}
		if decode(event.Data, &wrapper) == nil {
			messages := legacy.item.MessagesCopy()
			if wrapper.Message.Category == "summary" {
				at := min(1, len(messages))
				messages = append(messages[:at], append([]events.Message{wrapper.Message}, messages[at:]...)...)
				legacy.item.ReplaceMessages(messages)
			} else {
				legacy.item.Append(wrapper.Message)
			}
		}
	case events.MessageUpdated:
		messages := legacy.item.MessagesCopy()
		patch := mapData(data["patch"])
		for index := range messages {
			if messages[index].ID == stringValue(data["id"]) {
				if value, ok := patch["content"]; ok {
					messages[index].Content = stringValue(value)
				}
				if value, ok := patch["tokens"]; ok {
					messages[index].Tokens = intValue(value)
				}
				if value, ok := patch["elided"]; ok {
					messages[index].Elided = boolValue(value)
				}
			}
		}
		legacy.item.ReplaceMessages(messages)
	case events.MessageQueued:
		legacy.item.SetQueuedMessages(legacy.item.Snapshot(nil).QueuedMessages + 1)
	case events.BudgetEvent:
		var budget events.Budget
		if decode(event.Data, &budget) == nil {
			legacy.item.SetBudget(budget)
			legacy.item.SetToolTokens(budget.ToolSchemaTokens, budget.ToolMarginalTokens)
		}
	case events.ApprovalRequired:
		state := legacy.item.Snapshot(nil).Run
		state.Status = "paused"
		legacy.item.SetRun(state)
	case events.ApprovalDecided:
		state := legacy.item.Snapshot(nil).Run
		state.Status = "running"
		legacy.item.SetRun(state)
	case events.Compaction:
		legacy.item.RecordCompaction(intValue(data["after"]) - intValue(data["before"]))
		if stringValue(data["kind"]) == "summarize" {
			removed := map[string]bool{}
			for _, id := range stringsValue(data["affected_ids"]) {
				removed[id] = true
			}
			kept := []events.Message{}
			for _, message := range legacy.item.MessagesCopy() {
				if !removed[message.ID] {
					kept = append(kept, message)
				}
			}
			legacy.item.ReplaceMessages(kept)
		}
	case events.CompactionSummary:
		var attempt events.CompactionSummaryData
		if decode(event.Data, &attempt) == nil && attempt.Dispatched {
			legacy.item.RecordCompactionModel(attempt.Usage.PromptTokens, attempt.Usage.CompletionTokens)
		}
	}
	legacy.bus.Publish(event)
}

func (legacy *legacyLive) core() core {
	value := legacy.item.Snapshot(legacy.bus.Recent(legacy.item.ID))
	tools := make([]projection.Tool, len(value.Tools))
	for index, tool := range value.Tools {
		tools[index] = projection.Tool{Name: tool.Name, Enabled: tool.Enabled, Calls: tool.Calls, SchemaTokens: tool.SchemaTokens, MarginalTokens: tool.MarginalTokens}
	}
	return core{
		ID: value.ID, Label: value.Label, ServerID: value.ServerID, Workspace: value.Workspace,
		Run:   projection.Run{Status: value.Run.Status, RunID: value.Run.RunID, Turn: value.Run.Turn, MaxTurns: value.Run.MaxTurns, QueuePosition: value.Run.QueuePosition, Partial: value.Run.Partial, LastStopReason: value.Run.LastStopReason},
		Tools: tools, Messages: value.Messages, Budget: value.Budget, QueuedMessages: value.QueuedMessages,
		Runnable: value.Runnable, NotRunnableReason: value.NotRunnableReason, MemoryPath: value.MemoryPath,
		MemoryContent: value.MemoryContent, ModelTurns: value.ModelTurns, CompactionCount: value.CompactionCount,
		CompactionTokenDelta: value.CompactionTokenDelta, CompactionModelCalls: value.CompactionModelCalls,
		CompactionPrompt: value.CompactionPrompt, CompactionCompletion: value.CompactionCompletion,
	}
}

func resetTools(values []session.ToolState) []session.ToolState {
	result := append([]session.ToolState(nil), values...)
	for index := range result {
		result[index].Calls = 0
	}
	return result
}

func collect(inputs []string) ([]string, error) {
	seen := map[string]bool{}
	result := []string{}
	for _, input := range inputs {
		info, err := os.Stat(input)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			if strings.EqualFold(filepath.Ext(input), ".jsonl") {
				seen[input] = true
				result = append(result, input)
			}
			continue
		}
		err = filepath.WalkDir(input, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && strings.EqualFold(filepath.Ext(path), ".jsonl") && !seen[path] {
				seen[path] = true
				result = append(result, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(result)
	return result, nil
}

func sessionLog(records []projection.Record) bool { return firstSessionID(records) != "" }
func firstSessionID(records []projection.Record) string {
	for _, record := range records {
		if record.Event.SessionID != "" {
			return record.Event.SessionID
		}
	}
	return ""
}
func compact(value any) string {
	data, _ := json.Marshal(value)
	text := string(data)
	if len(text) > 500 {
		return text[:500] + "…"
	}
	return text
}
func emptySlice[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}
func emptyMap[K comparable, V any](values map[K]V) map[K]V {
	if values == nil {
		return map[K]V{}
	}
	return values
}
func decode(value, target any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}
func mapData(value any) map[string]any { result, _ := value.(map[string]any); return result }
func stringValue(value any) string     { result, _ := value.(string); return result }
func boolValue(value any) bool         { result, _ := value.(bool); return result }
func intValue(value any) int {
	if number, ok := value.(float64); ok {
		return int(number)
	}
	if number, ok := value.(int); ok {
		return number
	}
	return 0
}
func stringsValue(value any) []string {
	raw, _ := value.([]any)
	out := []string{}
	for _, item := range raw {
		out = append(out, stringValue(item))
	}
	return out
}
func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
