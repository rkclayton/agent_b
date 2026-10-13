package config

import (
	"fmt"
	"harness/internal/broker"
	"harness/internal/credential"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func Defaults(workspace string) Config {
	if workspace == "" {
		workspace = "./workspace"
	}
	abs, _ := filepath.Abs(workspace)
	connection := defaultConnection()
	// no placeholder that reads as a value. A connection with no
	// model chosen has an empty model, and ConnectionSetupReason says exactly
	// that. The literal "model" default reached save and was refused elsewhere,
	// with wording the operator could not act on.
	connection.ID, connection.Label, connection.BaseURL, connection.Model = "local", "Local", "http://127.0.0.1:8080", ""
	return Config{
		ConfigVersion: CurrentConfigVersion,
		Listen:        "127.0.0.1:8790", Workspace: abs, LogDir: "logs",
		Connections: []Connection{connection}, Agents: []Agent{{Name: "agent_b", Toolset: FullToolset(), Thinking: true}},
		Services: map[string]Service{},
		Sandbox:  Sandbox{Enabled: false, initialized: true},
		Run:      RunConfig{MaxTurns: DefaultMaxTurns, MaxWallClockSeconds: DefaultMaxWallClockSeconds, MaxToolCalls: DefaultMaxToolCalls, CycleWindow: 5, MaxConcurrent: 4}, Cron: Cron{MaxRunMinutes: 20}, Approval: Approval{Mode: ApprovalModeBoundaryOnly}, Context: GlobalContext{SoftPct: .80, SummaryPct: .90, Accounting: "auto"}, Memory: Memory{Enabled: true, Dir: "memory", MaxTokens: 1500}, Deliver: defaultDeliver(), OperatorFiles: OperatorFiles{LogRetentionDays: 30}, Notifications: defaultNotifications(), Updates: defaultUpdates(), Telemetry: defaultTelemetry(), Reflection: defaultReflection(),
		Tools:   Tools{ReadFile: ReadFileTool{DefaultLimit: 16 << 10, MaxLimit: 64 << 10}, Attachments: AttachmentTool{MaxBytes: 256 << 20, InlineMaxBytes: 2 << 20}, ListDir: ListDirTool{MaxEntries: 300, Ignore: []string{".git", "node_modules", "__pycache__", "vendor", "bin", "obj", "dist", ".venv"}}, Grep: GrepTool{MaxMatches: 50, MaxLineChars: 200}, Shell: ShellTool{OperatorCommands: []string{"git"}}, Fetch: FetchTool{TimeoutS: 20, MaxBytes: 2 << 20, MaxRedirects: 5, DefaultLimit: 16 << 10, MaxLimit: 64 << 10, AllowDomains: []string{}, DenyDomains: []string{}, AllowInternalHosts: []string{}}, WebSearch: WebSearchTool{Enabled: true, Engines: []string{"duckduckgo_html", "duckduckgo_lite", "bing", "brave", "wikipedia", "github", "hacker_news", "arxiv", "stackexchange", "pkg_go_dev", "npm"}, PerEngineTimeoutS: 8, BenchDurationMinutes: 30, initialized: true}, FindFiles: FindFilesTool{SkipRoots: []string{"Windows", "$Recycle.Bin", "System Volume Information", `ProgramData\Microsoft\Windows Defender*`, `Program Files\Windows Defender*`}}},
		Shell:   Shell{Command: []string{"powershell", "-NoProfile", "-NonInteractive", "-Command"}, TimeoutS: 60, MaxTimeoutS: 600, MaxOutputLinesHead: 60, MaxOutputLinesTail: 40, OperatorContextIdleTimeoutMinutes: 20, Deny: []string{"rm -rf /", "format ", "diskpart", "shutdown", "Remove-Item -Recurse -Force C:\\"}, ServiceAccount: ShellServiceAccount{Enabled: false, Account: "agentb-svc", Domain: ".", initialized: true}},
		Signing: Signing{TimestampURL: "http://timestamp.digicert.com"},
	}
}

func applyDefaults(c *Config) {
	d := Defaults(c.Workspace)
	legacyFull := FullToolset()[:12]
	previousFull := FullToolset()[:13]
	for index := range c.Agents {
		if equalStrings(c.Agents[index].Toolset, legacyFull) {
			c.Agents[index].Toolset = append(append([]string(nil), legacyFull...), "delegate", "cronjob")
		} else if equalStrings(c.Agents[index].Toolset, previousFull) {
			c.Agents[index].Toolset = append(append([]string(nil), previousFull...), "cronjob")
		}
	}
	if c.ConfigVersion == 0 {
		c.ConfigVersion = CurrentConfigVersion
	}
	if c.Listen == "" {
		c.Listen = d.Listen
	}
	if c.LogDir == "" {
		c.LogDir = d.LogDir
	}
	if c.Workspace == "" {
		c.Workspace = d.Workspace
	}
	if c.Run.MaxTurns == 0 {
		c.Run.MaxTurns = d.Run.MaxTurns
	}
	if c.Run.MaxWallClockSeconds == 0 {
		c.Run.MaxWallClockSeconds = d.Run.MaxWallClockSeconds
	}
	if c.Run.MaxToolCalls == 0 {
		c.Run.MaxToolCalls = d.Run.MaxToolCalls
	}
	if c.Run.MaxConcurrent == 0 {
		c.Run.MaxConcurrent = d.Run.MaxConcurrent
	}
	if c.Cron.MaxRunMinutes == 0 {
		c.Cron.MaxRunMinutes = d.Cron.MaxRunMinutes
	}
	if c.Approval.Mode == "" {
		c.Approval = d.Approval
	}
	// a config still holding the old defaults moves to the new ones;
	// any other value is the user's and stays.
	if c.Context.SoftPct == .75 && c.Context.SummaryPct == .85 {
		c.Context.SoftPct, c.Context.SummaryPct = d.Context.SoftPct, d.Context.SummaryPct
	}
	if c.Context.SoftPct == 0 {
		c.Context.SoftPct = d.Context.SoftPct
	}
	if c.Context.SummaryPct == 0 {
		c.Context.SummaryPct = d.Context.SummaryPct
	}
	if c.Context.Accounting == "" {
		c.Context.Accounting = d.Context.Accounting
	}
	if c.Memory.Dir == "" {
		c.Memory = d.Memory
	}
	if c.Services == nil {
		c.Services = map[string]Service{}
	}
	if !c.Sandbox.initialized {
		c.Sandbox = d.Sandbox
	}
	// Setup treats an explicitly empty connection catalog as an array. Keep
	// that public JSON shape stable even when a PowerShell JSON round trip or
	// an older config represented the empty list as null.
	if c.Connections == nil {
		c.Connections = []Connection{}
	}
	if len(c.Connections) == 0 {
		c.Agents = []Agent{}
	}
	if !c.Updates.initialized {
		c.Updates = d.Updates
	}
	if !c.Telemetry.initialized {
		c.Telemetry = d.Telemetry
	}
	if !c.Reflection.initialized {
		c.Reflection = d.Reflection
	}
	if !c.Reflection.Floor.initialized {
		c.Reflection.Floor = d.Reflection.Floor
	}
	if !c.Deliver.initialized {
		c.Deliver = d.Deliver
	}
	if c.OperatorFiles.LogRetentionDays == 0 {
		c.OperatorFiles.LogRetentionDays = d.OperatorFiles.LogRetentionDays
	}
	if !c.Notifications.initialized {
		c.Notifications = d.Notifications
	}
	if c.Notifications.DiscordCredential == "" {
		c.Notifications.DiscordCredential = d.Notifications.DiscordCredential
	}
	if c.Tools.ReadFile.DefaultLimit == 0 {
		c.Tools = d.Tools
	} else if c.Tools.Fetch.TimeoutS == 0 {
		c.Tools.Fetch = d.Tools.Fetch
	}
	if c.Tools.Attachments.MaxBytes == 0 {
		c.Tools.Attachments = d.Tools.Attachments
	}
	if c.Tools.Fetch.DenyDomains == nil {
		c.Tools.Fetch.DenyDomains = append([]string(nil), d.Tools.Fetch.DenyDomains...)
	}
	if !c.Tools.WebSearch.initialized {
		c.Tools.WebSearch = d.Tools.WebSearch
	}
	if c.Tools.FindFiles.SkipRoots == nil {
		c.Tools.FindFiles.SkipRoots = append([]string(nil), d.Tools.FindFiles.SkipRoots...)
	}
	if c.Tools.Shell.OperatorCommands == nil {
		c.Tools.Shell.OperatorCommands = append([]string(nil), d.Tools.Shell.OperatorCommands...)
	}
	if len(c.Shell.Command) == 0 {
		c.Shell = d.Shell
	}
	if c.Signing.TimestampURL == "" {
		c.Signing.TimestampURL = d.Signing.TimestampURL
	}
	if c.Shell.OperatorContextIdleTimeoutMinutes == 0 {
		c.Shell.OperatorContextIdleTimeoutMinutes = d.Shell.OperatorContextIdleTimeoutMinutes
	}
	if !c.Shell.ServiceAccount.initialized {
		c.Shell.ServiceAccount.Enabled = d.Shell.ServiceAccount.Enabled
		c.Shell.ServiceAccount.initialized = true
	}
	if strings.TrimSpace(c.Broker.URL) == "" {
		c.Broker.URL = broker.DefaultURL
	}
	if c.Shell.ServiceAccount.Account == "" {
		c.Shell.ServiceAccount.Account = d.Shell.ServiceAccount.Account
	}
	if c.Shell.ServiceAccount.Domain == "" {
		c.Shell.ServiceAccount.Domain = d.Shell.ServiceAccount.Domain
	}
	for i := range c.Connections {
		p := &c.Connections[i]
		pd := d.Connections[0]
		if !p.initialized {
			if p.RequestTimeoutS == 0 {
				p.RequestTimeoutS = pd.RequestTimeoutS
			}
			if p.ProbeMode == "" {
				p.ProbeMode = pd.ProbeMode
			}
			if p.Context.ReserveOutput == 0 {
				p.Context.ReserveOutput = pd.Context.ReserveOutput
			}
			if p.Sampling == (SamplingPair{}) {
				p.Sampling = pd.Sampling
			}
			if p.Reasoning.Control == "" {
				p.Reasoning = pd.Reasoning
			}
			p.initialized = true
		}
		if p.Label == "" {
			p.Label = p.ID
		}
		if p.Capabilities.ValidEfforts == nil {
			p.Capabilities.ValidEfforts = []string{}
		}
		if p.Capabilities.Findings == nil {
			p.Capabilities.Findings = []string{}
		}
		// Most connections are never evaluated. Give their runs the conservative
		// probe-mode-off assumptions without a timestamp or findings that would
		// falsely imply Eval ran.
		if p.Capabilities.ProbedAt == "" {
			if p.Capabilities.Server == "" {
				p.Capabilities.Server = "openai-compatible"
			}
			p.Capabilities.Streaming = true
			p.Capabilities.ToolCalls = true
			p.Capabilities.DocumentInput = true
			p.Capabilities.ImageInput = true
			p.Capabilities.Vision = VisionReadsImages
			if p.Capabilities.OverflowBehavior == "" {
				p.Capabilities.OverflowBehavior = "unknown"
			}
		}
	}
}

func ApplyDefaults(c *Config) { applyDefaults(c) }

func (c Config) ResolvedExchangeFolder() (string, error) {
	value := strings.TrimSpace(c.Deliver.ExchangeFolder)
	if value == "" {
		return "", fmt.Errorf("deliver.exchange_folder: required")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		lower := strings.ToLower(value)
		for {
			index := strings.Index(lower, "%userprofile%")
			if index < 0 {
				break
			}
			value = value[:index] + home + value[index+len("%USERPROFILE%"):]
			lower = strings.ToLower(value)
		}
	}
	value = os.ExpandEnv(value)
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("deliver.exchange_folder: must be an absolute path")
	}
	return filepath.Clean(value), nil
}

