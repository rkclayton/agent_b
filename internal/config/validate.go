package config

import (
	"fmt"
	"harness/internal/credential"
	"net"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var slug = regexp.MustCompile(`^[a-z0-9-]+$`)
var storedCredentialName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

var serviceEnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (c Config) Validate() error {
	if err := c.validateBasics(); err != nil {
		return err
	}
	connections, err := c.validateConnections()
	if err != nil {
		return err
	}
	if c.Images.Connection != "" && !connections[c.Images.Connection] {
		return fmt.Errorf("images.connection: must be empty or name an existing connection")
	}
	if c.Images.Connection == "" && c.Images.Model != "" {
		return fmt.Errorf("images.model: must be empty while images.connection uses the chat's connection")
	}
	if err := c.validateAgents(connections); err != nil {
		return err
	}
	if err := c.validateServices(); err != nil {
		return err
	}
	if err := c.validateRun(); err != nil {
		return err
	}
	if err := c.validateTools(); err != nil {
		return err
	}
	return c.validateShellAndOutput()
}

func (c Config) validateBasics() error {
	if c.ConfigVersion != CurrentConfigVersion {
		return fmt.Errorf("config_version: unsupported value %d (current %d)", c.ConfigVersion, CurrentConfigVersion)
	}
	if c.Listen == "" {
		return fmt.Errorf("listen: required")
	}
	if c.Workspace == "" {
		return fmt.Errorf("workspace: required")
	}
	for i, folder := range c.Shell.TrustedFolders {
		if !filepath.IsAbs(folder.Path) {
			return fmt.Errorf("shell.trusted_folders[%d].path: absolute path required", i)
		}
		if folder.Source != "card" && folder.Source != "settings" {
			return fmt.Errorf("shell.trusted_folders[%d].source: must be card or settings", i)
		}
	}
	if _, ok := ChatTextPixels(c.Chat.TextSize); !ok {
		return fmt.Errorf("chat.text_size: must be a whole number from 9 to 32")
	}
	if c.Chat.TextColor != "" && !allowed(c.Chat.TextColor, ChatTextColors) {
		return fmt.Errorf("chat.text_color: %q is not offered", c.Chat.TextColor)
	}
	// an unknown hidden surface would be a surface nothing can bring
	// back, so the name is refused rather than stored and forgotten.
	for _, name := range c.Chat.HiddenSurfaces {
		if !slices.Contains(HideableSurfaces, name) {
			return fmt.Errorf("chat.hidden_surfaces: %q is not a surface that can be hidden", name)
		}
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
	return nil
}

func (c Config) validateConnections() (map[string]bool, error) {
	seen := map[string]bool{}
	for i, p := range c.Connections {
		prefix := fmt.Sprintf("connections[%d]", i)
		if !slug.MatchString(p.ID) {
			return nil, fmt.Errorf("%s.id: must be a slug", prefix)
		}
		if seen[p.ID] {
			return nil, fmt.Errorf("%s.id: duplicate", prefix)
		}
		seen[p.ID] = true
		if p.MaxConcurrent < 0 {
			return nil, fmt.Errorf("%s.max_concurrent: cannot be negative", prefix)
		}
		if p.Credential != "" {
			if _, err := credential.NewNamed(".", p.Credential); err != nil {
				return nil, fmt.Errorf("%s.credential: %w", prefix, err)
			}
		}
		if p.ProbeMode != "full" && p.ProbeMode != "minimal" && p.ProbeMode != "off" {
			return nil, fmt.Errorf("%s.probe_mode: invalid", prefix)
		}
		if !oneOf(p.AttachmentHandling, "auto", "native", "extract") {
			return nil, fmt.Errorf("%s.attachment_handling: invalid", prefix)
		}
		if p.RequestTimeoutS < 1 {
			return nil, fmt.Errorf("%s.request_timeout_s: must be positive", prefix)
		}
		if p.ExtractURL != "" {
			endpoint, err := url.Parse(strings.TrimSpace(p.ExtractURL))
			if err != nil || endpoint == nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Fragment != "" {
				return nil, fmt.Errorf("%s.extract_url: must be an absolute HTTP(S) URL without user information or fragment", prefix)
			}
		}
		if !oneOf(p.Reasoning.Control, "auto", "chat_template_kwargs", "top_level", "server_flag", "none") {
			return nil, fmt.Errorf("%s.reasoning.control: invalid", prefix)
		}
		if len(p.Reasoning.ValidEfforts) > 0 && !contains(p.Reasoning.ValidEfforts, p.Reasoning.Effort) {
			return nil, fmt.Errorf("%s.reasoning.effort: not in valid_efforts", prefix)
		}
		if p.Reasoning.MaxTokens < 0 {
			return nil, fmt.Errorf("%s.reasoning.max_tokens: cannot be negative", prefix)
		}
		if p.Context.NCtx < 0 {
			return nil, fmt.Errorf("%s.context.n_ctx: cannot be negative", prefix)
		}
		if p.Context.ReserveOutput < 0 {
			return nil, fmt.Errorf("%s.context.reserve_output: cannot be negative", prefix)
		}
		// the one hard limit here. Output is carved out of the
		// context window, so a reserve larger than half of it starves the prompt it
		// is taken from. The bound never falls below the reserve every version has
		// shipped as its default, because a rule that refuses a configuration the
		// product itself created is a broken rule, not a safeguard. Every other
		// value in this area is the user's to set.
		if bound := reserveOutputBound(p.Context.NCtx); bound > 0 && p.Context.ReserveOutput > bound {
			return nil, fmt.Errorf("%s.context.reserve_output: %d leaves too little of the %d-token context window for the prompt; it may not exceed %d", prefix, p.Context.ReserveOutput, p.Context.NCtx, bound)
		}
		if p.Context.AnswerCeilingSeconds < 0 {
			return nil, fmt.Errorf("%s.context.answer_ceiling_seconds: cannot be negative", prefix)
		}
	}
	return seen, nil
}

func (c Config) validateAgents(seen map[string]bool) error {
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
		if agent.B != "" && !seen[agent.B] {
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
	return nil
}

func (c Config) validateServices() error {
	for name, service := range c.Services {
		prefix := "services." + name
		if !slug.MatchString(name) {
			return fmt.Errorf("services: names must be slugs")
		}
		endpoint, err := url.Parse(strings.TrimSpace(service.BaseURL))
		if err != nil || endpoint == nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
			return fmt.Errorf("%s.base_url: must be an absolute HTTP(S) URL without user information, query, or fragment", prefix)
		}
		if service.Kind != "" && service.Kind != "http" && service.Kind != "mcp" {
			return fmt.Errorf("%s.kind: must be http or mcp", prefix)
		}
		if err := ValidateServiceAuth(service.Auth); err != nil {
			return fmt.Errorf("%s.auth: %w", prefix, err)
		}
		if service.Kind != "mcp" && len(service.AllowedMethods) == 0 {
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
	return nil
}

func (c Config) validateRun() error {
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
	if c.Cron.MaxRunMinutes < 1 {
		return fmt.Errorf("cron.max_run_minutes: must be positive")
	}
	if !oneOf(c.Approval.Mode, ApprovalModeBoundaryOnly, ApprovalModeMutating, ApprovalModeAll, ApprovalModeOff) {
		return fmt.Errorf("approval.mode: invalid")
	}
	if !(c.Context.SoftPct > 0 && c.Context.SoftPct <= 1 && c.Context.SummaryPct > 0 && c.Context.SummaryPct <= 1) {
		return fmt.Errorf("context: require soft_pct and summary_pct in (0, 1]")
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
	return nil
}

func (c Config) validateTools() error {
	if c.Tools.ReadFile.DefaultLimit < 1 {
		return fmt.Errorf("tools.read_file.default_limit: must be positive")
	}
	if c.Tools.ReadFile.MaxLimit < 1 {
		return fmt.Errorf("tools.read_file.max_limit: must be positive")
	}
	if c.Tools.Attachments.MaxBytes < 1 {
		return fmt.Errorf("tools.attachments.max_bytes: must be positive")
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
	// startpage and mojeek were retired on 2026-09-26 and dropped from the
	// default list, but they stay KNOWN here on purpose. A saved config that still
	// names one of them predates the retirement, and refusing to load it would stop
	// the agent over an engine it simply no longer has an adapter for. They are
	// accepted, they match no adapter, and nothing runs for them.
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
	return nil
}

func (c Config) validateShellAndOutput() error {
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
// ModelPlaceholder is the literal string a connection used to be created with, and
// it is NEVER a model. The operator met it as a model name his server
// refused — "it says 'model' is not a type. MODEL WAS NEVER PUT IN BY ME" — and two
// earlier items stopped new connections getting it without ever saying what it means
// when it is already there. It means no model was chosen.
const ModelPlaceholder = "model"

// ModelChosen reports whether a connection's model is a model at all. An empty value
// and the placeholder are the same thing, in one place, so nothing can accept one and
// refuse the other.
func ModelChosen(model string) bool {
	trimmed := strings.TrimSpace(model)
	return trimmed != "" && !strings.EqualFold(trimmed, ModelPlaceholder)
}

func ConnectionSetupReason(connection *Connection) string {
	if strings.TrimSpace(connection.BaseURL) == "" {
		return "base_url is empty — Settings → Connections → this connection → base_url, or Open setup guide"
	}
	if !ModelChosen(connection.Model) {
		return "model is empty — Settings → Connections → this connection → model, or Open setup guide"
	}
	return ""
}

func validFetchHost(value string) bool {
	value = strings.TrimSpace(value)
	if net.ParseIP(value) != nil {
		return true
	}
	return value != "" && !strings.ContainsAny(value, `/\\:*?`) && !strings.Contains(value, "..")
}

func ValidateServiceAuth(value string) error {
	value = strings.TrimSpace(value)
	if value == "none" {
		return nil
	}
	// a connector's auth is a REFERENCE to a stored credential. The value
	// itself is never in the configuration.
	if strings.HasPrefix(value, "stored:") {
		name := strings.TrimSpace(strings.TrimPrefix(value, "stored:"))
		if !storedCredentialName.MatchString(name) {
			return fmt.Errorf("stored requires a credential name of lower-case letters, digits and hyphens")
		}
		return nil
	}
	if strings.HasPrefix(value, "entra:") {
		name := strings.TrimSpace(strings.TrimPrefix(value, "entra:"))
		if !storedCredentialName.MatchString(name) {
			return fmt.Errorf("entra requires a credential name of lower-case letters, digits and hyphens")
		}
		return nil
	}
	// static_bearer is RETIRED, but a configuration that still carries one must LOAD
	// — that is what the migration reads. So it is accepted here and refused in the two
	// places that would let a new one exist: a connector proposal, and Save.
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
	return fmt.Errorf("must be none, stored:<name>, entra:<name>, or exec:<argv>")
}

// ValidateNewServiceAuth is the rule for auth that is being WRITTEN: a connector the
// model proposes, or a configuration being saved. static_bearer cannot be
// created any more, only migrated away.
func ValidateNewServiceAuth(value string) error {
	if strings.HasPrefix(strings.TrimSpace(value), "static_bearer:") {
		return fmt.Errorf("static_bearer is retired; add the credential in Settings → Security and use stored:<name>")
	}
	return ValidateServiceAuth(value)
}
func oneOf(v string, values ...string) bool {
	for _, x := range values {
		if v == x {
			return true
		}
	}
	return false
}
