package stats

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"harness/internal/events"
	"harness/internal/session"
)

const Version = 1

type Tool struct {
	Calls    int64  `json:"calls"`
	Failures int64  `json:"failures"`
	LastUsed string `json:"last_used,omitempty"`
}
type Reliability struct {
	Briefs          int64 `json:"briefs"`
	Completed       int64 `json:"completed"`
	Interventions   int64 `json:"interventions"`
	Reworked        int64 `json:"reworked"`
	Silent          int64 `json:"silent"`
	ModelFailures   int64 `json:"model_failures"`
	HarnessFailures int64 `json:"harness_failures"`
	BriefFailures   int64 `json:"brief_failures"`
}
type Counters struct {
	Chats                     int64           `json:"chats"`
	Runs                      int64           `json:"runs"`
	Turns                     int64           `json:"turns"`
	Tools                     map[string]Tool `json:"tools"`
	ApprovalsRaised           int64           `json:"approvals_raised"`
	ApprovalsApproved         int64           `json:"approvals_approved"`
	OperatorGrants            int64           `json:"operator_grants"`
	Compactions               int64           `json:"compactions"`
	PromptTokens              int64           `json:"prompt_tokens"`
	CompletionTokens          int64           `json:"completion_tokens"`
	CachedTokens              int64           `json:"cached_tokens"`
	DelegatedPromptTokens     int64           `json:"delegated_prompt_tokens,omitempty"`
	DelegatedCompletionTokens int64           `json:"delegated_completion_tokens,omitempty"`
	DelegatedCachedTokens     int64           `json:"delegated_cached_tokens,omitempty"`
	DelegatedToolCalls        int64           `json:"delegated_tool_calls,omitempty"`
	WallMS                    int64           `json:"wall_ms"`
	ModelResponseMS           []int64         `json:"model_response_ms"`
	// Item 2ji (c): the per-connection row. RunTimeMS is one entry per finished
	// run, so the median is a real median rather than a mean pretending to be one.
	// The three time sums are what the run.stopped event reported, which item 2ji
	// (a) put there -- so Activity, the chat line and the eval harness read the
	// same numbers from the same place and cannot disagree.
	RunTimeMS     []int64 `json:"run_time_ms,omitempty"`
	RunModelMS    int64   `json:"run_model_ms,omitempty"`
	RunToolMS     int64   `json:"run_tool_ms,omitempty"`
	RunWaitingMS  int64   `json:"run_waiting_ms,omitempty"`
	EmptyReplies  int64   `json:"empty_replies,omitempty"`
	RepeatedCalls int64   `json:"repeated_calls,omitempty"`
	// RecentRuns is the last RecentRunWindow finished runs, so the row can show
	// lifetime and recent SIDE BY SIDE. A connection that has got worse looks
	// identical to one that was always this way if only the lifetime is shown.
	RecentRuns  []RunRecord `json:"recent_runs,omitempty"`
	FirstUse    string      `json:"first_use,omitempty"`
	LastUse     string      `json:"last_use,omitempty"`
	Reliability Reliability `json:"worker_reliability"`
}

// RecentRunWindow is the "last 20 runs" item 2ji (c) names.
const RecentRunWindow = 20

// RunRecord is one finished run, reduced to what the row needs. A run whose event
// carried no wall clock is not recorded at all, rather than recorded as zeros --
// see recordRun. That is the same narrowing the wire uses: absent, not zero.
type RunRecord struct {
	Stopped       string `json:"stopped,omitempty"`
	TotalMS       int64  `json:"total_ms"`
	ModelMS       int64  `json:"model_ms"`
	ToolMS        int64  `json:"tool_ms"`
	WaitingMS     int64  `json:"waiting_ms"`
	ToolCalls     int64  `json:"tool_calls"`
	ToolFailures  int64  `json:"tool_failures"`
	EmptyReplies  int64  `json:"empty_replies"`
	RepeatedCalls int64  `json:"repeated_calls"`
}

type Ledger struct {
	Version     int                 `json:"version"`
	AgentID     string              `json:"agent_id"`
	Agent       Counters            `json:"agent"`
	Connections map[string]Counters `json:"connections"`
}
type run struct {
	started    time.Time
	evidence   bool
	connection string
	wallMS     int64
	// Item 2ji (c): run.stopped carries no tool counts, so the tool-error rate for
	// the last 20 runs is tallied here as the results arrive.
	toolCalls    int64
	toolFailures int64
}

type Manager struct {
	dir      string
	registry *session.Registry
	bus      *events.Bus
	mu       sync.Mutex
	runs     map[string]*run
	ledgers  map[string]*Ledger
}

