package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	workspaceinfo "harness/internal/workspace"
)

type Registry struct {
	mu          sync.Mutex
	sessions    map[string]*Session
	next        int
	connections func(string) (*config.Connection, bool)
	bus         *events.Bus
	writers     *events.Writers
	maxTurns    int
	config      func() config.Config
	memory      func(context.Context, string, string) (string, string, error)
	agentMemory func(context.Context, string, string) (string, string, error)
	workspaces  *workspaceinfo.Manager
	plansRoot   string
	scratchRoot string
}

func NewRegistry(bus *events.Bus, writers *events.Writers, connections func(string) (*config.Connection, bool), maxTurns int, settings func() config.Config) *Registry {
	return &Registry{sessions: map[string]*Session{}, next: 2, connections: connections, bus: bus, writers: writers, maxTurns: maxTurns, config: settings}
}
func (r *Registry) SetMemoryLoader(loader func(context.Context, string, string) (string, string, error)) {
	r.memory = loader
}
func (r *Registry) SetAgentMemoryLoader(loader func(context.Context, string, string) (string, string, error)) {
	r.agentMemory = loader
}
func (r *Registry) SetWorkspaceManager(manager *workspaceinfo.Manager) { r.workspaces = manager }

// SwitchProfile replaces the idle registry's storage namespace in place. The
// scheduler and runner retain this registry pointer, so changing profiles does
// not restart the process or leave either component writing to the old root.
func (r *Registry) SwitchProfile(writers *events.Writers, memory, agentMemory func(context.Context, string, string) (string, string, error), manager *workspaceinfo.Manager, plansRoot string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, item := range r.sessions {
		snapshot := item.Snapshot()
		if snapshot.QueuedMessages > 0 || snapshot.Run.Status == "running" || snapshot.Run.Status == "queued" || snapshot.Run.Status == "paused" || snapshot.Run.Status == "stopping" {
			return fmt.Errorf("stop the run first")
		}
	}
	r.sessions = map[string]*Session{}
	r.next = 2
	r.writers = writers
	r.memory, r.agentMemory, r.workspaces = memory, agentMemory, manager
	r.plansRoot = filepath.Clean(plansRoot)
	r.scratchRoot = filepath.Join(filepath.Dir(r.plansRoot), "scratch")
	return nil
}
func (r *Registry) SetPlansRoot(root string) {
	r.plansRoot = filepath.Clean(root)
	r.scratchRoot = filepath.Join(filepath.Dir(r.plansRoot), "scratch")
}
func (r *Registry) Create(label, agentID, workspace string) (*Session, error) {
	return r.create(label, agentID, workspace, nil, "b", "")
}
func (r *Registry) CreateRole(label, agentID, workspace, role, planID string) (*Session, error) {
	return r.create(label, agentID, workspace, nil, role, planID)
}
func (r *Registry) CreateLike(sourceID string) (*Session, error) {
	return r.createLike(sourceID)
}
func (r *Registry) createLike(sourceID string) (*Session, error) {
	source, ok := r.Get(sourceID)
	if !ok {
		return nil, fmt.Errorf("source session not found")
	}
	snapshot := source.Snapshot()
	enabled := make(map[string]bool, len(snapshot.Tools))
	for _, tool := range snapshot.Tools {
		enabled[tool.Name] = tool.Enabled
	}
	agentID := snapshot.AgentID
	if _, found := r.resolveAgent(agentID); agentID == "" || !found {
		agentID = snapshot.ConnectionID
	}
	return r.create("", agentID, "", enabled, "b", "")
}

// Restore rehydrates a retained chat into a fresh operational tape. The
// retained transcript is the authority; the new session.created event seeds
// this launch's discardable projector and log generation from that state.
func (r *Registry) Restore(saved Snapshot) (*Session, error) {
	return r.RestoreWithTranscript(saved, nil)
}

