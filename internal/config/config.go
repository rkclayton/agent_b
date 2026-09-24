package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"harness/internal/credential"
)

type Config struct {
	ConfigVersion int                `json:"config_version"`
	Listen        string             `json:"listen"`
	Workspace     string             `json:"workspace"`
	LogDir        string             `json:"log_dir"`
	Connections   []Connection       `json:"connections"`
	Profiles      ProfileCatalog     `json:"profiles"`
	Services      map[string]Service `json:"services"`
	Agents        []Agent            `json:"agents"`
	Chat          Chat               `json:"chat"`
	Run           RunConfig          `json:"run"`
	Approval      Approval           `json:"approval"`
	Context       GlobalContext      `json:"context"`
	Memory        Memory             `json:"memory"`
	Tools         Tools              `json:"tools"`
	Shell         Shell              `json:"shell"`
	Sandbox       Sandbox            `json:"sandbox"`
	Deliver       Deliver            `json:"deliver"`
	OperatorFiles OperatorFiles      `json:"operator_files"`
	Notifications Notifications      `json:"notifications"`
	Updates       Updates            `json:"updates"`
	Signing       Signing            `json:"signing"`
	LoadNotices   []string           `json:"-"`
}

// ProfileCatalog selects the operator-owned namespace. Connections and host
// settings remain shared; each named entry owns chats, agents and memory.
type ProfileCatalog struct {
	Active string   `json:"active"`
	Names  []string `json:"names"`
}

type Roles struct {
	Main string `json:"main"`
	Aux  string `json:"aux"`
}

type Agent struct {
	Name           string   `json:"name"`
	B              string   `json:"b"`
	C              string   `json:"c,omitempty"`
	D              string   `json:"d,omitempty"`
	Toolset        []string `json:"toolset"`
	PromptAddendum string   `json:"prompt_addendum,omitempty"`
}

var agentIDCleaner = regexp.MustCompile(`[^a-z0-9]+`)

func AgentID(name string) string {
	id := agentIDCleaner.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	return strings.Trim(id, "-")
}

// Item 2ho (v1.6.0): TWELVE. web_search follows fetch_url because both are
// public-network reads; every older tool keeps its relative position.
func FullToolset() []string {
	return []string{"read_file", "list_dir", "write_file", "edit_file", "search", "shell", "remember", "recall", "fetch_url", "web_search", "run_script", "call_service"}
}

// MergedSearchNames are the two tools `search` replaced. A configuration
// written before v1.2.5 names them, and an agent whose toolset named them meant
// "this agent may search" - so they are read as `search` rather than rejected
// as unknown, which would have turned searching off without saying so.
var MergedSearchNames = map[string]bool{"search_text": true, "find_files": true}

// migrateToolset rewrites a pre-v1.2.5 toolset in place, without changing what
// the agent is allowed to do.
func migrateToolset(toolset []string) []string {
	migrated := make([]string, 0, len(toolset))
	seen := map[string]bool{}
	for _, name := range toolset {
		if MergedSearchNames[name] {
			name = "search"
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		migrated = append(migrated, name)
	}
	return migrated
}

type Chat struct {
	AutoRename  bool `json:"auto_rename"`
	initialized bool
}

// Updates controls the one passive release check. AutoCheck defaults on even
// for older configuration files that predate this object; an explicit false is
// preserved by the custom unmarshaller.
type Updates struct {
	AutoCheck   bool `json:"auto_check"`
	initialized bool
}

func defaultUpdates() Updates { return Updates{AutoCheck: true, initialized: true} }

func (u *Updates) UnmarshalJSON(data []byte) error {
	type plain Updates
	value := plain(defaultUpdates())
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*u = Updates(value)
	u.initialized = true
	return nil
}

func defaultChat() Chat { return Chat{AutoRename: true, initialized: true} }

func (c *Chat) UnmarshalJSON(data []byte) error {
	type plain Chat
	value := plain(defaultChat())
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*c = Chat(value)
	c.initialized = true
	return nil
}

type Connection struct {
	ID                   string       `json:"id"`
	Label                string       `json:"label"`
	BaseURL              string       `json:"base_url"`
	ExtractURL           string       `json:"extract_url"`
	AttachmentHandling   string       `json:"attachment_handling"`
	Model                string       `json:"model"`
	Credential           string       `json:"credential"`
	APIKey               string       `json:"api_key,omitempty"`
	RequestTimeoutS      int          `json:"request_timeout_s"`
	ProbeMode            string       `json:"probe_mode"`
	Sampling             SamplingPair `json:"sampling"`
	Reasoning            Reasoning    `json:"reasoning"`
	Context              Context      `json:"context"`
	SystemPromptOverride string       `json:"system_prompt_override"`
	Capabilities         Capabilities `json:"capabilities"`
	Measurement          *Measurement `json:"measurement,omitempty"`
	// MaxConcurrent is how many runs this model serves at once (item 2fc);
	// zero means one. The global run.max_concurrent still caps the total.
	MaxConcurrent int `json:"max_concurrent,omitempty"`
	initialized   bool
}

// Measurement is the optional, bounded setup-wizard capability run. It is
// descriptive evidence, never a gate: an unmeasured connection remains usable.
type Measurement struct {
	Passed        int     `json:"passed"`
	Total         int     `json:"total"`
	BriefsRun     int     `json:"briefs_run"`
	ToolErrors    int     `json:"tool_errors"`
	ToolErrorRate float64 `json:"tool_error_rate"`
	Trials        int     `json:"trials"`
	Provenance    string  `json:"provenance"`
	MeasuredAt    string  `json:"measured_at"`
	DurationMS    int64   `json:"duration_ms"`
	Capped        bool    `json:"capped"`
	Stopped       bool    `json:"stopped,omitempty"`
}

type Service struct {
	BaseURL             string   `json:"base_url"`
	Auth                string   `json:"auth"`
	AllowedMethods      []string `json:"allowed_methods"`
	TimeoutS            int      `json:"timeout_s"`
	MaxBodyKB           int      `json:"max_body_kb"`
	RequireConfirmation bool     `json:"require_confirmation"`
}

func defaultConnection() Connection {
	return Connection{
		RequestTimeoutS:    900,
		ProbeMode:          "full",
		AttachmentHandling: "auto",
		Sampling: SamplingPair{
			Thinking:    Sampling{Temperature: .6, TopP: .95, TopK: 20, RepeatPenalty: 1},
			Nonthinking: Sampling{Temperature: .7, TopP: .8, TopK: 20, PresencePenalty: 1.5, RepeatPenalty: 1},
		},
		Reasoning:    Reasoning{Control: "auto", Enabled: true, Effort: "medium", ValidEfforts: []string{}},
		Context:      Context{ReserveOutput: DefaultReserveOutput},
		Capabilities: Capabilities{ValidEfforts: []string{}, Findings: []string{}},
		initialized:  true,
	}
}

func (p Connection) NativeImageInput() bool {
	return p.AttachmentHandling == "native" || (p.AttachmentHandling == "auto" && p.Capabilities.Vision == VisionReadsImages)
}

func (p Connection) NativeDocumentInput() bool {
	return p.AttachmentHandling == "native" || (p.AttachmentHandling == "auto" && p.Capabilities.DocumentInput)
}

func (p *Connection) UnmarshalJSON(data []byte) error {
	type plain Connection
	value := plain(defaultConnection())
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*p = Connection(value)
	p.initialized = true
	return nil
}

type SamplingPair struct{ Thinking, Nonthinking Sampling }

func (s SamplingPair) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Thinking    Sampling `json:"thinking"`
		Nonthinking Sampling `json:"nonthinking"`
	}{s.Thinking, s.Nonthinking})
}
func (s *SamplingPair) UnmarshalJSON(data []byte) error {
	v := struct {
		Thinking    Sampling `json:"thinking"`
		Nonthinking Sampling `json:"nonthinking"`
	}{s.Thinking, s.Nonthinking}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	s.Thinking = v.Thinking
	s.Nonthinking = v.Nonthinking
	return nil
}

