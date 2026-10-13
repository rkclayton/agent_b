package config

import (
	"encoding/json"
	"regexp"
	"strings"
)

type Config struct {
	ConfigVersion int                     `json:"config_version"`
	Listen        string                  `json:"listen"`
	Workspace     string                  `json:"workspace"`
	LogDir        string                  `json:"log_dir"`
	Connections   []Connection            `json:"connections"`
	Profiles      ProfileCatalog          `json:"profiles"`
	Services      map[string]Service      `json:"services"`
	Agents        []Agent                 `json:"agents"`
	Chat          Chat                    `json:"chat"`
	Run           RunConfig               `json:"run"`
	Cron          Cron                    `json:"cron"`
	Approval      Approval                `json:"approval"`
	Context       GlobalContext           `json:"context"`
	Memory        Memory                  `json:"memory"`
	Tools         Tools                   `json:"tools"`
	Shell         Shell                   `json:"shell"`
	Sandbox       Sandbox                 `json:"sandbox"`
	Deliver       Deliver                 `json:"deliver"`
	OperatorFiles OperatorFiles           `json:"operator_files"`
	Notifications Notifications           `json:"notifications"`
	Updates       Updates                 `json:"updates"`
	Telemetry     Telemetry               `json:"telemetry"`
	Reflection    Reflection              `json:"reflection"`
	Skills        map[string]SkillSetting `json:"skills"`
	// ONE KEY. Empty is off and nothing dials; a URL is the broker this
	// AgentB reaches its phone through. Everything else about the broker — the
	// identity, the pairing, the device — is state, not configuration.
	Broker      Broker   `json:"broker,omitempty"`
	Signing     Signing  `json:"signing"`
	LoadNotices []string `json:"-"`
}

type SkillSetting struct {
	Name     string `json:"-"`
	Enabled  bool   `json:"enabled"`
	Source   string `json:"source,omitempty"`
	LastRead string `json:"last_read,omitempty"`
}

// ProfileCatalog selects the operator-owned namespace. Connections and host
// settings remain shared; each named entry owns chats, agents and memory.
type ProfileCatalog struct {
	Active string   `json:"active"`
	Names  []string `json:"names"`
}

type Agent struct {
	Name     string                  `json:"name"`
	Thinking bool                    `json:"thinking"`
	B        string                  `json:"b,omitempty"` // legacy primary binding; migrated to Connection.AgentProfile
	C        string                  `json:"c,omitempty"`
	D        string                  `json:"d,omitempty"`
	Toolset  []string                `json:"toolset"`
	Model    string                  `json:"model,omitempty"` // legacy model override; migrated into the connection
	Prompt   string                  `json:"prompt,omitempty"`
	Private  bool                    `json:"private,omitempty"`
	Skills   map[string]SkillSetting `json:"skills,omitempty"`
}

func (a *Agent) UnmarshalJSON(data []byte) error {
	type plain Agent
	var value struct {
		plain
		PromptAddendum string `json:"prompt_addendum"`
	}
	value.plain.Thinking = true
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*a = Agent(value.plain)
	if a.Prompt == "" {
		a.Prompt = value.PromptAddendum
	}
	return nil
}

var agentIDCleaner = regexp.MustCompile(`[^a-z0-9]+`)

func AgentID(name string) string {
	if strings.EqualFold(strings.TrimSpace(name), "agent_b") {
		return "agent_b"
	}
	id := agentIDCleaner.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	return strings.Trim(id, "-")
}

// delegate follows the twelve established tools; every older tool
// keeps its relative position.
func FullToolset() []string {
	return []string{"read_file", "list_dir", "write_file", "edit_file", "search", "shell", "remember", "recall", "fetch_url", "web_search", "run_script", "call_service", "delegate", "cronjob"}
}

// MergedSearchNames are the two tools `search` replaced. A configuration
// written before names them, and an agent whose toolset named them meant
// "this agent may search" - so they are read as `search` rather than rejected
// as unknown, which would have turned searching off without saying so.
var MergedSearchNames = map[string]bool{"search_text": true, "find_files": true}

// migrateToolset rewrites a pre-toolset in place, without changing what
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