func contains(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}

func (c Config) Masked() Config {
	out := c
	out.Connections = make([]Connection, len(c.Connections))
	copy(out.Connections, c.Connections)
	for i := range out.Connections {
		out.Connections[i].exposeActive = true
		if out.Connections[i].APIKey != "" {
			out.Connections[i].APIKey = "•••• set"
		}
	}
	return out
}

func (c Config) Connection(id string) (*Connection, bool) {
	for i := range c.Connections {
		if c.Connections[i].ID == id {
			connection := c.Connections[i]
			return &connection, true
		}
	}
	return nil, false
}

// ConnectionFor is the connection a session of this role runs on. c falls back to b so
// Go works on a single-connection install; d has no fallback, because a planner
// without a d connection is refused at creation.
func (a Agent) ConnectionFor(role string) string {
	switch role {
	case "d":
		return a.D
	case "c":
		if a.C != "" {
			return a.C
		}
	}
	return a.B
}

func (c Config) Agent(id string) (*Agent, bool) {
	if id == "agent-b" {
		id = "agent_b"
	}
	for i := range c.Agents {
		if AgentID(c.Agents[i].Name) == id {
			agent := c.Agents[i]
			agent.Toolset = append([]string(nil), agent.Toolset...)
			return &agent, true
		}
	}
	for i := range c.Agents {
		if c.Agents[i].B == id {
			agent := c.Agents[i]
			agent.Toolset = append([]string(nil), agent.Toolset...)
			return &agent, true
		}
	}
	return nil, false
}