func New(dataRoot string, registry *session.Registry, bus *events.Bus) *Manager {
	m := &Manager{dir: filepath.Join(dataRoot, "stats"), registry: registry, bus: bus, runs: map[string]*run{}, ledgers: map[string]*Ledger{}}
	ch, _ := bus.Subscribe()
	go func() {
		for event := range ch {
			m.record(event)
		}
	}()
	return m
}
func (m *Manager) SetRoot(dataRoot string) {
	m.mu.Lock()
	m.dir = filepath.Join(dataRoot, "stats")
	m.runs = map[string]*run{}
	m.ledgers = map[string]*Ledger{}
	m.mu.Unlock()
}
func empty(agentID string) *Ledger {
	return &Ledger{Version: Version, AgentID: agentID, Agent: Counters{Tools: map[string]Tool{}}, Connections: map[string]Counters{}}
}
func (m *Manager) path(id string) string { return filepath.Join(m.dir, id+".json") }
func (m *Manager) load(id string) *Ledger {
	if value := m.ledgers[id]; value != nil {
		return value
	}
	value := empty(id)
	if data, err := os.ReadFile(m.path(id)); err == nil {
		_ = json.Unmarshal(data, value)
	}
	if value.Agent.Tools == nil {
		value.Agent.Tools = map[string]Tool{}
	}
	if value.Connections == nil {
		value.Connections = map[string]Counters{}
	}
	m.ledgers[id] = value
	return value
}
func (m *Manager) save(value *Ledger) error {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := m.path(value.AgentID) + ".tmp"
	if err = os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path(value.AgentID))
}
func key(event events.Event) string { return event.SessionID + "\x00" + event.RunID }
func eventData(value any) map[string]any {
	data, _ := json.Marshal(value)
	out := map[string]any{}
	_ = json.Unmarshal(data, &out)
	return out
}
func int64Value(value any) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	}
	return 0
}
func (m *Manager) identity(sessionID string) (string, string, bool) {
	item, ok := m.registry.Get(sessionID)
	if !ok {
		return "", "", false
	}
	s := item.Snapshot()
	return s.AgentID, s.ConnectionID, true
}
func touch(c *Counters, at string) {
	if c.Tools == nil {
		c.Tools = map[string]Tool{}
	}
	if c.FirstUse == "" {
		c.FirstUse = at
	}
	c.LastUse = at
}
func add(c *Counters, event events.Event, r *run) {
	d := eventData(event.Data)
	recorded := true
	switch event.Type {
	case events.SessionCreated:
		c.Chats++
	case events.RunStarted:
		c.Runs++
		c.Reliability.Briefs++
	case events.ModelRequest:
		c.Turns++
	case events.ModelResponse:
		c.PromptTokens += int64Value(eventData(d["usage"])["prompt_tokens"])
		c.CompletionTokens += int64Value(eventData(d["usage"])["completion_tokens"])
		c.CachedTokens += int64Value(eventData(d["usage"])["cached_tokens"])
		ms := int64Value(d["duration_ms"])
		if ms > 0 {
			c.ModelResponseMS = append(c.ModelResponseMS, ms)
		}
		calls, _ := d["tool_calls"].([]any)
		if len(calls) == 0 && d["content"] != "" && r != nil {
			r.evidence = true
		}
	case events.DelegatedUsage:
		usage := eventData(d["usage"])
		prompt, completion, cached := int64Value(usage["prompt_tokens"]), int64Value(usage["completion_tokens"]), int64Value(usage["cached_tokens"])
		c.PromptTokens, c.CompletionTokens, c.CachedTokens = c.PromptTokens+prompt, c.CompletionTokens+completion, c.CachedTokens+cached
		c.DelegatedPromptTokens += prompt
		c.DelegatedCompletionTokens += completion
		c.DelegatedCachedTokens += cached
		calls, _ := d["tool_calls"].([]any)
		c.DelegatedToolCalls += int64(len(calls))
	case events.ToolResult:
		name, _ := d["name"].(string)
		tool := c.Tools[name]
		tool.Calls++
		if ok, exists := d["ok"].(bool); exists && !ok {
			tool.Failures++
		}
		tool.LastUsed = event.TS
		c.Tools[name] = tool
	case events.ApprovalRequired:
		c.ApprovalsRaised++
		c.Reliability.Interventions++
	case events.ApprovalDecided:
		if decision, _ := d["decision"].(string); decision == "approve" || decision == "once" || decision == "run" || decision == "session" || decision == "operator_mode" {
			c.ApprovalsApproved++
		}
	case events.ShellGrant, events.FileGrant:
		c.OperatorGrants++
	case events.Compaction:
		c.Compactions++
	case events.RunStopped:
		reason, _ := d["reason"].(string)
		if r != nil {
			c.WallMS += r.wallMS
		}
		// Item 2ji (c): the run's own figures, as the event reports them.
		recordRun(c, d, r, event.TS)
		if reason == "done" {
			c.Reliability.Completed++
			if r != nil && !r.evidence {
				c.Reliability.Silent++
			}
		} else if reason == "model_error" || reason == "model_unreachable" || reason == "length" {
			c.Reliability.ModelFailures++
		} else if reason == "tool_errors" || reason == "connection_not_runnable" || reason == "context_ceiling" || reason == "context_exhausted" {
			c.Reliability.HarnessFailures++
		} else if reason == "turn_ceiling" {
			c.Reliability.BriefFailures++
		}
	default:
		recorded = false
	}
	if recorded {
		touch(c, event.TS)
	}
}