type Sampling struct {
	Temperature     float64 `json:"temperature"`
	TopP            float64 `json:"top_p"`
	TopK            int     `json:"top_k"`
	MinP            float64 `json:"min_p"`
	PresencePenalty float64 `json:"presence_penalty"`
	RepeatPenalty   float64 `json:"repeat_penalty"`
}
type Reasoning struct {
	Control      string   `json:"control"`
	Enabled      bool     `json:"enabled"`
	Effort       string   `json:"effort"`
	ValidEfforts []string `json:"valid_efforts"`
	Preserve     bool     `json:"preserve"`
	MaxTokens    int      `json:"max_tokens,omitempty"`
}
type Context struct {
	NCtx          int            `json:"n_ctx"`
	ReserveOutput int            `json:"reserve_output"`
	Sizing        *ContextSizing `json:"sizing,omitempty"`
}

// ContextSizing records the immutable install-time inputs used to choose the
// context passed to a locally installed llama-server.
type ContextSizing struct {
	WeightsBytes    int64  `json:"weights_bytes"`
	KVBytesPerToken uint64 `json:"kv_bytes_per_token"`
	AvailableBytes  uint64 `json:"available_bytes"`
	ReserveBytes    uint64 `json:"reserve_bytes"`
}
type Capabilities struct {
	Server               string   `json:"server"`
	Props                bool     `json:"props"`
	NCtx                 int      `json:"n_ctx"`
	Tokenize             bool     `json:"tokenize"`
	ApplyTemplate        bool     `json:"apply_template"`
	ApplyTemplateTools   bool     `json:"apply_template_tools"`
	Streaming            bool     `json:"streaming"`
	ToolCalls            bool     `json:"tool_calls"`
	GrammarConstrained   bool     `json:"grammar_constrained"`
	CachedTokens         bool     `json:"cached_tokens"`
	Timings              bool     `json:"timings"`
	PromptProgress       bool     `json:"prompt_progress"`
	DocumentInput        bool     `json:"document_input"`
	ImageInput           bool     `json:"image_input"`
	Vision               string   `json:"vision"`
	ReasoningControl     string   `json:"reasoning_control"`
	ReasoningEmission    string   `json:"reasoning_emission,omitempty"`
	ValidEfforts         []string `json:"valid_efforts"`
	OverflowBehavior     string   `json:"overflow_behavior"`
	ObservedMessageLimit int      `json:"observed_message_limit,omitempty"`
	ProbedAt             string   `json:"probed_at"`
	Findings             []string `json:"findings"`
}

const (
	VisionReadsImages       = "reads images"
	VisionAcceptsUnreadable = "accepts images but does not read them"
	VisionRejected          = "rejected"
)

type RunConfig struct {
	MaxTurns                 int `json:"max_turns"`
	MaxWallClockSeconds      int `json:"max_wall_clock_seconds"`
	MaxToolCalls             int `json:"max_tool_calls"`
	CycleWindow              int `json:"cycle_window"`
	MaxConsecutiveToolErrors int `json:"max_consecutive_tool_errors"`
	MaxConcurrent            int `json:"max_concurrent"`
	QueueDepth               int `json:"queue_depth"`
}

const DefaultMaxTurns = 10000
const DefaultMaxWallClockSeconds = 6 * 60 * 60
const DefaultMaxToolCalls = 1000

type Approval struct {
	Mode string `json:"mode"`
	// Item 5f (v1.2.5): unattended means nothing asks. It changes WHO IS ASKED,
	// never who runs anything and never what the boundary permits - a refused
	// thing is still refused, it is just recorded rather than raised.
	Unattended bool `json:"unattended"`
}

type Deliver struct {
	Mode           string `json:"mode"`
	ExchangeFolder string `json:"exchange_folder"`
	initialized    bool
}

type OperatorFiles struct {
	AllowMailboxApprovals bool `json:"allow_mailbox_approvals"`
	LogRetentionDays      int  `json:"log_retention_days"`
}

type Notifications struct {
	DiscordCredential string `json:"discord_credential"`
}

func defaultDeliver() Deliver {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("USERPROFILE")
	}
	return Deliver{Mode: DeliverModeBoth, ExchangeFolder: filepath.Join(home, "Agent_b"), initialized: true}
}