// Chat keeps the reader's profile-scoped transcript presentation.
type Chat struct {
	// TextSize accepts old named steps while loading; current saves use whole
	// pixels. any keeps profile overlays written by older releases readable.
	TextSize any `json:"text_size,omitempty"`
	// Typeface is either a shipped family or an installed Windows family. A
	// stored family may later disappear; the UI marks it missing and renders the
	// shipped default until the reader chooses another.
	Typeface  string `json:"typeface,omitempty"`
	TextColor string `json:"text_color,omitempty"`
	// HiddenSurfaces retains the profile-scoped Plan-entry preference introduced
	// before Plan moved into Settings. It lives here rather than under shell
	// because it is a reader preference, not machine state.
	//
	// A hidden surface is hidden, NOT GONE: nothing about it is discarded and
	// re-enabling puts its Settings entry back.
	// Only the names in HideableSurfaces are accepted, because an unknown name
	// would be a surface nothing could bring back.
	HiddenSurfaces []string `json:"hidden_surfaces,omitempty"`
}

// HideableSurfaces are legacy static surfaces whose Settings entry may be hidden.
var HideableSurfaces = []string{"plan"}

var ChatTextColors = []string{"default", "white", "red", "orange", "yellow", "green", "cyan", "blue", "purple", "pink"}

func ChatTextPixels(value any) (int, bool) {
	switch typed := value.(type) {
	case nil:
		return 12, true
	case int:
		return typed, typed >= 9 && typed <= 32
	case float64:
		pixels := int(typed)
		return pixels, typed == float64(pixels) && pixels >= 9 && pixels <= 32
	case string:
		if typed == "" {
			return 12, true
		}
		pixels := map[string]int{"small": 11, "normal": 12, "large": 14, "larger": 16, "largest": 18}[typed]
		return pixels, pixels != 0
	default:
		return 0, false
	}
}