// AgentConnection resolves the connection and model an agent uses for a role.
func (c Config) AgentConnection(id, role string) (*Connection, bool) {
	agent, ok := c.Agent(id)
	if !ok {
		return nil, false
	}
	connectionID := agent.ConnectionFor(role)
	if connectionID == "" && role != "d" {
		if selected, found := c.ConnectionForProfile(id); found {
			connectionID = selected.ID
		}
	}
	connection, ok := c.Connection(connectionID)
	if !ok {
		return nil, false
	}
	if model := strings.TrimSpace(agent.Model); model != "" {
		connection.SelectModel(model, 0)
	}
	return connection, true
}

// ConnectionForProfile returns the first connection that names the profile.
// An empty profile is the built-in agent_b default.
func (c Config) ConnectionForProfile(id string) (*Connection, bool) {
	id = AgentID(id)
	for i := range c.Connections {
		profile := AgentID(c.Connections[i].AgentProfile)
		if profile == "" {
			profile = "agent_b"
		}
		if profile == id {
			connection := c.Connections[i]
			return &connection, true
		}
	}
	return nil, false
}

// ProfileForConnection resolves the prompt/tool/memory profile of a connection.
func (c Config) ProfileForConnection(connectionID string) (*Agent, bool) {
	connection, ok := c.Connection(connectionID)
	if !ok {
		return nil, false
	}
	id := AgentID(connection.AgentProfile)
	if id != "" {
		if agent, found := c.Agent(id); found {
			return agent, true
		}
		return c.Agent("agent_b")
	}
	// Config values assembled in memory by released callers can still carry the
	// pre-v15 binding. Files pass through migrateAgentProfiles before this point.
	for i := range c.Agents {
		if c.Agents[i].B == connectionID {
			return c.Agent(AgentID(c.Agents[i].Name))
		}
	}
	return c.Agent("agent_b")
}