func (d *Deliver) UnmarshalJSON(data []byte) error {
	type plain Deliver
	value := plain(defaultDeliver())
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*d = Deliver(value)
	d.initialized = true
	return nil
}

const (
	CurrentConfigVersion     = 8
	DefaultReserveOutput     = 10240
	ApprovalModeBoundaryOnly = "boundary-only"
	ApprovalModeMutating     = "mutating"
	ApprovalModeAll          = "all"
	ApprovalModeOff          = "off"
	DeliverModeChips         = "chips"
	DeliverModeFolder        = "folder"
	DeliverModeBoth          = "both"
)

const ApprovalDefaultMigrationNotice = "corrected inherited approval default from mutating to boundary-only; mutating can be reselected in Settings > Run & approval"
const OperatorIdleTimeoutMigrationNotice = "migrated shell.operator_context_timeout_minutes to shell.operator_context_idle_timeout_minutes; operator mode now expires after agent inactivity"
const ByteWindowMigrationNotice = "migrated read_file and fetch_url limits from line counts to UTF-8 byte windows"
const ModelRolesMigrationNotice = "migrated model connections to schema 5 with an explicit main role, optional aux role, and per-connection context size"
const AgentObjectsMigrationNotice = "migrated connection roles to schema 6 with one named agent, bound b/c connections, and the full toolset"

type GlobalContext struct {
	SoftPct    float64 `json:"soft_pct"`
	SummaryPct float64 `json:"summary_pct"`
	Accounting string  `json:"accounting"`
}
type Memory struct {
	Enabled   bool   `json:"enabled"`
	Dir       string `json:"dir"`
	MaxTokens int    `json:"max_tokens"`
}
type Tools struct {
	ReadFile    ReadFileTool   `json:"read_file"`
	Attachments AttachmentTool `json:"attachments"`
	ListDir     ListDirTool    `json:"list_dir"`
	Grep        GrepTool       `json:"grep"`
	Shell       ShellTool      `json:"shell"`
	Fetch       FetchTool      `json:"fetch"`
	WebSearch   WebSearchTool  `json:"web_search"`
	FindFiles   FindFilesTool  `json:"find_files"`
}

type AttachmentTool struct {
	MaxBytes int64 `json:"max_bytes"`
	// Item 2ch (v1.2.5): a PDF has two routes and the branch order used to
	// choose. The operator chose: SIDECAR BY DEFAULT, inline only under this
	// many bytes and only where the connection can read a document natively.
	// Inline is higher fidelity and unbounded; a sidecar is bounded and lossy,
	// and the unbounded one is not what should happen by accident.
	InlineMaxBytes int64 `json:"inline_max_bytes"`
}

// InlineDocumentLimit is the threshold in bytes, with the stated default.
func (a AttachmentTool) InlineDocumentLimit() int64 {
	if a.InlineMaxBytes > 0 {
		return a.InlineMaxBytes
	}
	return 2 << 20
}

type ShellTool struct {
	OperatorCommands []string `json:"operator_commands"`
}
type ReadFileTool struct {
	DefaultLimit int `json:"default_limit"`
	MaxLimit     int `json:"max_limit"`
}
type ListDirTool struct {
	MaxEntries int      `json:"max_entries"`
	Ignore     []string `json:"ignore"`
}
type GrepTool struct {
	MaxMatches   int `json:"max_matches"`
	MaxLineChars int `json:"max_line_chars"`
}
type FindFilesTool struct {
	SkipRoots []string `json:"skip_roots"`
}
type FetchTool struct {
	TimeoutS           int      `json:"timeout_s"`
	MaxBytes           int64    `json:"max_bytes"`
	MaxRedirects       int      `json:"max_redirects"`
	DefaultLimit       int      `json:"default_limit"`
	MaxLimit           int      `json:"max_limit"`
	AllowDomains       []string `json:"allow_domains"`
	DenyDomains        []string `json:"deny_domains"`
	AllowInternalHosts []string `json:"allow_internal_hosts"`
}
type WebSearchTool struct {
	Enabled              bool     `json:"enabled"`
	Engines              []string `json:"engines"`
	PerEngineTimeoutS    int      `json:"per_engine_timeout_s"`
	BenchDurationMinutes int      `json:"bench_duration_minutes"`
	initialized          bool
}

func (w *WebSearchTool) UnmarshalJSON(data []byte) error {
	type plain WebSearchTool
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*w = WebSearchTool(value)
	w.initialized = true
	return nil
}

type Shell struct {
	Command            []string `json:"command"`
	TimeoutS           int      `json:"timeout_s"`
	MaxTimeoutS        int      `json:"max_timeout_s"`
	MaxOutputLinesHead int      `json:"max_output_lines_head"`
	MaxOutputLinesTail int      `json:"max_output_lines_tail"`
	FileRoutingGuard   *bool    `json:"file_routing_guard"`
	// OperatorContext is exposed to the UI but Save always persists it as false.
	OperatorContext                   bool                `json:"operator_context"`
	OperatorContextExpiresAt          string              `json:"-"`
	OperatorContextIdleTimeoutMinutes int                 `json:"operator_context_idle_timeout_minutes"`
	ServiceAccount                    ShellServiceAccount `json:"service_account"`
	AllowLocalNetwork                 bool                `json:"allow_local_network"`
	ConfirmedLocalSubnets             []string            `json:"confirmed_local_subnets"`
	AllowedModelRanges                []string            `json:"allowed_model_ranges"`
	Deny                              []string            `json:"deny"`
}

type ShellServiceAccount struct {
	Enabled bool   `json:"enabled"`
	Account string `json:"account"`
	Domain  string `json:"domain"`
}

type Sandbox struct {
	Enabled     bool `json:"enabled"`
	initialized bool
}