// RestoreWithTranscript is Restore carrying the chat's visible transcript, as
// projected from its retained journal, in the session.created event. A restore
// opens a new log generation with no predecessor, so without it the projector
// starts the chat empty while its messages come back whole (item 2es).
func (r *Registry) RestoreWithTranscript(saved Snapshot, transcript any) (*Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if saved.ID == "" {
		return nil, fmt.Errorf("restore session: missing id")
	}
	if _, exists := r.sessions[saved.ID]; exists {
		return nil, fmt.Errorf("restore session %s: duplicate id", saved.ID)
	}
	agent, ok := r.resolveAgent(saved.AgentID)
	if !ok {
		return nil, fmt.Errorf("restore session %s: unknown agent %s", saved.ID, saved.AgentID)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, saved.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("restore session %s created_at: %w", saved.ID, err)
	}
	logPath, err := r.writers.OpenSession(saved.ID)
	if err != nil {
		return nil, err
	}
	run := saved.Run
	if run.Status != "idle" {
		run.Status, run.RunID, run.QueuePosition, run.Partial = "idle", "", 0, ""
		run.LastStopReason = "aborted_mid_run"
	}
	tools, calls := map[string]bool{}, map[string]int{}
	schemaTokens, marginalTokens := map[string]int{}, map[string]int{}
	for _, tool := range saved.Tools {
		tools[tool.Name], calls[tool.Name] = tool.Enabled, tool.Calls
		schemaTokens[tool.Name], marginalTokens[tool.Name] = tool.SchemaTokens, tool.MarginalTokens
	}
	role := saved.Role
	if role == "" {
		role = "b"
	}
	planID, planDir, planRepo := "", "", ""
	if (role == "d" || role == "c") && saved.PlanID != "" {
		planID, err = normalizePlanID(saved.PlanID)
		if err != nil {
			return nil, fmt.Errorf("restore session %s: %w", saved.ID, err)
		}
		planDir = filepath.Join(r.plansRoot, planID)
		planRepo = planRepoForRestore(planDir, saved.PlanRepo)
	}
	workspace := firstNonempty(saved.WorkspaceDir, saved.Workspace)
	workspaceMissing, runnable, notRunnableReason := saved.WorkspaceMissing, saved.Runnable, saved.NotRunnableReason
	if saved.Scratch {
		if r.scratchRoot != "" {
			workspace = filepath.Join(r.scratchRoot, saved.ID)
		}
		if err := os.MkdirAll(workspace, 0o700); err != nil {
			workspaceMissing, runnable = true, false
			notRunnableReason = fmt.Sprintf("scratch folder is unavailable: %s: %v", workspace, err)
		} else {
			workspaceMissing, runnable, notRunnableReason = false, true, ""
		}
	} else if info, statErr := os.Stat(workspace); statErr != nil || !info.IsDir() {
		workspaceMissing, runnable = true, false
		notRunnableReason = fmt.Sprintf("folder is missing: %s", workspace)
	} else if workspaceMissing {
		workspaceMissing, runnable, notRunnableReason = false, true, ""
	}
	s := &Session{
		ID: saved.ID, Label: saved.Label, AgentID: saved.AgentID, ConnectionID: saved.ConnectionID,
		AgentName: saved.AgentName, BConnection: saved.BConnection, Role: role, PlanID: planID, PlanName: saved.PlanName, PlanDir: planDir, PlanRepo: planRepo, PlansRoot: r.plansRoot, PlanRepos: r.planRepos, RegisterPlan: r.EnsurePlan, PromptAddendum: agent.PromptAddendum, NetworkBoundary: saved.NetworkBoundary,
		Workspace: workspace, WorkspaceMissing: workspaceMissing, Scratch: saved.Scratch,
		ProjectBlock: saved.ProjectContent, ProjectFiles: append([]string(nil), saved.ProjectFiles...), ProjectNotes: append([]string(nil), saved.ProjectNotes...),
		PendingRepoPolicy: clonePolicyState(saved.PendingRepoPolicy), RepoPolicy: clonePolicyState(saved.RepoPolicy),
		Run: run, ToolsEnabled: tools, ToolCalls: calls, LastSeen: map[string]time.Time{}, CreatedAt: createdAt,
		Closed: saved.Closed, NamePinned: saved.NamePinned, Messages: append([]events.Message(nil), saved.Messages...), Budget: saved.Budget,
		LogPath: logPath, Runnable: runnable, NotRunnableReason: notRunnableReason,
		LoadFolderMemory: r.folderLoader(saved.ConnectionID), MemoryBlock: saved.MemoryContent, MemoryPath: saved.MemoryPath, AgentMemoryBlock: saved.AgentMemoryContent, AgentMemoryPath: saved.AgentMemoryPath,
		SchemaTokens: schemaTokens, MarginalTokens: marginalTokens, queuedMessages: saved.QueuedMessages,
		modelTurns: saved.ModelTurns, compactionCount: saved.CompactionCount, compactionTokenDelta: saved.CompactionTokenDelta,
		compactionModelCalls: saved.CompactionModelCalls, compactionPrompt: saved.CompactionPrompt, compactionCompletion: saved.CompactionCompletion,
	}
	if s.NetworkBoundary == "" {
		s.NetworkBoundary = NetworkBoundary(r.config())
	}
	if r.workspaces != nil && !s.WorkspaceMissing {
		s.ProjectTouch = r.projectTouch(s)
	}
	s.EnsurePlan = r.ensurePlanHook(s)
	r.sessions[s.ID] = s
	if strings.HasPrefix(s.ID, "s") {
		if value, parseErr := strconv.Atoi(strings.TrimPrefix(s.ID, "s")); parseErr == nil && value >= r.next {
			r.next = value + 1
		}
	}
	data := map[string]any{"workspace_dir": s.Workspace, "session": s.SnapshotUnlocked()}
	if transcript != nil {
		data["chat"] = transcript
	}
	r.bus.Publish(events.New(events.SessionCreated, s.ID, "", data))
	return s, nil
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
func planRepoForRestore(dir, saved string) string {
	if saved != "" {
		return saved
	}
	return planRepo(dir)
}
func planDisplayName(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "plan.md"))
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if name := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#")); strings.HasPrefix(strings.TrimSpace(line), "#") && name != "" {
				return name
			}
		}
	}
	return filepath.Base(dir)
}
func normalizePlanID(value string) (string, error) {
	if value == "" || value == "." || value == ".." || filepath.IsAbs(value) || filepath.Base(value) != value || strings.ContainsAny(value, `/\`) {
		return "", fmt.Errorf("plan_id: invalid plan id")
	}
	return value, nil
}
func (r *Registry) create(label, agentID, workspace string, enabled map[string]bool, role, planID string) (*Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	agent, ok := r.resolveAgent(agentID)
	if !ok {
		return nil, fmt.Errorf("agent_id: unknown agent %s", agentID)
	}
	agentID = config.AgentID(agent.Name)
	if role == "" {
		role = "b"
	}
	if role != "b" && role != "d" && role != "c" {
		return nil, fmt.Errorf("role must be b, c or d")
	}
	// The worker runs on the c connection when one is assigned and falls back to b,
	// so Go works on a single-connection install rather than refusing to start.
	connectionID := agent.ConnectionFor(role)
	if role == "d" && connectionID == "" {
		return nil, fmt.Errorf("agent_d is not assigned")
	}
	connection, ok := r.connections(connectionID)
	if !ok {
		return nil, fmt.Errorf("agent_id: %s connection %s was not found", role, connectionID)
	}
	id := "main"
	if len(r.sessions) > 0 {
		id = fmt.Sprintf("s%d", r.next)
		r.next++
	}
	// Item 2go (v1.2.5): a chat with no name has NO NAME. It used to be called
	// after its own id - s14 - which told the operator nothing and was never
	// something he wrote. The tab reads "new chat" until his first message
	// names it.
	planDir, planName, selectedRepo := "", "", ""
	// d and c both bind to the plan: d to write its text, c to work in the
	// repository the plan names. Without this the worker lands in scratch and
	// every path in its item points at nothing.
	if (role == "d" || role == "c") && planID != "" {
		normalized, normalizeErr := normalizePlanID(planID)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		planID = normalized
		planDir = filepath.Join(r.plansRoot, planID)
		if info, statErr := os.Lstat(planDir); statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("plan_id: plan not found")
		}
		planName = planDisplayName(planDir)
		selectedRepo = planRepo(planDir)
		if workspace == "" {
			workspace = selectedRepo
		}
	}
	scratch := workspace == ""
	if scratch {
		if r.scratchRoot == "" {
			return nil, fmt.Errorf("scratch storage is unavailable")
		}
		for {
			workspace = filepath.Join(r.scratchRoot, id)
			_, statErr := os.Lstat(workspace)
			if os.IsNotExist(statErr) {
				break
			}
			if statErr != nil {
				return nil, statErr
			}
			id = fmt.Sprintf("s%d", r.next)
			r.next++
		}
		if err := os.MkdirAll(workspace, 0o700); err != nil {
			return nil, err
		}
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	setup := workspaceinfo.Setup{Dir: abs}
	if r.workspaces != nil {
		setup, err = r.workspaces.Inspect(abs)
		if err != nil {
			return nil, err
		}
		abs = setup.Dir
	}
	if !scratch && r.plansRoot != "" && hasAgentFile(abs) {
		if _, _, err := r.ensurePlanLocked(abs); err != nil {
			return nil, err
		}
	}
	logPath, err := r.writers.OpenSession(id)
	if err != nil {
		return nil, err
	}
	runnable, reason := runnable(connection, r.config().Context.Accounting)
	tools := enabled
	if tools == nil {
		tools = map[string]bool{}
		for _, name := range config.FullToolset() {
			tools[name] = false
		}
		for _, name := range agent.Toolset {
			tools[name] = true
		}
	}
	if role == "d" {
		tools["shell"] = false
		tools["run_script"] = false
	}
	var activePolicy, pendingPolicy *workspaceinfo.PolicyState
	if setup.Policy.Path != "" {
		copy := setup.Policy
		if setup.Policy.Approved && setup.Policy.Error == "" {
			activePolicy = &copy
		} else {
			pendingPolicy = &copy
		}
	}
	if activePolicy != nil && len(activePolicy.Policy.DefaultToolset) > 0 {
		selected := map[string]bool{}
		for name := range tools {
			selected[name] = false
		}
		for _, name := range activePolicy.Policy.DefaultToolset {
			if _, ok := selected[name]; ok {
				selected[name] = true
			}
		}
		tools = selected
	}
	// v0.69.0/W16 cold review: a repository's policy picks tools within the
	// role, never around it — a planner reads the folder and never runs it.
	if role == "d" {
		tools["shell"] = false
		tools["run_script"] = false
	}
	memoryBlock, memoryPath := "", ""
	if r.memory != nil {
		memoryBlock, memoryPath, err = r.folderMemory(scratch, abs, agent.B)
		if err != nil {
			return nil, err
		}
	}
	agentMemoryBlock, agentMemoryPath := "", ""
	if r.agentMemory != nil {
		agentMemoryBlock, agentMemoryPath, err = r.agentMemory(context.Background(), agentID, agent.B)
		if err != nil {
			return nil, err
		}
	}
	settings := r.config()
	session := &Session{LoadFolderMemory: r.folderLoader(agent.B), ID: id, Label: label, AgentID: agentID, ConnectionID: connectionID, AgentName: agent.Name, BConnection: connection.Label, Role: role, PlanID: planID, PlanName: planName, PlanDir: planDir, PlanRepo: selectedRepo, PlansRoot: r.plansRoot, PlanRepos: r.planRepos, RegisterPlan: r.EnsurePlan, PromptAddendum: agent.PromptAddendum, NetworkBoundary: NetworkBoundary(settings), Workspace: abs, WorkspaceMissing: setup.Missing, Scratch: scratch, ProjectBlock: setup.Instructions.Block, ProjectFiles: setup.Instructions.Files, ProjectNotes: setup.Instructions.Notes, PendingRepoPolicy: pendingPolicy, RepoPolicy: activePolicy, Run: RunState{Status: "idle", MaxTurns: r.maxTurns}, ToolsEnabled: tools, ToolCalls: map[string]int{}, LastSeen: map[string]time.Time{}, CreatedAt: time.Now().UTC(), LogPath: logPath, Runnable: runnable, NotRunnableReason: reason, DegradedNotes: degradedFeatures(connection, settings.Context.Accounting), MemoryBlock: memoryBlock, MemoryPath: memoryPath, AgentMemoryBlock: agentMemoryBlock, AgentMemoryPath: agentMemoryPath, MemoryMaxTokens: settings.Memory.MaxTokens, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	if r.workspaces != nil && !setup.Missing {
		session.ProjectTouch = r.projectTouch(session)
	}
	session.EnsurePlan = r.ensurePlanHook(session)
	session.Messages = []events.Message{}
	session.Budget = initialBudget(connection)
	r.sessions[id] = session
	r.bus.Publish(events.New(events.SessionCreated, id, "", map[string]any{"workspace_dir": abs, "session": session.Snapshot()}))
	if len(setup.Instructions.Files) > 0 {
		r.bus.Publish(events.New(events.ProjectInstructions, id, "", map[string]any{"block": setup.Instructions.Block, "files": setup.Instructions.Files, "notes": setup.Instructions.Notes, "lazy": false}))
	}
	return session, nil
}

func NetworkBoundary(settings config.Config) string {
	reachable := "loopback and the configured model server"
	if len(settings.Shell.AllowedModelRanges) > 0 {
		reachable += ", plus the operator-configured ranges " + strings.Join(settings.Shell.AllowedModelRanges, ", ")
	}
	fetch := "public addresses and exact tools.fetch.allow_internal_hosts entries"
	if settings.Shell.AllowLocalNetwork && len(settings.Shell.ConfirmedLocalSubnets) > 0 {
		subnets := strings.Join(settings.Shell.ConfirmedLocalSubnets, ", ")
		reachable += ", plus the operator-confirmed LAN subnets " + subnets
		fetch += ", plus the operator-confirmed LAN subnets " + subnets
	}
	return "Network boundary: service-context shell may reach " + reachable + "; fetch_url may reach " + fetch + " and always refuses link-local, cloud metadata, and this Agent_b listener; when another target is needed, offer Run as you for operator approval under the operator's non-elevated identity."
}

func hasAgentFile(dir string) bool {
	for _, name := range []string{"AGENT_B.md", "AGENTS.md", "CLAUDE.md"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

func (r *Registry) ensurePlanHook(item *Session) func(string) {
	return func(dir string) {
		if item.Scratch || !hasAgentFile(dir) {
			return
		}
		_, _, _ = r.EnsurePlan(dir)
	}
}

func (r *Registry) ApplyRepoPolicySession(sessionID string, state workspaceinfo.PolicyState) error {
	s, ok := r.Get(sessionID)
	if !ok {
		return fmt.Errorf("session not found")
	}
	copy := state
	s.SetRepoPolicy(&copy)
	if len(state.Policy.DefaultToolset) > 0 {
		enabled := s.EnabledTools()
		for name := range enabled {
			s.ToggleTool(name, false)
		}
		for _, name := range state.Policy.DefaultToolset {
			if s.Role == "d" && (name == "shell" || name == "run_script") {
				continue
			}
			s.ToggleTool(name, true)
		}
	}
	return nil
}

func (r *Registry) projectTouch(item *Session) func(string) {
	return func(relative string) {
		snapshot := item.Snapshot()
		root := snapshot.WorkspaceDir
		target := filepath.Join(root, relative)
		info, statErr := os.Stat(target)
		if statErr == nil && !info.IsDir() {
			target = filepath.Dir(target)
		}
		addition, loadErr := workspaceinfo.LoadInstructions(root, target)
		if loadErr != nil {
			r.bus.Publish(events.New(events.Error, item.ID, "", map[string]any{"where": "project_instructions", "message": loadErr.Error()}))
			return
		}
		if item.AppendProject(addition.Block, addition.Files, addition.Notes) {
			r.bus.Publish(events.New(events.ProjectInstructions, item.ID, "", map[string]any{"block": addition.Block, "files": addition.Files, "notes": addition.Notes, "lazy": true}))
		}
	}
}
func (r *Registry) DenyRepoPolicy(sessionID string) error {
	s, ok := r.Get(sessionID)
	if !ok {
		return fmt.Errorf("session not found")
	}
	s.mu.Lock()
	s.PendingRepoPolicy = nil
	s.mu.Unlock()
	return nil
}
func (r *Registry) RevokeRepoPolicy(workspace string) {
	for _, s := range r.List() {
		if strings.EqualFold(filepath.Clean(s.Workspace), filepath.Clean(workspace)) {
			s.mu.Lock()
			s.RepoPolicy = nil
			s.PendingRepoPolicy = nil
			s.mu.Unlock()
		}
	}
}
func (r *Registry) ClearWorkspaceMemory(workspace string) {
	for _, s := range r.List() {
		if strings.EqualFold(filepath.Clean(s.Workspace), filepath.Clean(workspace)) {
			s.mu.Lock()
			s.MemoryBlock = ""
			s.mu.Unlock()
		}
	}
}
func (r *Registry) ClearAgentMemory(agentID string) {
	for _, s := range r.List() {
		if s.AgentID == agentID {
			s.mu.Lock()
			s.AgentMemoryBlock = ""
			s.mu.Unlock()
		}
	}
}
func (r *Registry) Get(id string) (*Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	return s, ok
}
func (r *Registry) Label(id string) string {
	if s, ok := r.Get(id); ok {
		return s.Label
	}
	return id
}
func (r *Registry) ConnectionInUse(connectionID string) (string, bool) {
	for _, item := range r.List() {
		if item.ConnectionID == connectionID {
			return item.ID, true
		}
	}
	return "", false
}
func (r *Registry) ConnectionRunnable(connectionID string) (bool, string) {
	connection, ok := r.connections(connectionID)
	if !ok {
		return false, "unknown connection " + connectionID
	}
	return runnable(connection, r.config().Context.Accounting)
}
func (r *Registry) AgentRunnable(agentID string) (bool, string) {
	return r.AgentRoleRunnable(agentID, "b")
}
func (r *Registry) AgentRoleRunnable(agentID, role string) (bool, string) {
	agent, ok := r.resolveAgent(agentID)
	if !ok {
		return false, "unknown agent " + agentID
	}
	connectionID := agent.ConnectionFor(role)
	if role == "d" && connectionID == "" {
		return false, "agent_d is not assigned"
	}
	return r.ConnectionRunnable(connectionID)
}
func (r *Registry) resolveAgent(id string) (*config.Agent, bool) {
	cfg := r.config()
	if agent, ok := cfg.Agent(id); ok {
		return agent, true
	}
	for i := range cfg.Agents {
		if cfg.Agents[i].B == id {
			agent := cfg.Agents[i]
			return &agent, true
		}
	}
	for _, candidate := range cfg.Connections {
		if config.AgentID(candidate.Label) == id {
			return &config.Agent{Name: candidate.Label, B: candidate.ID, Toolset: config.FullToolset()}, true
		}
	}
	if connection, ok := r.connections(id); ok {
		return &config.Agent{Name: connection.Label, B: connection.ID, Toolset: config.FullToolset()}, true
	}
	return nil, false
}
func (r *Registry) List() []*Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	values := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		values = append(values, s)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values
}
func (r *Registry) Rename(id, label string) error { return r.RenameBy(id, label, "user") }
func (r *Registry) RenameBy(id, label, by string) error {
	s, ok := r.Get(id)
	if !ok {
		return fmt.Errorf("session not found")
	}
	label = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(label, "\r", " "), "\n", " "))
	if label == "" {
		return fmt.Errorf("label is required")
	}
	if strings.EqualFold(label, "null") {
		return fmt.Errorf("label cannot be null")
	}
	if len([]rune(label)) > 80 {
		label = string([]rune(label)[:80])
	}
	if by == "aux" {
		by = "c"
	}
	if by != "user" && by != "c" {
		return fmt.Errorf("rename author is invalid")
	}
	s.mu.Lock()
	if by == "c" && s.NamePinned {
		s.mu.Unlock()
		return nil
	}
	s.Label = label
	if by == "user" {
		s.NamePinned = true
	}
	s.mu.Unlock()
	r.bus.Publish(events.New(events.SessionRenamed, id, "", map[string]any{"session_id": id, "label": label, "by": by}))
	return nil
}
func (r *Registry) SetConnection(id, connectionID string) error {
	s, ok := r.Get(id)
	if !ok {
		return fmt.Errorf("session not found")
	}
	connection, ok := r.connections(connectionID)
	if !ok {
		return fmt.Errorf("connection_id: unknown connection %s", connectionID)
	}
	runnable, reason := runnable(connection, r.config().Context.Accounting)
	if !runnable {
		return fmt.Errorf("connection_id: %s", reason)
	}

	s.mu.Lock()
	if s.Closed {
		s.mu.Unlock()
		return fmt.Errorf("session is closed")
	}
	if s.Run.Status != "idle" {
		s.mu.Unlock()
		return fmt.Errorf("session is running")
	}
	workspace, scratch := s.Workspace, s.Scratch
	memoryBlock, memoryPath := s.MemoryBlock, s.MemoryPath
	s.mu.Unlock()

	if r.memory != nil {
		var err error
		memoryBlock, memoryPath, err = r.folderMemory(scratch, workspace, connectionID)
		if err != nil {
			return err
		}
	}

	s.mu.Lock()
	if s.Closed {
		s.mu.Unlock()
		return fmt.Errorf("session is closed")
	}
	if s.Run.Status != "idle" {
		s.mu.Unlock()
		return fmt.Errorf("session is running")
	}
	s.ConnectionID = connectionID
	s.AgentName, s.BConnection = connection.Label, connection.Label
	s.Runnable, s.NotRunnableReason = true, ""
	s.MemoryBlock, s.MemoryPath = memoryBlock, memoryPath
	s.Budget = initialBudget(connection)
	s.mu.Unlock()

	r.bus.Publish(events.New(events.SessionUpdated, id, "", map[string]any{
		"session_id":          id,
		"connection_id":       connectionID,
		"agent_name":          connection.Label,
		"b_connection":        connection.Label,
		"runnable":            true,
		"not_runnable_reason": "",
		"memory_path":         memoryPath,
		"memory_content":      memoryBlock,
	}))
	return nil
}

func (r *Registry) SetAgent(id, agentID string) error {
	s, ok := r.Get(id)
	if !ok {
		return fmt.Errorf("session not found")
	}
	agent, ok := r.resolveAgent(agentID)
	if !ok {
		return fmt.Errorf("agent_id: unknown agent %s", agentID)
	}
	snapshot := s.Snapshot()
	connectionID := agent.ConnectionFor(snapshot.Role)
	if snapshot.Role == "d" && connectionID == "" {
		return fmt.Errorf("agent_id: agent_d is not assigned")
	}
	connection, ok := r.connections(connectionID)
	if !ok {
		return fmt.Errorf("agent_id: %s connection %s was not found", snapshot.Role, connectionID)
	}
	agentID = config.AgentID(agent.Name)
	runnable, reason := runnable(connection, r.config().Context.Accounting)
	if !runnable {
		return fmt.Errorf("agent_id: %s", reason)
	}
	s.mu.Lock()
	if s.Closed {
		s.mu.Unlock()
		return fmt.Errorf("session is closed")
	}
	if s.Run.Status != "idle" {
		s.mu.Unlock()
		return fmt.Errorf("session is running")
	}
	workspace, scratch := s.Workspace, s.Scratch
	s.mu.Unlock()
	memoryBlock, memoryPath := "", ""
	if r.memory != nil {
		var err error
		memoryBlock, memoryPath, err = r.folderMemory(scratch, workspace, connectionID)
		if err != nil {
			return err
		}
	}
	agentMemoryBlock, agentMemoryPath := "", ""
	if r.agentMemory != nil {
		var err error
		agentMemoryBlock, agentMemoryPath, err = r.agentMemory(context.Background(), agentID, connectionID)
		if err != nil {
			return err
		}
	}
	enabled := map[string]bool{}
	for _, name := range config.FullToolset() {
		enabled[name] = false
	}
	for _, name := range agent.Toolset {
		enabled[name] = true
	}
	if snapshot.Role == "d" {
		enabled["shell"] = false
		enabled["run_script"] = false
	}
	s.mu.Lock()
	s.AgentID, s.ConnectionID, s.AgentName, s.BConnection = agentID, connectionID, agent.Name, connection.Label
	s.PromptAddendum, s.ToolsEnabled = agent.PromptAddendum, enabled
	s.Runnable, s.NotRunnableReason = true, ""
	s.MemoryBlock, s.MemoryPath, s.AgentMemoryBlock, s.AgentMemoryPath, s.Budget = memoryBlock, memoryPath, agentMemoryBlock, agentMemoryPath, initialBudget(connection)
	s.mu.Unlock()
	r.bus.Publish(events.New(events.SessionUpdated, id, "", map[string]any{"session_id": id, "agent_id": agentID, "connection_id": connectionID, "agent_name": agent.Name, "b_connection": connection.Label, "runnable": true, "not_runnable_reason": "", "memory_path": memoryPath, "memory_content": memoryBlock}))
	return nil
}

func (r *Registry) ApplyAgentBinding(agentID string) error {
	agent, ok := r.resolveAgent(agentID)
	if !ok {
		return fmt.Errorf("agent_id: unknown agent %s", agentID)
	}
	for _, item := range r.List() {
		snapshot := item.Snapshot()
		if snapshot.AgentID != agentID || snapshot.Closed {
			continue
		}
		if snapshot.Run.Status == "running" || snapshot.Run.Status == "stopping" {
			return fmt.Errorf("agent_id: session %s is running", snapshot.ID)
		}
		connectionID := agent.ConnectionFor(snapshot.Role)
		connection, ok := r.connections(connectionID)
		if !ok {
			return fmt.Errorf("agent_id: %s connection %s was not found", snapshot.Role, connectionID)
		}
		if runnable, reason := runnable(connection, r.config().Context.Accounting); !runnable {
			return fmt.Errorf("agent_id: %s", reason)
		}
		memoryBlock, memoryPath := snapshot.MemoryContent, snapshot.MemoryPath
		if r.memory != nil {
			var err error
			memoryBlock, memoryPath, err = r.folderMemory(snapshot.Scratch, snapshot.Workspace, connectionID)
			if err != nil {
				return err
			}
		}
		agentMemoryBlock, agentMemoryPath := snapshot.AgentMemoryContent, snapshot.AgentMemoryPath
		if r.agentMemory != nil {
			var err error
			agentMemoryBlock, agentMemoryPath, err = r.agentMemory(context.Background(), agentID, connectionID)
			if err != nil {
				return err
			}
		}
		item.ApplyAgentConfig(agentID, *agent, *connection)
		item.mu.Lock()
		item.MemoryBlock, item.MemoryPath = memoryBlock, memoryPath
		item.AgentMemoryBlock, item.AgentMemoryPath = agentMemoryBlock, agentMemoryPath
		item.Budget = initialBudget(connection)
		item.mu.Unlock()
		r.bus.Publish(events.New(events.SessionUpdated, item.ID, "", map[string]any{
			"session_id": item.ID, "agent_id": agentID, "connection_id": connectionID,
			"agent_name": agent.Name, "b_connection": connection.Label, "runnable": true,
			"not_runnable_reason": "", "memory_path": memoryPath, "memory_content": memoryBlock,
			"agent_memory_path": agentMemoryPath, "agent_memory_content": agentMemoryBlock,
		}))
	}
	return nil
}

func (r *Registry) ApplyAgentToolset(agentID string, enabled map[string]bool) {
	for _, item := range r.List() {
		if item.AgentID != agentID {
			continue
		}
		for _, name := range config.FullToolset() {
			item.ToggleTool(name, enabled[name])
		}
	}
}
func (r *Registry) Reset(id string) (string, error) {
	s, ok := r.Get(id)
	if !ok {
		return "", fmt.Errorf("session not found")
	}
	if s.IsRunning() {
		return "", fmt.Errorf("session is running")
	}
	if s.IsClosed() {
		return "", fmt.Errorf("session is closed")
	}
	path, predecessor, err := r.writers.RotateSession(id)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.Messages = nil
	s.ToolCalls = map[string]int{}
	s.queuedMessages = 0
	s.modelTurns = 0
	s.compactionCount = 0
	s.compactionTokenDelta = 0
	s.compactionModelCalls = 0
	s.compactionPrompt = 0
	s.compactionCompletion = 0
	s.LogPath = path
	s.Run = RunState{Status: "idle", MaxTurns: r.maxTurns}
	if r.memory != nil {
		block, memoryPath, loadErr := r.folderMemory(s.Scratch, s.Workspace, s.ConnectionID)
		if loadErr != nil {
			s.mu.Unlock()
			return "", loadErr
		}
		s.MemoryBlock, s.MemoryPath = block, memoryPath
	}
	if r.agentMemory != nil {
		block, path, loadErr := r.agentMemory(context.Background(), s.AgentID, s.ConnectionID)
		if loadErr != nil {
			s.mu.Unlock()
			return "", loadErr
		}
		s.AgentMemoryBlock, s.AgentMemoryPath = block, path
	}
	s.mu.Unlock()
	data := map[string]any{"session_id": id, "log_path": path}
	if predecessor.Offset > 0 {
		data["predecessor"] = map[string]any{"generation": predecessor.Generation, "offset": predecessor.Offset}
	}
	r.bus.Publish(events.New(events.SessionReset, id, "", data))
	return path, nil
}

func (r *Registry) DropLastMessage(id string) (events.Message, error) {
	s, ok := r.Get(id)
	if !ok {
		return events.Message{}, fmt.Errorf("session not found")
	}
	if s.IsRunning() {
		return events.Message{}, fmt.Errorf("session is running")
	}
	if s.IsClosed() {
		return events.Message{}, fmt.Errorf("session is closed")
	}
	message, ok := s.DropLastMessage()
	if !ok {
		return events.Message{}, fmt.Errorf("session has no messages")
	}
	r.bus.Publish(events.New(events.MessageRemoved, id, "", map[string]any{
		"id": message.ID, "reason": "operator_repair",
	}))
	return message, nil
}
func (r *Registry) Close(id string) error {
	s, ok := r.Get(id)
	if !ok {
		return fmt.Errorf("session not found")
	}
	if err := s.Close(); err != nil {
		return err
	}
	r.bus.Publish(events.New(events.SessionClosed, id, "", map[string]any{"session_id": id}))
	return nil
}

func (r *Registry) Reopen(id string) error {
	s, ok := r.Get(id)
	if !ok {
		return fmt.Errorf("session not found")
	}
	s.mu.Lock()
	if !s.Closed {
		s.mu.Unlock()
		return fmt.Errorf("session is already open")
	}
	s.Closed = false
	s.mu.Unlock()
	r.bus.Publish(events.New(events.SessionReopened, id, "", map[string]any{"session_id": id}))
	return nil
}

func (r *Registry) Delete(id string) (events.SessionInventory, error) {
	r.mu.Lock()
	s, ok := r.sessions[id]
	if !ok {
		r.mu.Unlock()
		return events.SessionInventory{}, fmt.Errorf("session not found")
	}
	if !s.IsClosed() {
		r.mu.Unlock()
		return events.SessionInventory{}, fmt.Errorf("session must be closed before deletion")
	}
	if s.IsRunning() {
		r.mu.Unlock()
		return events.SessionInventory{}, fmt.Errorf("session is running")
	}
	r.mu.Unlock()
	inventory, err := r.writers.DeleteSession(id)
	if err != nil {
		return events.SessionInventory{}, err
	}
	r.mu.Lock()
	delete(r.sessions, id)
	r.mu.Unlock()
	return inventory, nil
}

// Item 2gy (v1.2.5): a capability finding gates the FEATURE that needs it, not
// the chat. A probe that could not get an answer - a busy GPU, a timeout, a 503
// - used to be recorded as a server that cannot call tools, and that finding
// then stopped every run with "connection not runnable". A chat the operator can
// still talk in is not unrunnable because one capability is missing.
//
// "Not runnable" now means exactly what it says: there is no endpoint or no
// model name, so there is nothing to send a request to. Everything else - no
// tool calling, no streaming, a truncating server, no /tokenize for exact
// accounting - degrades the feature and says so, which is what the honest
// degradation rule has always asked for.
func runnable(connection *config.Connection, accounting string) (bool, string) {
	if reason := config.ConnectionSetupReason(connection); reason != "" {
		return false, reason
	}
	return true, ""
}

// degradedFeatures lists what this connection cannot do, for the strip to say once
// rather than for the run to refuse.
func degradedFeatures(connection *config.Connection, accounting string) []string {
	c := connection.Capabilities
	var notes []string
	if connection.Context.NCtx == 0 {
		notes = append(notes, "context length unknown")
	}
	if !c.ToolCalls {
		notes = append(notes, "tools off · connection reports no tool calling")
	}
	if c.OverflowBehavior == "truncate" {
		notes = append(notes, "server truncates context")
	}
	if !c.Streaming {
		notes = append(notes, "streaming unavailable")
	}
	if accounting == "exact" && !c.Tokenize {
		notes = append(notes, "exact accounting requested but this server has no /tokenize")
	}
	return notes
}

func initialBudget(connection *config.Connection) events.Budget {
	nctx := connection.Context.NCtx
	ceiling := nctx - connection.Context.ReserveOutput
	if ceiling < 0 {
		ceiling = 0
	}
	return events.Budget{
		NCtx: nctx, Reserve: connection.Context.ReserveOutput, Ceiling: ceiling,
		Mode: "estimated", Estimated: true, EstimatedCategories: []string{},
		Categories:       map[string]int{"system": 0, "project": 0, "workspace_memory": 0, "agent_memory": 0, "tools": 0, "history": 0, "files": 0, "results": 0, "fetched": 0, "summary": 0},
		ToolSchemaTokens: map[string]int{}, ToolMarginalTokens: map[string]int{},
	}
}

func (r *Registry) RefreshRunnable() {
	for _, item := range r.List() {
		connection, ok := r.connections(item.ConnectionID)
		if !ok {
			item.SetRunnable(false, "connection not found")
			continue
		}
		ok, reason := runnable(connection, r.config().Context.Accounting)
		item.SetRunnable(ok, reason)
	}
}

// folderMemory loads a chat's folder memory layer. A scratch folder has no
// layer of its own (item 2fh): a scratch chat's folder memory is the layers of
// the plan repositories it writes into, loaded at run start.
func (r *Registry) folderMemory(scratch bool, workspace, connectionID string) (string, string, error) {
	if scratch || r.memory == nil {
		return "", "", nil
	}
	return r.memory(context.Background(), workspace, connectionID)
}

// folderLoader loads one folder's layer for a chat on connectionID.
func (r *Registry) folderLoader(connectionID string) func(string) (string, string, error) {
	return func(folder string) (string, string, error) {
		if r.memory == nil {
			return "", "", nil
		}
		return r.memory(context.Background(), folder, connectionID)
	}
}