func (c Config) DefaultAgentID() string {
	if len(c.Agents) == 0 {
		return ""
	}
	return AgentID(c.Agents[0].Name)
}

// ReserveOutputFor is the output allowance a probe proposes for a connection
// whose window it has just learned. a fixed 10,240 tokens is 3.9%
// of a 262,144-token window, and the operator lost a 672-second answer to it. The
// relationship is one eighth of the window, never below the old fixed default and
// never above MaxProposedReserveOutput, which also keeps it inside (e3)'s bound.
func ReserveOutputFor(nCtx int) int {
	if nCtx <= 0 {
		return DefaultReserveOutput
	}
	return min(max(nCtx/8, DefaultReserveOutput), MaxProposedReserveOutput)
}

// ReasoningShareFor is how much of the output allowance thinking may take when
// reasoning is on and reasoning.max_tokens is unset. sharing one
// allowance uncapped let a long thinking pass consume it and leave no answer.
func ReasoningShareFor(reserveOutput int) int {
	if reserveOutput <= 0 {
		return 0
	}
	return max(reserveOutput/2, 1)
}

// reserveOutputBound is the hard limit: half the context window, but
// never less than the default every shipped version used, so a configuration with
// a small window keeps loading.
func reserveOutputBound(nCtx int) int {
	if nCtx <= 0 {
		return 0
	}
	return max(nCtx/2, DefaultReserveOutput)
}