func (s *Sandbox) UnmarshalJSON(data []byte) error {
	var legacy struct {
		Enabled    *bool           `json:"enabled"`
		Workspaces map[string]bool `json:"workspaces"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return err
	}
	s.initialized = true
	if legacy.Enabled != nil {
		s.Enabled = *legacy.Enabled
		return nil
	}
	if legacy.Workspaces == nil {
		s.Enabled = true
		return nil
	}
	for _, enabled := range legacy.Workspaces {
		if enabled {
			s.Enabled = true
			break
		}
	}
	return nil
}

func (c Config) SandboxTarget(sessionID string, mounts []string) (string, bool) {
	if !c.Sandbox.Enabled {
		return "", false
	}
	identity := strings.ToLower(strings.TrimSpace(sessionID)) + "\x00" + strings.Join(mounts, "\x00")
	digest := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("agentb-%x", digest[:6]), true
}

type Signing struct {
	Thumbprint   string `json:"thumbprint"`
	TimestampURL string `json:"timestamp_url"`
}

func Defaults(workspace string) Config {
	if workspace == "" {
		workspace = "./workspace"
	}
	abs, _ := filepath.Abs(workspace)
	connection := defaultConnection()
	connection.ID, connection.Label, connection.BaseURL, connection.Model = "local", "Local", "http://127.0.0.1:8080", "model"
	return Config{
		ConfigVersion: CurrentConfigVersion,
		Listen:        "127.0.0.1:8790", Workspace: abs, LogDir: "logs",
		Connections: []Connection{connection}, Agents: []Agent{{Name: connection.Label, B: "local", Toolset: FullToolset()}}, Chat: defaultChat(),
		Services: map[string]Service{},
		Sandbox:  Sandbox{Enabled: true, initialized: true},
		Run:      RunConfig{MaxTurns: DefaultMaxTurns, MaxWallClockSeconds: DefaultMaxWallClockSeconds, MaxToolCalls: DefaultMaxToolCalls, CycleWindow: 8, MaxConsecutiveToolErrors: 3, MaxConcurrent: 2}, Approval: Approval{Mode: ApprovalModeBoundaryOnly}, Context: GlobalContext{SoftPct: .75, SummaryPct: .85, Accounting: "auto"}, Memory: Memory{Enabled: true, Dir: "memory", MaxTokens: 1500}, Deliver: defaultDeliver(), OperatorFiles: OperatorFiles{LogRetentionDays: 30}, Notifications: Notifications{DiscordCredential: "discord-webhook"}, Updates: defaultUpdates(),
		Tools:   Tools{ReadFile: ReadFileTool{DefaultLimit: 16 << 10, MaxLimit: 64 << 10}, Attachments: AttachmentTool{MaxBytes: 8 << 20, InlineMaxBytes: 2 << 20}, ListDir: ListDirTool{MaxEntries: 300, Ignore: []string{".git", "node_modules", "__pycache__", "vendor", "bin", "obj", "dist", ".venv"}}, Grep: GrepTool{MaxMatches: 50, MaxLineChars: 200}, Shell: ShellTool{OperatorCommands: []string{"git"}}, Fetch: FetchTool{TimeoutS: 20, MaxBytes: 2 << 20, MaxRedirects: 5, DefaultLimit: 16 << 10, MaxLimit: 64 << 10, AllowDomains: []string{}, DenyDomains: []string{"ipinfo.io", "ipapi.co", "ip-api.com", "ifconfig.me", "ipify.org", "geojs.io", "ipgeolocation.io", "icanhazip.com"}, AllowInternalHosts: []string{}}, WebSearch: WebSearchTool{Enabled: true, Engines: []string{"duckduckgo_html", "duckduckgo_lite", "bing", "brave", "startpage", "mojeek", "wikipedia", "github", "hacker_news", "arxiv", "stackexchange", "pkg_go_dev", "npm"}, PerEngineTimeoutS: 8, BenchDurationMinutes: 30, initialized: true}, FindFiles: FindFilesTool{SkipRoots: []string{"Windows", "$Recycle.Bin", "System Volume Information", `ProgramData\Microsoft\Windows Defender*`, `Program Files\Windows Defender*`}}},
		Shell:   Shell{Command: []string{"powershell", "-NoProfile", "-NonInteractive", "-Command"}, TimeoutS: 60, MaxTimeoutS: 600, MaxOutputLinesHead: 60, MaxOutputLinesTail: 40, OperatorContextIdleTimeoutMinutes: 20, Deny: []string{"rm -rf /", "format ", "diskpart", "shutdown", "Remove-Item -Recurse -Force C:\\"}, FileRoutingGuard: boolPointer(true), ServiceAccount: ShellServiceAccount{Account: "agentb-svc", Domain: "."}},
		Signing: Signing{TimestampURL: "http://timestamp.digicert.com"},
	}
}

func Load(path string) (*Config, bool, bool, error) {
	return LoadWithTemplate(path, filepath.Join(filepath.Dir(path), "harness.example.json"))
}

// LoadWithTemplate loads the live configuration from path and, on first run,
// materializes it from the explicitly supplied application template.
func LoadWithTemplate(path, examplePath string) (*Config, bool, bool, error) {
	return LoadWithRoots(path, examplePath, filepath.Dir(path))
}

// LoadWithRoots resolves connection credential references against the explicit
// operator data root rather than deriving their location from the config path.
func LoadWithRoots(path, examplePath, dataRoot string) (*Config, bool, bool, error) {
	data, err := os.ReadFile(path)
	created := false
	if os.IsNotExist(err) {
		data, err = os.ReadFile(examplePath)
		if err != nil {
			return nil, false, false, fmt.Errorf("%s is missing; read %s: %w", filepath.Base(path), filepath.Base(examplePath), err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, false, false, fmt.Errorf("create config directory: %w", err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, false, false, fmt.Errorf("create %s from %s: %w", filepath.Base(path), filepath.Base(examplePath), err)
		}
		created = true
	}
	if err != nil {
		return nil, false, false, err
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	var metadata struct {
		ConfigVersion *int `json:"config_version"`
		Run           struct {
			MaxTurns            *int `json:"max_turns"`
			MaxWallClockSeconds *int `json:"max_wall_clock_seconds"`
			MaxToolCalls        *int `json:"max_tool_calls"`
		} `json:"run"`
		Approval struct {
			Mode string `json:"mode"`
		} `json:"approval"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, false, created, err
	}
	if metadata.Run.MaxTurns != nil && *metadata.Run.MaxTurns == 0 {
		return nil, false, created, fmt.Errorf("run.max_turns: zero is not unlimited; omit it for the default %d or use a positive pathological-case backstop", DefaultMaxTurns)
	}
	if metadata.Run.MaxWallClockSeconds != nil && *metadata.Run.MaxWallClockSeconds == 0 {
		return nil, false, created, fmt.Errorf("run.max_wall_clock_seconds: zero is not unlimited; omit it for the default %d or use a positive backstop", DefaultMaxWallClockSeconds)
	}
	if metadata.Run.MaxToolCalls != nil && *metadata.Run.MaxToolCalls == 0 {
		return nil, false, created, fmt.Errorf("run.max_tool_calls: zero is not unlimited; omit it for the default %d or use a positive backstop", DefaultMaxToolCalls)
	}
	unstamped := metadata.ConfigVersion == nil
	if !unstamped && *metadata.ConfigVersion != 2 && *metadata.ConfigVersion != 3 && *metadata.ConfigVersion != 4 && *metadata.ConfigVersion != 5 && *metadata.ConfigVersion != 6 && *metadata.ConfigVersion != 7 && *metadata.ConfigVersion != CurrentConfigVersion {
		return nil, false, created, fmt.Errorf("config_version: unsupported value %d (current %d)", *metadata.ConfigVersion, CurrentConfigVersion)
	}
	migrated, data, err := migrateV1(data)
	if err != nil {
		return nil, false, created, err
	}
	version := 0
	if metadata.ConfigVersion != nil {
		version = *metadata.ConfigVersion
	}
	connectionKeyMigrated, data, err := migrateConnectionKey(data)
	if err != nil {
		return nil, false, created, err
	}
	schemaMigrated, idleTimeoutRemapped, data, err := migrateOperatorIdleTimeout(data, version)
	if err != nil {
		return nil, false, created, err
	}
	byteWindowMigrated, data, err := migrateByteWindows(data, version)
	if err != nil {
		return nil, false, created, err
	}
	modelRolesMigrated, data, err := migrateModelConnections(data, version)
	if err != nil {
		return nil, false, created, err
	}
	agentsMigrated, data, err := migrateAgentObjects(data, version)
	if err != nil {
		return nil, false, created, err
	}
	webSearchMigrated, data, err := migrateWebSearch(data, version)
	if err != nil {
		return nil, false, created, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, false, created, err
	}
	// Operator context is a launch-scoped state, never a startup instruction.
	cfg.Shell.OperatorContext = false
	cfg.Shell.OperatorContextExpiresAt = ""
	applyDefaults(&cfg)
	// Item 13 (v1.2.5): a configuration written before the merge names the two
	// tools that became `search`. It meant "this agent may search", so it is
	// read that way rather than refused as unknown.
	for i := range cfg.Agents {
		cfg.Agents[i].Toolset = migrateToolset(cfg.Agents[i].Toolset)
	}
	approvalDefaultCorrected := unstamped && metadata.Approval.Mode == ApprovalModeMutating
	if approvalDefaultCorrected {
		cfg.Approval.Mode = ApprovalModeBoundaryOnly
	}
	if err := cfg.Validate(); err != nil {
		return nil, false, created, err
	}
	if err := ResolveConnectionCredentials(&cfg, dataRoot); err != nil {
		return nil, false, created, err
	}
	if migrated || connectionKeyMigrated || schemaMigrated || byteWindowMigrated || modelRolesMigrated || agentsMigrated || webSearchMigrated || unstamped {
		if err := cfg.Save(path); err != nil {
			return nil, false, created, err
		}
	}
	if approvalDefaultCorrected {
		cfg.LoadNotices = append(cfg.LoadNotices, ApprovalDefaultMigrationNotice)
	}
	if idleTimeoutRemapped {
		cfg.LoadNotices = append(cfg.LoadNotices, OperatorIdleTimeoutMigrationNotice)
	}
	if byteWindowMigrated && !unstamped {
		cfg.LoadNotices = append(cfg.LoadNotices, ByteWindowMigrationNotice)
	}
	if modelRolesMigrated && !unstamped {
		cfg.LoadNotices = append(cfg.LoadNotices, ModelRolesMigrationNotice)
	}
	if agentsMigrated && !unstamped {
		cfg.LoadNotices = append(cfg.LoadNotices, AgentObjectsMigrationNotice)
	}
	return &cfg, migrated || connectionKeyMigrated || schemaMigrated || byteWindowMigrated || modelRolesMigrated || agentsMigrated || webSearchMigrated, created, nil
}