// recordRun folds one finished run's reported time into the connection's
// counters and onto the recent window. A run whose event carries no time -- every
// run journalled before item 2ji -- contributes nothing rather than a row of
// zeros that would drag every rate towards nothing.
func recordRun(c *Counters, d map[string]any, r *run, at string) {
	timeFields := eventData(d["time"])
	total := int64Value(timeFields["total_ms"])
	if total <= 0 {
		return
	}
	record := RunRecord{
		Stopped:       at,
		TotalMS:       total,
		ModelMS:       int64Value(timeFields["model_ms"]),
		ToolMS:        int64Value(timeFields["tool_ms"]),
		WaitingMS:     int64Value(timeFields["waiting_ms"]),
		EmptyReplies:  int64Value(d["empty_replies"]),
		RepeatedCalls: int64Value(d["repeated_calls"]),
	}
	if r != nil {
		record.ToolCalls, record.ToolFailures = r.toolCalls, r.toolFailures
	}
	c.RunTimeMS = append(c.RunTimeMS, total)
	c.RunModelMS += record.ModelMS
	c.RunToolMS += record.ToolMS
	c.RunWaitingMS += record.WaitingMS
	c.EmptyReplies += record.EmptyReplies
	c.RepeatedCalls += record.RepeatedCalls
	c.RecentRuns = append(c.RecentRuns, record)
	if len(c.RecentRuns) > RecentRunWindow {
		c.RecentRuns = c.RecentRuns[len(c.RecentRuns)-RecentRunWindow:]
	}
}

func (m *Manager) record(event events.Event) {
	agentID, connection, ok := m.identity(event.SessionID)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ledger := m.load(agentID)
	r := m.runs[key(event)]
	if event.Type == events.RunStarted {
		r = &run{started: time.Now(), connection: connection}
		m.runs[key(event)] = r
	}
	// Item 2ji (c): the tool-error rate for the last 20 runs needs per-run counts,
	// and run.stopped carries none -- it never knew them. This is tallied HERE
	// rather than in add(), which runs twice per event, once for the agent and once
	// for the connection, and would count every tool call twice.
	if event.Type == events.ToolResult && r != nil {
		r.toolCalls++
		if ok, exists := eventData(event.Data)["ok"].(bool); exists && !ok {
			r.toolFailures++
		}
	}
	if event.Type == events.RunStopped && r != nil {
		r.wallMS = time.Since(r.started).Milliseconds()
	}
	add(&ledger.Agent, event, r)
	p := ledger.Connections[connection]
	add(&p, event, r)
	ledger.Connections[connection] = p
	if event.Type == events.RunStopped {
		delete(m.runs, key(event))
	}
	if event.Type == events.SessionCreated || event.Type == events.RunStopped {
		_ = m.save(ledger)
	}
}
func clone(value *Ledger) Ledger {
	data, _ := json.Marshal(value)
	var out Ledger
	_ = json.Unmarshal(data, &out)
	return out
}
func (m *Manager) Snapshot(id string) Ledger {
	m.mu.Lock()
	defer m.mu.Unlock()
	return clone(m.load(id))
}
func (m *Manager) Clear(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	value := empty(id)
	m.ledgers[id] = value
	return m.save(value)
}
func Percentile(values []int64, percentile float64) int64 {
	if len(values) == 0 {
		return 0
	}
	copy := append([]int64(nil), values...)
	sort.Slice(copy, func(i, j int) bool { return copy[i] < copy[j] })
	index := int(float64(len(copy)-1) * percentile)
	return copy[index]
}