// MigrateConnectorCredentials is. Each `static_bearer:<ENV>` connector is
// moved into the credential store ONCE, on load: the variable is read, stored, bound to
// the connector's own origin, and READ BACK — and only then is the connector rewritten to
// `stored:<name>`. If the variable is empty, or the value does not read back, the
// connector is left unauthenticated with one line saying so. There is no fallback to the
// environment: a secret that lives in a variable any child process inherits is the thing
// this item removes, so leaving it there "just for now" would be the whole failure.
//
// It returns one notice per connector it changed, naming the connector and never the
// value. Running it again is silent, because there is nothing left to migrate.
func MigrateConnectorCredentials(cfg *Config, dataRoot string) []string {
	notices := []string{}
	if cfg == nil || len(cfg.Services) == 0 {
		return notices
	}
	vault := credential.NewVault(dataRoot)
	names := make([]string, 0, len(cfg.Services))
	for name := range cfg.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		service := cfg.Services[name]
		auth := strings.TrimSpace(service.Auth)
		if !strings.HasPrefix(auth, "static_bearer:") {
			continue
		}
		variable := strings.TrimSpace(strings.TrimPrefix(auth, "static_bearer:"))
		value := strings.TrimSpace(os.Getenv(variable))
		if value == "" {
			service.Auth = "none"
			cfg.Services[name] = service
			notices = append(notices, fmt.Sprintf("connector %q: %s is empty, so its credential could not be moved into the store and the connector is left unauthenticated", name, variable))
			continue
		}
		if !storedCredentialName.MatchString(name) {
			service.Auth = "none"
			cfg.Services[name] = service
			notices = append(notices, fmt.Sprintf("connector %q: its name cannot be a credential name, so it is left unauthenticated; add the credential in Settings and bind it", name))
			continue
		}
		origin, err := credential.NormalizeOrigin(service.BaseURL)
		if err != nil {
			service.Auth = "none"
			cfg.Services[name] = service
			notices = append(notices, fmt.Sprintf("connector %q: %v, so its credential was not moved and the connector is left unauthenticated", name, err))
			continue
		}
		if err := vault.Put(name, origin, "", value); err != nil {
			service.Auth = "none"
			cfg.Services[name] = service
			notices = append(notices, fmt.Sprintf("connector %q: its credential could not be stored (%v), so the connector is left unauthenticated", name, err))
			continue
		}
		// Put already reads the value back; this is the second half of (h)'s rule — the
		// connector is rewritten only after the store has answered with what was written.
		stored, _, err := vault.Secret(name)
		if err != nil || stored != value {
			_ = vault.Delete(name)
			service.Auth = "none"
			cfg.Services[name] = service
			notices = append(notices, fmt.Sprintf("connector %q: the stored credential did not verify, so the connector is left unauthenticated", name))
			continue
		}
		service.Auth = "stored:" + name
		cfg.Services[name] = service
		notices = append(notices, fmt.Sprintf("connector %q: its credential moved from %s into the credential store and is bound to %s", name, variable, origin))
	}
	return notices
}