func (c Config) Save(path string) error {
	persisted := c
	persisted.Connections = append([]Connection(nil), c.Connections...)
	for i := range persisted.Connections {
		persisted.Connections[i].APIKey = ""
	}
	persisted.ConfigVersion = CurrentConfigVersion
	if err := persisted.Validate(); err != nil {
		return err
	}
	persisted.Shell.OperatorContext = false
	persisted.Shell.OperatorContextExpiresAt = ""
	data, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

func ResolveConnectionCredentials(cfg *Config, dataRoot string) error {
	for i := range cfg.Connections {
		connection := &cfg.Connections[i]
		if connection.APIKey == "" && connection.Credential == "" {
			continue
		}
		if connection.Credential == "" {
			connection.Credential = connection.ID
		}
		store, err := credential.NewNamed(dataRoot, connection.Credential)
		if err != nil {
			return fmt.Errorf("connections[%d].credential: %w", i, err)
		}
		if connection.APIKey != "" {
			if err := store.Write([]byte(connection.APIKey)); err != nil {
				return fmt.Errorf("store credential %q: %w", connection.Credential, err)
			}
			continue
		}
		value, err := store.Read()
		if err != nil {
			if errors.Is(err, credential.ErrNotStored) {
				return fmt.Errorf("connections[%d].credential: named credential %q is not stored", i, connection.Credential)
			}
			return fmt.Errorf("connections[%d].credential: %w", i, err)
		}
		connection.APIKey = string(value)
		clearBytes(value)
	}
	return nil
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

var slug = regexp.MustCompile(`^[a-z0-9-]+$`)
var serviceEnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (c Config) Validate() error {
	if c.ConfigVersion != CurrentConfigVersion {
		return fmt.Errorf("config_version: unsupported value %d (current %d)", c.ConfigVersion, CurrentConfigVersion)
	}
	if c.Listen == "" {
		return fmt.Errorf("listen: required")
	}
	if c.Workspace == "" {
		return fmt.Errorf("workspace: required")
	}
	if !c.Shell.AllowLocalNetwork && len(c.Shell.ConfirmedLocalSubnets) > 0 {
		return fmt.Errorf("shell.confirmed_local_subnets: must be empty while allow_local_network is off")
	}
	for _, raw := range c.Shell.ConfirmedLocalSubnets {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || !prefix.Addr().Is4() || !prefix.Masked().Addr().IsPrivate() || prefix.Bits() < 8 || prefix.Bits() > 32 || prefix.String() != prefix.Masked().String() {
			return fmt.Errorf("shell.confirmed_local_subnets: %q must be a canonical private IPv4 prefix", raw)
		}
	}
	for _, raw := range c.Shell.AllowedModelRanges {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || !prefix.Addr().Is4() || prefix.Bits() < 8 || prefix.Bits() > 32 || prefix.String() != prefix.Masked().String() {
			return fmt.Errorf("shell.allowed_model_ranges: %q must be a canonical IPv4 prefix between /8 and /32", raw)
		}
	}
	seen := map[string]bool{}
	for i, p := range c.Connections {
		prefix := fmt.Sprintf("connections[%d]", i)
		if !slug.MatchString(p.ID) {
			return fmt.Errorf("%s.id: must be a slug", prefix)
		}
		if seen[p.ID] {
			return fmt.Errorf("%s.id: duplicate", prefix)
		}
		seen[p.ID] = true
		if p.MaxConcurrent < 0 {
			return fmt.Errorf("%s.max_concurrent: cannot be negative", prefix)
		}
		if p.Credential != "" {
			if _, err := credential.NewNamed(".", p.Credential); err != nil {
				return fmt.Errorf("%s.credential: %w", prefix, err)
			}
		}
		if p.ProbeMode != "full" && p.ProbeMode != "minimal" && p.ProbeMode != "off" {
			return fmt.Errorf("%s.probe_mode: invalid", prefix)
		}
		if !oneOf(p.AttachmentHandling, "auto", "native", "extract") {
			return fmt.Errorf("%s.attachment_handling: invalid", prefix)
		}
		if p.RequestTimeoutS < 1 {
			return fmt.Errorf("%s.request_timeout_s: must be positive", prefix)
		}
		if p.ExtractURL != "" {
			endpoint, err := url.Parse(strings.TrimSpace(p.ExtractURL))
			if err != nil || endpoint == nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Fragment != "" {
				return fmt.Errorf("%s.extract_url: must be an absolute HTTP(S) URL without user information or fragment", prefix)
			}
		}
		if !oneOf(p.Reasoning.Control, "auto", "chat_template_kwargs", "top_level", "server_flag", "none") {
			return fmt.Errorf("%s.reasoning.control: invalid", prefix)
		}
		if len(p.Reasoning.ValidEfforts) > 0 && !contains(p.Reasoning.ValidEfforts, p.Reasoning.Effort) {
			return fmt.Errorf("%s.reasoning.effort: not in valid_efforts", prefix)
		}
		if p.Reasoning.MaxTokens < 0 {
			return fmt.Errorf("%s.reasoning.max_tokens: cannot be negative", prefix)
		}
		if p.Context.NCtx < 0 {
			return fmt.Errorf("%s.context.n_ctx: cannot be negative", prefix)
		}
		if p.Context.ReserveOutput < 0 {
			return fmt.Errorf("%s.context.reserve_output: cannot be negative", prefix)
		}
		if p.Capabilities.Props && p.Capabilities.NCtx > 0 && p.Context.NCtx > p.Capabilities.NCtx {
			return fmt.Errorf("%s.context.n_ctx: may not exceed probed n_ctx", prefix)
		}
		if p.ProbeMode == "off" && p.Context.NCtx == 0 {
			return fmt.Errorf("%s.context.n_ctx: required when probe_mode is off", prefix)
		}
	}
	if len(c.Connections) == 0 && len(c.Agents) != 0 {
		return fmt.Errorf("agents: must be empty until a connection is configured")
	}
	if len(c.Connections) > 0 && len(c.Agents) == 0 {
		return fmt.Errorf("agents: at least one agent is required")
	}
	agentIDs := map[string]bool{}
	knownTools := map[string]bool{}
	for _, name := range FullToolset() {
		knownTools[name] = true
	}
	for i, agent := range c.Agents {
		prefix := fmt.Sprintf("agents[%d]", i)
		name := strings.TrimSpace(agent.Name)
		if name == "" || AgentID(name) == "" {
			return fmt.Errorf("%s.name: required", prefix)
		}
		if strings.EqualFold(name, "agent_a") {
			return fmt.Errorf("%s.name: agent_a is reserved", prefix)
		}
		id := AgentID(name)
		if agentIDs[id] {
			return fmt.Errorf("%s.name: duplicate agent id %s", prefix, id)
		}
		agentIDs[id] = true
		if !seen[agent.B] {
			return fmt.Errorf("%s.b: must name an existing connection", prefix)
		}
		if agent.C != "" && !seen[agent.C] {
			return fmt.Errorf("%s.c: must be empty or name an existing connection", prefix)
		}
		if agent.D != "" && !seen[agent.D] {
			return fmt.Errorf("%s.d: must be empty or name an existing connection", prefix)
		}
		toolSeen := map[string]bool{}
		for _, tool := range agent.Toolset {
			if !knownTools[tool] {
				return fmt.Errorf("%s.toolset: unknown tool %s", prefix, tool)
			}
			if toolSeen[tool] {
				return fmt.Errorf("%s.toolset: duplicate tool %s", prefix, tool)
			}
			toolSeen[tool] = true
		}
	}
	for name, service := range c.Services {
		prefix := "services." + name
		if !slug.MatchString(name) {
			return fmt.Errorf("services: names must be slugs")
		}
		endpoint, err := url.Parse(strings.TrimSpace(service.BaseURL))
		if err != nil || endpoint == nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
			return fmt.Errorf("%s.base_url: must be an absolute HTTP(S) URL without user information, query, or fragment", prefix)
		}
		if err := validateServiceAuth(service.Auth); err != nil {
			return fmt.Errorf("%s.auth: %w", prefix, err)
		}
		if len(service.AllowedMethods) == 0 {
			return fmt.Errorf("%s.allowed_methods: at least one method is required", prefix)
		}
		for _, method := range service.AllowedMethods {
			if !oneOf(strings.ToUpper(strings.TrimSpace(method)), "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE") {
				return fmt.Errorf("%s.allowed_methods: method %q is invalid", prefix, method)
			}
		}
		if service.TimeoutS < 1 || service.TimeoutS > 300 {
			return fmt.Errorf("%s.timeout_s: must be between 1 and 300", prefix)
		}
		if service.MaxBodyKB < 1 || service.MaxBodyKB > 65536 {
			return fmt.Errorf("%s.max_body_kb: must be between 1 and 65536", prefix)
		}
	}
	if c.Run.MaxTurns < 1 {
		return fmt.Errorf("run.max_turns: must be positive; zero is not unlimited (omit it for the default %d)", DefaultMaxTurns)
	}
	if c.Run.MaxWallClockSeconds < 1 {
		return fmt.Errorf("run.max_wall_clock_seconds: must be positive")
	}
	if c.Run.MaxToolCalls < 1 {
		return fmt.Errorf("run.max_tool_calls: must be positive")
	}
	if c.Run.MaxConcurrent < 1 {
		return fmt.Errorf("run.max_concurrent: must be positive")
	}
	if c.Run.QueueDepth < 0 {
		return fmt.Errorf("run.queue_depth: cannot be negative")
	}
	if c.Run.CycleWindow < 0 {
		return fmt.Errorf("run.cycle_window: cannot be negative")
	}
	if c.Run.MaxConsecutiveToolErrors < 0 {
		return fmt.Errorf("run.max_consecutive_tool_errors: cannot be negative")
	}
	if !oneOf(c.Approval.Mode, ApprovalModeBoundaryOnly, ApprovalModeMutating, ApprovalModeAll, ApprovalModeOff) {
		return fmt.Errorf("approval.mode: invalid")
	}
	if !(c.Context.SoftPct > 0 && c.Context.SoftPct < c.Context.SummaryPct && c.Context.SummaryPct <= 1) {
		return fmt.Errorf("context: require soft_pct < summary_pct <= 1")
	}
	if !oneOf(c.Context.Accounting, "auto", "exact", "estimated") {
		return fmt.Errorf("context.accounting: invalid")
	}
	if c.Memory.MaxTokens < 0 {
		return fmt.Errorf("memory.max_tokens: cannot be negative")
	}
	if c.Memory.Dir == "" {
		return fmt.Errorf("memory.dir: required")
	}
	if c.Tools.ReadFile.DefaultLimit < 1 {
		return fmt.Errorf("tools.read_file.default_limit: must be positive")
	}
	if c.Tools.ReadFile.MaxLimit < 1 {
		return fmt.Errorf("tools.read_file.max_limit: must be positive")
	}
	if c.Tools.Attachments.MaxBytes < 1 || c.Tools.Attachments.MaxBytes > 128<<20 {
		return fmt.Errorf("tools.attachments.max_bytes: must be between 1 and 134217728")
	}
	if c.Tools.ListDir.MaxEntries < 1 {
		return fmt.Errorf("tools.list_dir.max_entries: must be positive")
	}
	if c.Tools.Grep.MaxMatches < 1 || c.Tools.Grep.MaxLineChars < 1 {
		return fmt.Errorf("tools.grep: limits must be positive")
	}
	if c.Tools.Fetch.TimeoutS < 1 || c.Tools.Fetch.TimeoutS > 300 {
		return fmt.Errorf("tools.fetch.timeout_s: must be between 1 and 300")
	}
	if c.Tools.Fetch.MaxBytes < 1 || c.Tools.Fetch.MaxBytes > 64<<20 {
		return fmt.Errorf("tools.fetch.max_bytes: must be between 1 and 67108864")
	}
	if c.Tools.Fetch.MaxRedirects < 0 || c.Tools.Fetch.MaxRedirects > 20 {
		return fmt.Errorf("tools.fetch.max_redirects: must be between 0 and 20")
	}
	if c.Tools.Fetch.DefaultLimit < 1 || c.Tools.Fetch.MaxLimit < 1 || c.Tools.Fetch.DefaultLimit > c.Tools.Fetch.MaxLimit {
		return fmt.Errorf("tools.fetch: byte limits must be positive and default_limit no greater than max_limit")
	}
	if c.Tools.WebSearch.PerEngineTimeoutS < 1 || c.Tools.WebSearch.PerEngineTimeoutS > 60 {
		return fmt.Errorf("tools.web_search.per_engine_timeout_s: must be between 1 and 60")
	}
	if c.Tools.WebSearch.BenchDurationMinutes < 1 || c.Tools.WebSearch.BenchDurationMinutes > 10080 {
		return fmt.Errorf("tools.web_search.bench_duration_minutes: must be between 1 and 10080")
	}
	knownSearchEngines := map[string]bool{"duckduckgo_html": true, "duckduckgo_lite": true, "bing": true, "brave": true, "startpage": true, "mojeek": true, "wikipedia": true, "github": true, "hacker_news": true, "arxiv": true, "stackexchange": true, "pkg_go_dev": true, "npm": true}
	seenSearchEngines := map[string]bool{}
	for _, engine := range c.Tools.WebSearch.Engines {
		if !knownSearchEngines[engine] {
			return fmt.Errorf("tools.web_search.engines: unknown engine %q", engine)
		}
		if seenSearchEngines[engine] {
			return fmt.Errorf("tools.web_search.engines: duplicate engine %q", engine)
		}
		seenSearchEngines[engine] = true
	}
	for _, host := range append(append(append([]string{}, c.Tools.Fetch.AllowDomains...), c.Tools.Fetch.DenyDomains...), c.Tools.Fetch.AllowInternalHosts...) {
		if !validFetchHost(host) {
			return fmt.Errorf("tools.fetch: domain-list entries must be hostnames or IP addresses without schemes, ports, paths, or wildcards")
		}
	}
	for _, root := range c.Tools.FindFiles.SkipRoots {
		if strings.TrimSpace(root) == "" {
			return fmt.Errorf("tools.find_files.skip_roots: entries cannot be empty")
		}
	}
	for _, command := range c.Tools.Shell.OperatorCommands {
		if strings.TrimSpace(command) == "" {
			return fmt.Errorf("tools.shell.operator_commands: entries cannot be empty")
		}
	}
	if len(c.Shell.Command) == 0 {
		return fmt.Errorf("shell.command: required")
	}
	if c.Shell.TimeoutS < 1 || c.Shell.MaxTimeoutS < c.Shell.TimeoutS {
		return fmt.Errorf("shell.timeout_s: must be positive and no greater than max_timeout_s")
	}
	if c.Shell.MaxOutputLinesHead < 0 || c.Shell.MaxOutputLinesTail < 0 {
		return fmt.Errorf("shell: output line limits cannot be negative")
	}
	if c.Shell.OperatorContextIdleTimeoutMinutes < 1 || c.Shell.OperatorContextIdleTimeoutMinutes > 1440 {
		return fmt.Errorf("shell.operator_context_idle_timeout_minutes: must be between 1 and 1440")
	}
	if strings.TrimSpace(c.Shell.ServiceAccount.Account) == "" {
		return fmt.Errorf("shell.service_account.account: required")
	}
	if strings.TrimSpace(c.Shell.ServiceAccount.Domain) == "" {
		return fmt.Errorf("shell.service_account.domain: required")
	}
	if !oneOf(c.Deliver.Mode, DeliverModeChips, DeliverModeFolder, DeliverModeBoth) {
		return fmt.Errorf("deliver.mode: must be chips, folder, or both")
	}
	if c.OperatorFiles.LogRetentionDays < 1 || c.OperatorFiles.LogRetentionDays > 3650 {
		return fmt.Errorf("operator_files.log_retention_days: must be between 1 and 3650")
	}
	if _, err := credential.NewNamed(".", c.Notifications.DiscordCredential); err != nil {
		return fmt.Errorf("notifications.discord_credential: %w", err)
	}
	if _, err := c.ResolvedExchangeFolder(); err != nil {
		return err
	}
	if c.Signing.TimestampURL != "" && !strings.HasPrefix(strings.ToLower(c.Signing.TimestampURL), "http://") && !strings.HasPrefix(strings.ToLower(c.Signing.TimestampURL), "https://") {
		return fmt.Errorf("signing.timestamp_url: must be an HTTP(S) URL")
	}
	return nil
}

// ConnectionSetupReason explains incomplete first-run connection settings without
// making the configuration file itself invalid.
func ConnectionSetupReason(connection *Connection) string {
	if strings.TrimSpace(connection.BaseURL) == "" {
		return "base_url is empty — Settings → Connections → this connection → base_url, or Open setup guide"
	}
	if strings.TrimSpace(connection.Model) == "" {
		return "model is empty — Settings → Connections → this connection → model, or Open setup guide"
	}
	return ""
}

func applyDefaults(c *Config) {
	d := Defaults(c.Workspace)
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
	if c.Approval.Mode == "" {
		c.Approval = d.Approval
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
	if len(c.Connections) == 0 {
		c.Agents = []Agent{}
	}
	if !c.Chat.initialized {
		c.Chat = d.Chat
	}
	if !c.Updates.initialized {
		c.Updates = d.Updates
	}
	if !c.Deliver.initialized {
		c.Deliver = d.Deliver
	}
	if c.OperatorFiles.LogRetentionDays == 0 {
		c.OperatorFiles.LogRetentionDays = d.OperatorFiles.LogRetentionDays
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
	if c.Shell.FileRoutingGuard == nil {
		c.Shell.FileRoutingGuard = boolPointer(true)
	}
	if c.Shell.OperatorContextIdleTimeoutMinutes == 0 {
		c.Shell.OperatorContextIdleTimeoutMinutes = d.Shell.OperatorContextIdleTimeoutMinutes
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

func (s Shell) FileRoutingGuardEnabled() bool {
	return s.FileRoutingGuard == nil || *s.FileRoutingGuard
}
func boolPointer(value bool) *bool { return &value }
func validFetchHost(value string) bool {
	value = strings.TrimSpace(value)
	if net.ParseIP(value) != nil {
		return true
	}
	return value != "" && !strings.ContainsAny(value, `/\\:*?`) && !strings.Contains(value, "..")
}

func validateServiceAuth(value string) error {
	value = strings.TrimSpace(value)
	if value == "none" {
		return nil
	}
	if strings.HasPrefix(value, "static_bearer:") {
		name := strings.TrimSpace(strings.TrimPrefix(value, "static_bearer:"))
		if name == "" || !serviceEnvironmentName.MatchString(name) {
			return fmt.Errorf("static_bearer requires an environment variable name")
		}
		return nil
	}
	if strings.HasPrefix(value, "exec:") && strings.TrimSpace(strings.TrimPrefix(value, "exec:")) != "" {
		return nil
	}
	return fmt.Errorf("must be none, static_bearer:<env>, or exec:<argv>")
}
func oneOf(v string, values ...string) bool {
	for _, x := range values {
		if v == x {
			return true
		}
	}
	return false
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
	out.Connections = append([]Connection(nil), c.Connections...)
	for i := range out.Connections {
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
	for i := range c.Agents {
		if AgentID(c.Agents[i].Name) == id {
			agent := c.Agents[i]
			agent.Toolset = append([]string(nil), agent.Toolset...)
			return &agent, true
		}
	}
	return nil, false
}

func (c Config) DefaultAgentID() string {
	if len(c.Agents) == 0 {
		return ""
	}
	return AgentID(c.Agents[0].Name)
}