func allowed(value string, values []string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// Updates controls the one passive release check. AutoCheck defaults on even
// for older configuration files that predate this object; an explicit false is
// preserved by the custom unmarshaller.
// reflection's floor, visible and adjustable.
//
// A run below BOTH counts does not trigger reflection. The numbers come from
// the measurement of this machine's own journals -- seven of eleven runs
// made two model calls or fewer, and reflection doubled the cost of a one-call
// run -- and they are configuration rather than constants precisely because a
// number derived from one machine's distribution should not be something the
// operator cannot see or move.
//
// Zero on either count disables that half of the floor. Zero on both means
// reflection runs on everything, which is the behaviour before this item.
type ReflectionFloor struct {
	ModelCalls  int `json:"model_calls"`
	ToolCalls   int `json:"tool_calls"`
	initialized bool
}

// defaultReflectionFloor is 2iy's decision sheet: three model calls or one tool
// call, whichever is reached first.
func defaultReflectionFloor() ReflectionFloor {
	return ReflectionFloor{ModelCalls: 3, ToolCalls: 1, initialized: true}
}

func (f *ReflectionFloor) UnmarshalJSON(data []byte) error {
	type plain ReflectionFloor
	value := plain(defaultReflectionFloor())
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*f = ReflectionFloor(value)
	f.initialized = true
	return nil
}

// diagnostic telemetry. Three keys and no more.
//
// An empty Endpoint selects telemetry.DefaultEndpoint at runtime. Keeping the
// stored value empty preserves the distinction between the product default and
// an operator override without adding another configuration key.
type Telemetry struct {
	Enabled     bool   `json:"enabled"`
	Endpoint    string `json:"endpoint"`
	InstallID   string `json:"install_id"`
	initialized bool
}

func defaultTelemetry() Telemetry { return Telemetry{Enabled: true, initialized: true} }

func (t *Telemetry) UnmarshalJSON(data []byte) error {
	type plain Telemetry
	value := plain(defaultTelemetry())
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*t = Telemetry(value)
	t.initialized = true
	return nil
}

// Reflection is what reflection reads from configuration. adds the
// floor; nothing else about reflection is configurable and this does not change
// that.
type Reflection struct {
	Floor       ReflectionFloor `json:"floor"`
	initialized bool
}

func defaultReflection() Reflection {
	return Reflection{Floor: defaultReflectionFloor(), initialized: true}
}

func (r *Reflection) UnmarshalJSON(data []byte) error {
	type plain Reflection
	value := plain(defaultReflection())
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*r = Reflection(value)
	r.initialized = true
	return nil
}

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

type Connection struct {
	ID                   string            `json:"id"`
	Label                string            `json:"label"`
	BaseURL              string            `json:"base_url"`
	ExtractURL           string            `json:"extract_url"`
	AttachmentHandling   string            `json:"-"`
	ReadsImages          bool              `json:"-"`
	Model                string            `json:"model"`
	AgentProfile         string            `json:"agent_profile,omitempty"`
	Credential           string            `json:"credential"`
	APIKey               string            `json:"api_key,omitempty"`
	RequestTimeoutS      int               `json:"request_timeout_s"`
	ProbeMode            string            `json:"probe_mode"`
	Sampling             SamplingPair      `json:"-"`
	Reasoning            Reasoning         `json:"-"`
	Context              Context           `json:"-"`
	SystemPromptOverride string            `json:"-"`
	Capabilities         Capabilities      `json:"-"`
	Measurement          *Measurement      `json:"-"`
	Models               []ConnectionModel `json:"-"`
	// MaxConcurrent is how many runs this server serves at once;
	// zero means one. The global run.max_concurrent still caps the total.
	MaxConcurrent int `json:"max_concurrent,omitempty"`
	initialized   bool
	exposeActive  bool
}

// ConnectionModel holds every value whose truth depends on which model answers.
// The fields duplicated on Connection are the resolved active-model view used by
// the run loop; only this nested form is persisted.
type ConnectionModel struct {
	Model                string       `json:"model"`
	AttachmentHandling   string       `json:"attachment_handling"`
	ReadsImages          bool         `json:"reads_images"`
	Sampling             SamplingPair `json:"sampling"`
	Reasoning            Reasoning    `json:"reasoning"`
	Context              Context      `json:"context"`
	SystemPromptOverride string       `json:"system_prompt_override"`
	Capabilities         Capabilities `json:"capabilities"`
	Measurement          *Measurement `json:"measurement,omitempty"`
}

func (m *ConnectionModel) UnmarshalJSON(data []byte) error {
	type plain ConnectionModel
	value := plain(defaultConnectionModel(""))
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*m = ConnectionModel(value)
	return nil
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
	CompletionMS  int64   `json:"completion_ms,omitempty"`
	Capped        bool    `json:"capped"`
	Stopped       bool    `json:"stopped,omitempty"`
	// the two arms the harness ran, the decision it
	// wrote from them, and the window the model will actually run with. A
	// measurement that only reported a pass count could not explain what it
	// changed, and the operator would be left reading a switch that moved by
	// itself.
	//
	// All of it is omitempty: every measurement recorded before this release is a
	// single arm with no decision, and it must keep rendering as what it was
	// rather than as a two-arm run with empty halves.
	ReasoningOn  *MeasurementArm    `json:"reasoning_on,omitempty"`
	ReasoningOff *MeasurementArm    `json:"reasoning_off,omitempty"`
	Decision     *ReasoningDecision `json:"decision,omitempty"`
	NCtx         int                `json:"n_ctx,omitempty"`
	WindowTokens int                `json:"window_tokens,omitempty"`
}

type Service struct {
	Kind                string   `json:"kind,omitempty"`
	BaseURL             string   `json:"base_url"`
	Auth                string   `json:"auth"`
	AllowedMethods      []string `json:"allowed_methods"`
	TimeoutS            int      `json:"timeout_s"`
	MaxBodyKB           int      `json:"max_body_kb"`
	RequireConfirmation bool     `json:"require_confirmation"`
	// the one key a connector gains when it has imported an OpenAPI
	// document. Its presence is what makes the document the allow-list.
	OpenAPI *ServiceOpenAPI `json:"openapi,omitempty"`
}

// ServiceOpenAPI records the document a connector imported: where it came from, the
// snapshot kept under the user data root, that snapshot's SHA-256, and which
// operations the operator enabled on the approval card.
type ServiceOpenAPI struct {
	Source     string   `json:"source"`
	Snapshot   string   `json:"snapshot"`
	SHA256     string   `json:"sha256"`
	Operations []string `json:"operations"`
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

func defaultConnectionModel(model string) ConnectionModel {
	value := defaultConnection()
	return ConnectionModel{Model: model, AttachmentHandling: value.AttachmentHandling, ReadsImages: value.ReadsImages, Sampling: value.Sampling, Reasoning: value.Reasoning, Context: value.Context, Capabilities: value.Capabilities}
}

// NewConnectionModel returns the shipped settings for a model, enriched only
// with context metadata the server published.
func NewConnectionModel(model string, publishedContext int) ConnectionModel {
	value := defaultConnectionModel(model)
	if publishedContext > 0 {
		value.Context.NCtx = publishedContext
		value.Context.ReserveOutput = ReserveOutputFor(publishedContext)
		value.Capabilities.NCtx = publishedContext
	}
	return value
}

func (p Connection) NativeImageInput() bool {
	return p.ReadsImages && p.AttachmentHandling != "extract"
}

func (p Connection) NativeDocumentInput() bool {
	return p.AttachmentHandling == "native" || (p.AttachmentHandling == "auto" && p.Capabilities.DocumentInput)
}

func (p Connection) IsQwen38() bool {
	return strings.Contains(strings.ToLower(p.Model), "qwen3.8")
}

func (p *Connection) UnmarshalJSON(data []byte) error {
	type plain Connection
	value := plain(defaultConnection())
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var stored map[string]json.RawMessage
	if err := json.Unmarshal(data, &stored); err != nil {
		return err
	}
	var nested struct {
		Models []ConnectionModel `json:"models"`
	}
	if err := json.Unmarshal(data, &nested); err != nil {
		return err
	}
	legacy := defaultConnectionModel(value.Model)
	for _, candidate := range nested.Models {
		if candidate.Model == value.Model {
			legacy = candidate
			break
		}
	}
	type plainModel ConnectionModel
	legacyOverlay := plainModel(legacy)
	if err := json.Unmarshal(data, &legacyOverlay); err != nil {
		return err
	}
	legacy = ConnectionModel(legacyOverlay)
	if _, present := stored["reads_images"]; !present && len(nested.Models) == 0 {
		legacy.ReadsImages = legacy.Capabilities.ProbedAt == "" || legacy.Capabilities.Vision == VisionReadsImages
	}
	value.Models = nested.Models
	*p = Connection(value)
	legacyPresent := false
	for _, key := range []string{"attachment_handling", "reads_images", "sampling", "reasoning", "context", "system_prompt_override", "capabilities", "measurement"} {
		if _, present := stored[key]; present {
			legacyPresent = true
			break
		}
	}
	if len(p.Models) == 0 && ModelChosen(p.Model) {
		legacy.Model = p.Model
		p.Models = []ConnectionModel{legacy}
	} else if legacyPresent && ModelChosen(p.Model) {
		legacy.Model = p.Model
		found := false
		for index := range p.Models {
			if p.Models[index].Model == p.Model {
				p.Models[index], found = legacy, true
				break
			}
		}
		if !found {
			p.Models = append(p.Models, legacy)
		}
	}
	p.loadActiveModel(legacy)
	p.initialized = true
	return nil
}

func (p Connection) activeModel() ConnectionModel {
	for _, model := range p.Models {
		if model.Model == p.Model {
			return model
		}
	}
	return defaultConnectionModel(p.Model)
}

func (p *Connection) loadActiveModel(fallback ConnectionModel) {
	model := fallback
	for _, candidate := range p.Models {
		if candidate.Model == p.Model {
			model = candidate
			break
		}
	}
	p.AttachmentHandling, p.ReadsImages = model.AttachmentHandling, model.ReadsImages
	p.Sampling, p.Reasoning, p.Context = model.Sampling, model.Reasoning, model.Context
	p.SystemPromptOverride, p.Capabilities, p.Measurement = model.SystemPromptOverride, model.Capabilities, model.Measurement
}

// StoreActiveModel commits the resolved view before a connection is copied or
// saved. It updates one bounded entry and never walks any other collection.
func (p *Connection) StoreActiveModel() {
	if !ModelChosen(p.Model) {
		return
	}
	model := ConnectionModel{Model: p.Model, AttachmentHandling: p.AttachmentHandling, ReadsImages: p.ReadsImages, Sampling: p.Sampling, Reasoning: p.Reasoning, Context: p.Context, SystemPromptOverride: p.SystemPromptOverride, Capabilities: p.Capabilities, Measurement: p.Measurement}
	for index := range p.Models {
		if p.Models[index].Model == p.Model {
			p.Models[index] = model
			return
		}
	}
	p.Models = append(p.Models, model)
}

// SelectModel preserves the old model and resolves or creates the selected one.
func (p *Connection) SelectModel(model string, publishedContext int) {
	oldModel := p.Model
	p.StoreActiveModel()
	p.Model = model
	for _, candidate := range p.Models {
		if candidate.Model == model {
			p.loadActiveModel(candidate)
			return
		}
	}
	if !ModelChosen(oldModel) {
		for index := range p.Models {
			if p.Models[index].Model == oldModel {
				p.Models[index].Model = model
				p.loadActiveModel(p.Models[index])
				return
			}
		}
	}
	next := NewConnectionModel(model, publishedContext)
	p.Models = append(p.Models, next)
	p.loadActiveModel(next)
}

func (p Connection) MarshalJSON() ([]byte, error) {
	copy := p
	copy.StoreActiveModel()
	type plain Connection
	data, err := json.Marshal(struct {
		plain
		Models []ConnectionModel `json:"models"`
	}{plain(copy), copy.Models})
	if err != nil || !p.exposeActive {
		return data, err
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	active, err := json.Marshal(copy.activeModel())
	if err != nil {
		return nil, err
	}
	var fields map[string]any
	if err := json.Unmarshal(active, &fields); err != nil {
		return nil, err
	}
	for key, value := range fields {
		if key != "model" {
			document[key] = value
		}
	}
	return json.Marshal(document)
}
