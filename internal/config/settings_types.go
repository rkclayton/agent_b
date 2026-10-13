package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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
	NCtx          int `json:"n_ctx"`
	ReserveOutput int `json:"reserve_output"`
	// AnswerCeilingSeconds bounds ONE answer, never a run or a chat:
	// the operator spent 672 seconds on a single turn that was then thrown away.
	// Zero means no ceiling, which is what every existing configuration says.
	AnswerCeilingSeconds int            `json:"answer_ceiling_seconds,omitempty"`
	Sizing               *ContextSizing `json:"sizing,omitempty"`
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
	// a server byte cap cannot be probed -- neither vLLM's /v1/models
	// entry nor llama.cpp's /props publishes one -- so it is learned from the
	// refusal that names it and remembered for this connection.
	ObservedByteLimit int      `json:"observed_byte_limit,omitempty"`
	ProbedAt          string   `json:"probed_at"`
	Findings          []string `json:"findings"`
}

const (
	VisionReadsImages       = "reads images"
	VisionAcceptsUnreadable = "accepts images but does not read them"
	VisionRejected          = "rejected"
)

type RunConfig struct {
	MaxTurns            int `json:"max_turns"`
	MaxWallClockSeconds int `json:"max_wall_clock_seconds"`
	MaxToolCalls        int `json:"max_tool_calls"`
	CycleWindow         int `json:"cycle_window"`
	MaxConcurrent       int `json:"max_concurrent"`
	QueueDepth          int `json:"queue_depth"`
}

type Cron struct {
	MaxRunMinutes int `json:"max_run_minutes"`
}

const DefaultMaxTurns = 10000
const DefaultMaxWallClockSeconds = 6 * 60 * 60
const DefaultMaxToolCalls = 1000

type Approval struct {
	Mode string `json:"mode"`
	// unattended means nothing asks. It changes WHO IS ASKED,
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
	Desktop           bool   `json:"desktop"`
	DesktopNeedsYou   bool   `json:"desktop_needs_you"`
	DesktopFinished   bool   `json:"desktop_finished"`
	DesktopScheduled  bool   `json:"desktop_scheduled"`
	initialized       bool
}

func defaultNotifications() Notifications {
	return Notifications{DiscordCredential: "discord-webhook", Desktop: true, DesktopNeedsYou: true, DesktopFinished: true, DesktopScheduled: true, initialized: true}
}
func (n *Notifications) UnmarshalJSON(data []byte) error {
	type plain Notifications
	value := plain(defaultNotifications())
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*n = Notifications(value)
	n.initialized = true
	return nil
}

// Broker is the one config key. made it a key with a BUILT-IN
// default — broker.DefaultURL, written once in internal/broker — so a fresh install can
// pair a phone without anybody finding and typing an address. A url in the file still
// wins, for tests and for anyone running their own broker; the page neither shows nor
// edits it.
type Broker struct {
	URL string `json:"url"`
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
	CurrentConfigVersion = 15
	DefaultReserveOutput = 10240
	// MaxProposedReserveOutput caps what a probe proposes..
	MaxProposedReserveOutput = 32768
	ApprovalModeBoundaryOnly = "boundary-only"
	ApprovalModeMutating     = "mutating"
	ApprovalModeAll          = "all"
	ApprovalModeOff          = "off"
	DeliverModeChips         = "chips"
	DeliverModeFolder        = "folder"
	DeliverModeBoth          = "both"
)

const ApprovalDefaultMigrationNotice = "corrected inherited approval default from mutating to boundary-only; mutating can be reselected in Settings > Run & approval"
const OperatorIdleTimeoutMigrationNotice = "migrated shell.operator_context_timeout_minutes to shell.operator_context_idle_timeout_minutes; Run as you now expires after agent inactivity"
const ByteWindowMigrationNotice = "migrated read_file and fetch_url limits from line counts to UTF-8 byte windows"
const ModelRolesMigrationNotice = "migrated model connections to schema 5 with an explicit main role, optional aux role, and per-connection context size"

// ModelPlaceholderMigrationNotice is, and it names the connections it
// cleared: the operator asked five times for the placeholder to go and every fix so
// far had only stopped new connections getting it.
const ModelPlaceholderMigrationNotice = "cleared the \"model\" placeholder from connection(s): "
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
	// a PDF has two routes and the branch order used to
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
	// OperatorContext is exposed to the UI but Save always persists it as false.
	OperatorContext                   bool                `json:"operator_context"`
	OperatorContextExpiresAt          string              `json:"-"`
	OperatorContextIdleTimeoutMinutes int                 `json:"operator_context_idle_timeout_minutes"`
	ServiceAccount                    ShellServiceAccount `json:"service_account"`
	AllowLocalNetwork                 bool                `json:"allow_local_network"`
	ConfirmedLocalSubnets             []string            `json:"confirmed_local_subnets"`
	AllowedModelRanges                []string            `json:"allowed_model_ranges"`
	Deny                              []string            `json:"deny"`
	TrustedFolders                    []TrustedFolder     `json:"trusted_folders"`
}

type TrustedFolder struct {
	Path    string `json:"path"`
	AddedAt string `json:"added_at"`
	Source  string `json:"source"`
}

type ShellServiceAccount struct {
	Enabled     bool   `json:"enabled"`
	Account     string `json:"account"`
	Domain      string `json:"domain"`
	initialized bool
}

func (s *ShellServiceAccount) UnmarshalJSON(data []byte) error {
	type plain ShellServiceAccount
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*s = ShellServiceAccount(value)
	s.initialized = true
	return nil
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
