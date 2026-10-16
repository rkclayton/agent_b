package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"harness/internal/credential"
)

func TestSaveSkipsByteIdenticalConfig2l0(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harness.json")
	cfg := Defaults(t.TempDir())
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(946684800, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	unchanged, err := os.Stat(path)
	if err != nil || !unchanged.ModTime().Equal(stamp) {
		t.Fatalf("identical save changed mtime: %v err=%v", unchanged.ModTime(), err)
	}
	cfg.Listen = "127.0.0.1:8791"
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	changed, err := os.Stat(path)
	if err != nil || changed.ModTime().Equal(stamp) {
		t.Fatalf("content change did not write config: %v err=%v", changed.ModTime(), err)
	}
}

func TestLegacyVoiceKeyIsDroppedOnSave2sa(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harness.json")
	cfg := Defaults(filepath.Join(filepath.Dir(path), "workspace"))
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	document["voice"] = map[string]any{"default_session_id": "old-chat"}
	data, _ = json.Marshal(document)
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, _, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = loaded.Save(path); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(path)
	if bytes.Contains(saved, []byte(`"voice"`)) || bytes.Contains(saved, []byte("old-chat")) {
		t.Fatalf("removed voice setting survived save: %s", saved)
	}
}

func writeConfigFixture(t *testing.T, path, mode string, stamped bool) {
	t.Helper()
	cfg := Defaults(t.TempDir())
	cfg.Approval.Mode = mode
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if !stamped {
		delete(document, "config_version")
	}
	data, err = json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAttachmentHandlingDefaultsAndValidation(t *testing.T) {
	cfg := Defaults(t.TempDir())
	if cfg.Connections[0].AttachmentHandling != "auto" {
		t.Fatalf("attachment handling=%q", cfg.Connections[0].AttachmentHandling)
	}
	for _, value := range []string{"auto", "native", "extract"} {
		candidate := cfg
		candidate.Connections = append([]Connection(nil), cfg.Connections...)
		candidate.Connections[0].AttachmentHandling = value
		if err := candidate.Validate(); err != nil {
			t.Fatalf("%s: %v", value, err)
		}
	}
	cfg.Connections[0].AttachmentHandling = "surprise"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "attachment_handling") {
		t.Fatalf("invalid attachment handling: %v", err)
	}
}

func TestReadsImagesDefaultsAndMigratesFromStoredFinding2qx(t *testing.T) {
	fresh := Defaults(t.TempDir()).Connections[0]
	if fresh.ReadsImages {
		t.Fatal("a new connection must start with reads_images off")
	}
	for _, row := range []struct {
		vision string
		want   bool
	}{
		{VisionReadsImages, true},
		{VisionRejected, false},
	} {
		raw := fmt.Sprintf(`{"id":"old","attachment_handling":"auto","capabilities":{"vision":%q,"probed_at":"2026-10-06T12:00:00Z"}}`, row.vision)
		var connection Connection
		if err := json.Unmarshal([]byte(raw), &connection); err != nil {
			t.Fatal(err)
		}
		if connection.ReadsImages != row.want {
			t.Errorf("vision=%q reads_images=%t, want %t", row.vision, connection.ReadsImages, row.want)
		}
	}
}

func TestConnectionWithReadsImagesRoundTripsEveryStoredKey2qx(t *testing.T) {
	original := Defaults(t.TempDir()).Connections[0]
	original.ID, original.Label = "old", "Old"
	original.BaseURL, original.ExtractURL, original.Model = "http://127.0.0.1:8080", "http://127.0.0.1:8081", "m"
	original.ReadsImages, original.Credential, original.MaxConcurrent = true, "secret", 2
	original.Context.NCtx, original.Context.ReserveOutput = 32768, 4096
	original.SystemPromptOverride = "keep"
	original.Capabilities.Vision, original.Capabilities.ProbedAt = VisionReadsImages, "2026-10-06T12:00:00Z"
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var connection Connection
	if err := json.Unmarshal(raw, &connection); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(connection)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]any
	if err := json.Unmarshal(raw, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &after); err != nil {
		t.Fatal(err)
	}
	for key, value := range before {
		if !reflect.DeepEqual(after[key], value) {
			t.Errorf("stored key %s changed: %#v -> %#v", key, value, after[key])
		}
	}
}

func TestUnknownOrOverstatedContextDoesNotInvalidateConnection2qr(t *testing.T) {
	for _, nctx := range []int{0, 32_000_000} {
		cfg := Defaults(t.TempDir())
		cfg.Connections[0].ProbeMode = "off"
		cfg.Connections[0].Context.NCtx = nctx
		cfg.Connections[0].Capabilities.Props = true
		cfg.Connections[0].Capabilities.NCtx = 32_768
		if err := cfg.Validate(); err != nil {
			t.Errorf("n_ctx %d invalidated connection: %v", nctx, err)
		}
	}
}

func TestLoadCreatesConfigFromExample(t *testing.T) {
	dir := t.TempDir()
	examplePath := filepath.Join(dir, "harness.example.json")
	configPath := filepath.Join(dir, "harness.json")
	example := Defaults("./workspace")
	if err := example.Save(examplePath); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatal(err)
	}

	got, migrated, created, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if migrated || !created {
		t.Fatalf("migrated=%v created=%v", migrated, created)
	}
	// Item 2l1 (b): a fresh connection has NO model, and the setup reason says
	// exactly that. It used to be seeded with the literal string "model", which
	// read as a real value, passed the emptiness check, and was refused later in
	// words the operator could not act on.
	if reason := ConnectionSetupReason(&got.Connections[0]); !strings.Contains(reason, "model is empty") {
		t.Fatalf("setup reason = %q, want it to name the empty model", reason)
	}
	if got.Connections[0].Model != "" {
		t.Fatalf("fresh default model = %q, want empty", got.Connections[0].Model)
	}
	written, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(written, want) {
		t.Fatal("created config is not an exact copy of the example")
	}

	_, _, created, err = Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("existing config reported as created")
	}
}

func TestConnectionSetupReasonNamesConnectionsAndSetupGuide(t *testing.T) {
	connection := defaultConnection()
	if got, want := ConnectionSetupReason(&connection), "base_url is empty — Settings → Connections → this connection → base_url, or Open setup guide"; got != want {
		t.Fatalf("empty base_url reason = %q, want %q", got, want)
	}
	connection.BaseURL = "http://127.0.0.1:8080"
	if got, want := ConnectionSetupReason(&connection), "model is empty — Settings → Connections → this connection → model, or Open setup guide"; got != want {
		t.Fatalf("empty model reason = %q, want %q", got, want)
	}
	// Item 2nq (b): the literal "model" is not a model. It used to satisfy this check,
	// which is how a connection carrying the old placeholder counted as set up while
	// every server refused it by name.
	connection.Model = "model"
	if got := ConnectionSetupReason(&connection); got == "" {
		t.Fatalf("the placeholder is accepted as a model")
	}
	connection.Model = "a-real-model"
	if got := ConnectionSetupReason(&connection); got != "" {
		t.Fatalf("complete connection reason = %q, want empty", got)
	}
}

func TestConnectionFallbackIsOneOtherConnectionOrNone2ro(t *testing.T) {
	cfg := Defaults(t.TempDir())
	other := cfg.Connections[0]
	other.ID, other.Label = "backup", "Backup"
	cfg.Connections = append(cfg.Connections, other)
	cfg.Connections[0].Fallback = other.ID
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(cfg)
	if err != nil || !strings.Contains(string(encoded), `"fallback":"backup"`) {
		t.Fatalf("encoded=%s err=%v", encoded, err)
	}
	cfg.Connections[0].Fallback = cfg.Connections[0].ID
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), ".fallback") {
		t.Fatalf("self fallback error=%v", err)
	}
	cfg.Connections[0].Fallback = "missing"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), ".fallback") {
		t.Fatalf("missing fallback error=%v", err)
	}
	cfg.Connections[0].Fallback = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("none fallback: %v", err)
	}
}

func TestApplyDefaultsNormalizesNullConnectionsToEmptyArray(t *testing.T) {
	cfg := Config{Workspace: t.TempDir(), Connections: nil}
	ApplyDefaults(&cfg)
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"connections":[]`)) {
		t.Fatalf("empty connection catalog was not an array: %s", data)
	}
}

func TestUnevaluatedConnectionUsesProbeOffAssumptionsWithoutPretendingEvalRan2qw(t *testing.T) {
	cfg := Defaults(t.TempDir())
	cfg.Connections[0].Capabilities = Capabilities{}
	ApplyDefaults(&cfg)
	caps := cfg.Connections[0].Capabilities
	if !caps.Streaming || !caps.ToolCalls || !caps.DocumentInput || !caps.ImageInput || caps.Vision != VisionReadsImages {
		t.Fatalf("unevaluated assumptions=%+v", caps)
	}
	if caps.ProbedAt != "" || len(caps.Findings) != 0 {
		t.Fatalf("unevaluated connection claims Eval ran: %+v", caps)
	}
}

func TestSaveAndMaskKeepEmptyCatalogsAsArrays(t *testing.T) {
	cfg := Defaults(t.TempDir())
	cfg.Connections = []Connection{}
	cfg.Agents = []Agent{}
	path := filepath.Join(t.TempDir(), "harness.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"connections": []`)) {
		t.Fatalf("saved empty connection catalog was not an array: %s", data)
	}
	if !bytes.Contains(data, []byte(`"agents": []`)) {
		t.Fatalf("saved empty agent catalog was not an array: %s", data)
	}
	masked, err := json.Marshal(cfg.Masked())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(masked, []byte(`"connections":[]`)) {
		t.Fatalf("masked empty connection catalog was not an array: %s", masked)
	}
	if !bytes.Contains(masked, []byte(`"agents":[]`)) {
		t.Fatalf("masked empty agent catalog was not an array: %s", masked)
	}
}

func TestLoadRejectsExplicitZeroIndependentBackstops(t *testing.T) {
	for _, field := range []string{"max_wall_clock_seconds"} {
		t.Run(field, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "harness.json")
			template, err := os.ReadFile("../../harness.example.json")
			if err != nil {
				t.Fatal(err)
			}
			document := strings.Replace(string(template), `"`+field+`": 21600`, `"`+field+`": 0`, 1)
			if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := Load(path); err == nil || !strings.Contains(err.Error(), field) || !strings.Contains(err.Error(), "zero is not unlimited") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestLoadWithTemplateSeparatesApplicationAndDataRoots(t *testing.T) {
	applicationRoot := t.TempDir()
	dataRoot := filepath.Join(t.TempDir(), "nested", "data")
	templatePath := filepath.Join(applicationRoot, "harness.example.json")
	configPath := filepath.Join(dataRoot, "harness.json")
	example := Defaults(t.TempDir())
	if err := example.Save(templatePath); err != nil {
		t.Fatal(err)
	}
	loaded, _, created, err := LoadWithTemplate(configPath, templatePath)
	if err != nil {
		t.Fatal(err)
	}
	if !created || loaded.Listen != example.Listen {
		t.Fatalf("created=%v loaded=%+v", created, loaded)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("live config was not created in data root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(applicationRoot, "harness.json")); !os.IsNotExist(err) {
		t.Fatalf("live config was written into application root: %v", err)
	}
}

func TestApprovalModeDefaultsWhenAbsentOrEmpty(t *testing.T) {
	for _, test := range []struct {
		name   string
		absent bool
	}{
		{name: "absent", absent: true},
		{name: "empty"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "harness.json")
			data, err := json.Marshal(Defaults(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := json.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			if test.absent {
				delete(document, "approval")
			} else {
				document["approval"] = map[string]any{"mode": ""}
			}
			data, err = json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, _, _, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Approval.Mode != ApprovalModeBoundaryOnly {
				t.Fatalf("approval mode=%q, want %q", cfg.Approval.Mode, ApprovalModeBoundaryOnly)
			}
		})
	}
}

func TestOperatorFilesDefaultOffWithThirtyDayRetention(t *testing.T) {
	cfg := Defaults(t.TempDir())
	if cfg.OperatorFiles.AllowMailboxApprovals || cfg.OperatorFiles.LogRetentionDays != 30 {
		t.Fatalf("operator files defaults = %+v", cfg.OperatorFiles)
	}
	cfg.OperatorFiles.LogRetentionDays = 3651
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "operator_files.log_retention_days") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestServicesAdditiveCurrentSchemaDefaultsEmpty(t *testing.T) {
	cfg := Defaults(t.TempDir())
	if cfg.ConfigVersion != CurrentConfigVersion || cfg.Services == nil || len(cfg.Services) != 0 {
		t.Fatalf("defaults version=%d services=%#v", cfg.ConfigVersion, cfg.Services)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, "services")
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var omitted Config
	if err := json.Unmarshal(data, &omitted); err != nil {
		t.Fatal(err)
	}
	ApplyDefaults(&omitted)
	if omitted.ConfigVersion != CurrentConfigVersion || omitted.Services == nil || len(omitted.Services) != 0 {
		t.Fatalf("omitted services=%#v version=%d", omitted.Services, omitted.ConfigVersion)
	}
	if err := omitted.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateCheckDefaultsOnButPersistsOff(t *testing.T) {
	defaults := Defaults(t.TempDir())
	if !defaults.Updates.AutoCheck {
		t.Fatal("update checks should default on")
	}
	var absent Config
	if err := json.Unmarshal([]byte(`{"config_version":6}`), &absent); err != nil {
		t.Fatal(err)
	}
	applyDefaults(&absent)
	if !absent.Updates.AutoCheck {
		t.Fatal("an absent updates object should default on")
	}
	var disabled Config
	if err := json.Unmarshal([]byte(`{"updates":{"auto_check":false}}`), &disabled); err != nil {
		t.Fatal(err)
	}
	applyDefaults(&disabled)
	if disabled.Updates.AutoCheck {
		t.Fatal("an explicit false should be preserved")
	}
}

func TestServiceAllowlistValidation(t *testing.T) {
	valid := Service{BaseURL: "https://broker.example/api", Auth: "exec:entra-token --scope broker", AllowedMethods: []string{"GET", "post"}, TimeoutS: 30, MaxBodyKB: 256, RequireConfirmation: true}
	cfg := Defaults(t.TempDir())
	cfg.Services["acme-services"] = valid
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*Service)
		want   string
	}{
		{"base_url", func(service *Service) { service.BaseURL = "file:///tmp/broker" }, "base_url"},
		{"auth", func(service *Service) { service.Auth = "oauth:magic" }, "auth"},
		{"static_env", func(service *Service) { service.Auth = "static_bearer:not a name" }, "environment variable"},
		{"methods", func(service *Service) { service.AllowedMethods = nil }, "allowed_methods"},
		{"timeout", func(service *Service) { service.TimeoutS = 0 }, "timeout_s"},
		{"body_limit", func(service *Service) { service.MaxBodyKB = 0 }, "max_body_kb"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cfg
			service := valid
			test.mutate(&service)
			candidate.Services = map[string]Service{"acme-services": service}
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestMCPCommandServiceValidation2rm(t *testing.T) {
	cfg := Defaults(t.TempDir())
	cfg.Services["local"] = Service{Kind: "mcp", BaseURL: "command:fixture-server --stdio", Auth: "none", TimeoutS: 30, MaxBodyKB: 64}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	service := cfg.Services["local"]
	service.Auth = "stored:local"
	cfg.Services["local"] = service
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "must use none") {
		t.Fatalf("credentialed command error=%v", err)
	}
}

func TestHookValidation2rn(t *testing.T) {
	cfg := Defaults(t.TempDir())
	cfg.Hooks = []Hook{{Event: "before_tool", Tool: "shell", Command: "check"}, {Event: "after_tool", Tool: "*", Command: "record", TimeoutS: 60}, {Event: "run_end", Command: "finish"}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, hook := range []Hook{{Event: "other", Command: "x"}, {Event: "before_tool", Tool: "missing", Command: "x"}, {Event: "after_tool", Command: ""}, {Event: "run_end", Command: "x", TimeoutS: 61}} {
		bad := cfg
		bad.Hooks = []Hook{hook}
		if err := bad.Validate(); err == nil {
			t.Fatalf("invalid hook accepted: %+v", hook)
		}
	}
}

func TestHarnessExampleShipsBoundaryOnlyIndependentlyOfDefaults(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "harness.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		ConfigVersion int `json:"config_version"`
		Approval      struct {
			Mode string `json:"mode"`
		} `json:"approval"`
		Deliver Deliver   `json:"deliver"`
		Run     RunConfig `json:"run"`
		Shell   struct {
			OperatorContextIdleTimeoutMinutes int                 `json:"operator_context_idle_timeout_minutes"`
			ServiceAccount                    ShellServiceAccount `json:"service_account"`
		} `json:"shell"`
		Tools struct {
			Shell ShellTool `json:"shell"`
			Fetch struct {
				DenyDomains []string `json:"deny_domains"`
			} `json:"fetch"`
			FindFiles struct {
				SkipRoots []string `json:"skip_roots"`
			} `json:"find_files"`
		} `json:"tools"`
		Connections []Connection `json:"connections"`
		Agents      []Agent      `json:"agents"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if document.ConfigVersion != CurrentConfigVersion {
		t.Fatalf("template config_version=%d, want %d", document.ConfigVersion, CurrentConfigVersion)
	}
	if document.Approval.Mode != "boundary-only" {
		t.Fatalf("template approval mode=%q, want literal boundary-only", document.Approval.Mode)
	}
	if document.Deliver.Mode != DeliverModeBoth || document.Deliver.ExchangeFolder != `%USERPROFILE%\Agent_b` {
		t.Fatalf("template delivery=%+v", document.Deliver)
	}
	if document.Shell.OperatorContextIdleTimeoutMinutes != 20 {
		t.Fatalf("template operator idle timeout=%d, want literal 20", document.Shell.OperatorContextIdleTimeoutMinutes)
	}
	if document.Shell.ServiceAccount.Enabled {
		t.Fatal("fresh-install template enables the service identity")
	}
	if len(document.Tools.Shell.OperatorCommands) != 1 || document.Tools.Shell.OperatorCommands[0] != "git" {
		t.Fatalf("template operator commands=%v, want [git]", document.Tools.Shell.OperatorCommands)
	}
	if len(document.Tools.Fetch.DenyDomains) != 0 || len(document.Tools.FindFiles.SkipRoots) != 5 {
		t.Fatalf("template policy defaults: deny_domains=%v skip_roots=%v", document.Tools.Fetch.DenyDomains, document.Tools.FindFiles.SkipRoots)
	}
	if len(document.Connections) != 0 || len(document.Agents) != 0 {
		t.Fatalf("first-run template must have no configured connections or agents: connections=%d agents=%+v", len(document.Connections), document.Agents)
	}
}

func TestEmptyServerListIsValidFirstRunState(t *testing.T) {
	cfg := Defaults(t.TempDir())
	cfg.Connections = []Connection{}
	cfg.Agents = []Agent{}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Agents = []Agent{{Name: "Local", B: "missing", Toolset: FullToolset()}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "agents") {
		t.Fatalf("nonempty first-run agent error=%v", err)
	}
}

func TestDeliveryDefaultsOnlyWhenOmitted(t *testing.T) {
	cfg := Defaults(t.TempDir())
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, "deliver")
	data, _ = json.Marshal(document)
	var omitted Config
	if err := json.Unmarshal(data, &omitted); err != nil {
		t.Fatal(err)
	}
	ApplyDefaults(&omitted)
	if omitted.Deliver.Mode != DeliverModeBoth || omitted.Deliver.ExchangeFolder == "" {
		t.Fatalf("omitted delivery did not default: %+v", omitted.Deliver)
	}

	document["deliver"] = map[string]any{"mode": "", "exchange_folder": ""}
	data, _ = json.Marshal(document)
	var explicit Config
	if err := json.Unmarshal(data, &explicit); err != nil {
		t.Fatal(err)
	}
	ApplyDefaults(&explicit)
	if explicit.Deliver.Mode != "" || explicit.Deliver.ExchangeFolder != "" {
		t.Fatalf("explicit empties were defaulted: %+v", explicit.Deliver)
	}
	if err := explicit.Validate(); err == nil || !strings.Contains(err.Error(), "deliver.mode") {
		t.Fatalf("explicit empty validation=%v", err)
	}
}

func TestPolicyListsDefaultOnlyWhenOmitted(t *testing.T) {
	omitted := Defaults(t.TempDir())
	omitted.Tools.Fetch.DenyDomains = nil
	omitted.Tools.FindFiles.SkipRoots = nil
	omitted.Tools.Shell.OperatorCommands = nil
	applyDefaults(&omitted)
	if len(omitted.Tools.Fetch.DenyDomains) != 0 || len(omitted.Tools.FindFiles.SkipRoots) != 5 || len(omitted.Tools.Shell.OperatorCommands) != 1 || omitted.Tools.Shell.OperatorCommands[0] != "git" {
		t.Fatalf("omitted defaults: deny=%v skip=%v operator=%v", omitted.Tools.Fetch.DenyDomains, omitted.Tools.FindFiles.SkipRoots, omitted.Tools.Shell.OperatorCommands)
	}

	cleared := Defaults(t.TempDir())
	cleared.Tools.Fetch.DenyDomains = []string{}
	cleared.Tools.FindFiles.SkipRoots = []string{}
	cleared.Tools.Shell.OperatorCommands = []string{}
	applyDefaults(&cleared)
	if cleared.Tools.Fetch.DenyDomains == nil || len(cleared.Tools.Fetch.DenyDomains) != 0 || cleared.Tools.FindFiles.SkipRoots == nil || len(cleared.Tools.FindFiles.SkipRoots) != 0 || cleared.Tools.Shell.OperatorCommands == nil || len(cleared.Tools.Shell.OperatorCommands) != 0 {
		t.Fatalf("explicit clears were replaced: deny=%#v skip=%#v operator=%#v", cleared.Tools.Fetch.DenyDomains, cleared.Tools.FindFiles.SkipRoots, cleared.Tools.Shell.OperatorCommands)
	}
}

func TestCurrentOperatorIdleTimeoutConfigIsUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harness.json")
	cfg := Defaults(t.TempDir())
	cfg.Shell.OperatorContextIdleTimeoutMinutes = 11
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, migrated, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if migrated || loaded.Shell.OperatorContextIdleTimeoutMinutes != 11 || !bytes.Equal(before, after) {
		t.Fatalf("migrated=%t idle=%d changed=%t", migrated, loaded.Shell.OperatorContextIdleTimeoutMinutes, !bytes.Equal(before, after))
	}
}

func TestSaveStampsConfigVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harness.json")
	cfg := Defaults(t.TempDir())
	cfg.ConfigVersion = 0
	cfg.Approval.Mode = ApprovalModeMutating
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, _, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ConfigVersion != CurrentConfigVersion || loaded.Approval.Mode != ApprovalModeMutating {
		t.Fatalf("round trip version=%d mode=%q", loaded.ConfigVersion, loaded.Approval.Mode)
	}
}

func TestReserveOutputUsesCanonicalDefault(t *testing.T) {
	cfg := Config{Connections: []Connection{{}}, Shell: Shell{Command: []string{"unused"}}}
	ApplyDefaults(&cfg)
	if got := cfg.Connections[0].Context.ReserveOutput; got != DefaultReserveOutput {
		t.Fatalf("reserve_output=%d, want canonical default %d", got, DefaultReserveOutput)
	}
}

func TestServiceAccountSplitDefaultsOffWithLocalAccountDefaults2qo(t *testing.T) {
	cfg := Config{Shell: Shell{Command: []string{"unused"}}}
	ApplyDefaults(&cfg)
	if cfg.Shell.ServiceAccount.Enabled {
		t.Fatal("service-account split defaulted on")
	}
	if cfg.Shell.OperatorContext {
		t.Fatal("operator context defaulted on")
	}
	if cfg.Shell.OperatorContextIdleTimeoutMinutes != 20 {
		t.Fatalf("operator context idle timeout=%d, want 20", cfg.Shell.OperatorContextIdleTimeoutMinutes)
	}
	if cfg.Shell.ServiceAccount.Account != "agentb-svc" || cfg.Shell.ServiceAccount.Domain != "." {
		t.Fatalf("service-account defaults = %+v", cfg.Shell.ServiceAccount)
	}
}

func TestFetchDefaultsAndAllowListValidation(t *testing.T) {
	cfg := Config{Tools: Tools{ReadFile: ReadFileTool{DefaultLimit: 1}}, Shell: Shell{Command: []string{"unused"}}}
	ApplyDefaults(&cfg)
	if cfg.Tools.Fetch.TimeoutS != 20 || cfg.Tools.Fetch.MaxBytes != 2<<20 || cfg.Tools.Fetch.MaxRedirects != 5 {
		t.Fatalf("fetch defaults = %+v", cfg.Tools.Fetch)
	}
	if len(cfg.Tools.Fetch.AllowDomains) != 0 || len(cfg.Tools.Fetch.AllowInternalHosts) != 0 {
		t.Fatalf("fetch allow lists should default empty: %+v", cfg.Tools.Fetch)
	}
	full := Defaults(t.TempDir())
	full.Tools.Fetch.AllowDomains = []string{"https://example.com"}
	if err := full.Validate(); err == nil {
		t.Fatal("fetch allow-list entry with scheme was accepted")
	}
	full = Defaults(t.TempDir())
	full.Tools.Fetch.AllowInternalHosts = []string{"::1"}
	if err := full.Validate(); err != nil {
		t.Fatalf("IPv6 internal host refused: %v", err)
	}
}

func TestOperatorContextNeverPersistsOrRestartsEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harness.json")
	cfg := Defaults(t.TempDir())
	cfg.Shell.OperatorContext = true
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"operator_context": true`)) {
		t.Fatal("operator context was persisted on")
	}
	loaded, _, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Shell.OperatorContext {
		t.Fatal("operator context restarted on")
	}
}

func TestAgentConnectionsUseLetteredSlots(t *testing.T) {
	cfg := Defaults(t.TempDir())
	c := cfg.Connections[0]
	c.ID, c.Label = "small", "Small"
	cfg.Connections = append(cfg.Connections, c)
	cfg.Agents[0].C = "small"
	agent, ok := cfg.ProfileForConnection("local")
	if !ok || AgentID(agent.Name) != "agent_b" || agent.B != "" || agent.C != "small" || agent.D != "" {
		t.Fatalf("lettered agent=%+v ok=%t", agent, ok)
	}
}

func TestAgentNameReservesAgentA(t *testing.T) {
	cfg := Defaults(t.TempDir())
	for _, name := range []string{"agent_a", "Agent_A", "AGENT_A"} {
		cfg.Agents[0].Name = name
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "agent_a is reserved") {
			t.Fatalf("name=%q error=%v", name, err)
		}
	}
}

func TestConnectionCredentialNameIsValidated(t *testing.T) {
	cfg := Defaults(t.TempDir())
	cfg.Connections[0].Credential = "../outside"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "connections[0].credential") {
		t.Fatalf("invalid credential error=%v", err)
	}
}

func TestAttachmentConfigIsAdditiveCurrentSchema(t *testing.T) {
	cfg := Defaults(t.TempDir())
	if cfg.ConfigVersion != CurrentConfigVersion || cfg.Tools.Attachments.MaxBytes != 256<<20 {
		t.Fatalf("defaults: version=%d attachments=%+v", cfg.ConfigVersion, cfg.Tools.Attachments)
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "harness.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	tools := document["tools"].(map[string]any)
	delete(tools, "attachments")
	without, _ := json.Marshal(document)
	var loaded Config
	if err := json.Unmarshal(without, &loaded); err != nil {
		t.Fatal(err)
	}
	ApplyDefaults(&loaded)
	if loaded.ConfigVersion != CurrentConfigVersion || loaded.Tools.Attachments.MaxBytes != 256<<20 {
		t.Fatalf("omitted attachment defaults=%+v version=%d", loaded.Tools.Attachments, loaded.ConfigVersion)
	}
	loaded.Tools.Attachments.MaxBytes = 0
	if err := loaded.Validate(); err == nil || !strings.Contains(err.Error(), "tools.attachments.max_bytes") {
		t.Fatalf("zero limit validation=%v", err)
	}
	loaded.Tools.Attachments.MaxBytes = 512 << 20
	if err := loaded.Validate(); err != nil {
		t.Fatalf("512 MiB: %v", err)
	}
}

// Items 2s3, 2rj, and 2sj: each new tool follows every established configurable tool.
func TestFullToolsetContractKeepsExistingOrderThenProcess(t *testing.T) {
	want := "read_file,list_dir,write_file,edit_file,search,shell,remember,recall,fetch_url,web_search,run_script,call_service,delegate,cronjob,image,todo,ask,process,code"
	got := FullToolset()
	if len(got) != 19 || strings.Join(got, ",") != want {
		t.Fatalf("full toolset=%v", got)
	}
}

func TestApplyDefaultsAddsDelegateOnlyToThePreviousFullToolset(t *testing.T) {
	legacyFull := FullToolset()[:13]
	cfg := Defaults(t.TempDir())
	cfg.Agents = []Agent{{Name: "Full", B: cfg.Connections[0].ID, Toolset: append([]string(nil), legacyFull...)}, {Name: "Limited", B: cfg.Connections[0].ID, Toolset: []string{"read_file"}}}
	ApplyDefaults(&cfg)
	if got := strings.Join(cfg.Agents[0].Toolset, ","); got != strings.Join(FullToolset(), ",") {
		t.Fatalf("full toolset=%s", got)
	}
	if got := strings.Join(cfg.Agents[1].Toolset, ","); got != "read_file" {
		t.Fatalf("limited toolset widened=%s", got)
	}
}

func TestPreMergeToolsetIsReadAsSearch(t *testing.T) {
	for _, item := range []struct {
		toolset []string
		want    string
	}{
		{[]string{"read_file", "search_text", "find_files"}, "read_file,search"},
		{[]string{"search_text"}, "search"},
		{[]string{"find_files", "shell"}, "search,shell"},
		{[]string{"read_file", "shell"}, "read_file,shell"},
	} {
		if got := strings.Join(migrateToolset(item.toolset), ","); got != item.want {
			t.Fatalf("migrateToolset(%v) = %q, want %q", item.toolset, got, item.want)
		}
	}
}

// Item 2nq (a) and (b): THE PLACEHOLDER LEAVES DISK, and "model" is never a model.
//
// The operator: "i have an existing connection. i click test. it says 'model' is not
// a type. MODEL WAS NEVER PUT IN BY ME... i asked about 5 times now to remove MODEL
// and there it is." Two earlier items removed the placeholder from Defaults() and
// from addConnection(); both only stop a NEW connection getting it, and nothing ever
// removed it from a configuration already written. Every one of those fixes was a
// no-op for his install.
func TestModelIsNeverAModel2nq(t *testing.T) {
	for _, value := range []string{"", " ", "model", "MODEL", " Model "} {
		connection := Connection{BaseURL: "http://127.0.0.1:8080", Model: value}
		reason := ConnectionSetupReason(&connection)
		if !strings.Contains(reason, "model is empty") {
			t.Errorf("a model of %q is accepted: %q", value, reason)
		}
	}
	connection := Connection{BaseURL: "http://127.0.0.1:8080", Model: "a-real-model"}
	if reason := ConnectionSetupReason(&connection); reason != "" {
		t.Errorf("a real model is refused: %q", reason)
	}
}

// Item 2nv (h): MIGRATION, with verification and no plaintext fallback. Each
// static_bearer connector is moved into the store on load; a variable that is empty, or a
// value that does not read back, leaves the connector unauthenticated and says why.
func TestStaticBearerConnectorsMigrateIntoTheStore2nv(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("SKIPPED: credential storage is DPAPI, so this case is Windows-only.")
	}
	root := t.TempDir()
	t.Setenv("AGENTB_2NV_DEPOT", "migrated-secret-2nv")
	t.Setenv("AGENTB_2NV_EMPTY", "")
	cfg := Config{Services: map[string]Service{
		"depot":  {Kind: "http", BaseURL: "https://api.example.test:8443", Auth: "static_bearer:AGENTB_2NV_DEPOT", AllowedMethods: []string{"GET"}, TimeoutS: 30, MaxBodyKB: 64},
		"hollow": {Kind: "http", BaseURL: "https://other.example.test", Auth: "static_bearer:AGENTB_2NV_EMPTY", AllowedMethods: []string{"GET"}, TimeoutS: 30, MaxBodyKB: 64},
		"helper": {Kind: "http", BaseURL: "https://third.example.test", Auth: "exec:helper token", AllowedMethods: []string{"GET"}, TimeoutS: 30, MaxBodyKB: 64},
	}}
	notices := MigrateConnectorCredentials(&cfg, root)

	if got := cfg.Services["depot"].Auth; got != "stored:depot" {
		t.Fatalf("the migrated connector's auth is %q", got)
	}
	// NO PLAINTEXT FALLBACK: an empty variable leaves it unauthenticated, not on the env.
	if got := cfg.Services["hollow"].Auth; got != "none" {
		t.Fatalf("the connector with an empty variable is %q", got)
	}
	if got := cfg.Services["helper"].Auth; got != "exec:helper token" {
		t.Fatalf("a helper connector was touched: %q", got)
	}
	if len(notices) != 2 {
		t.Fatalf("notices: %v", notices)
	}
	joined := strings.Join(notices, " | ")
	if !strings.Contains(joined, "depot") || !strings.Contains(joined, "hollow") {
		t.Fatalf("the notices do not name the connectors: %s", joined)
	}
	if strings.Contains(joined, "migrated-secret-2nv") {
		t.Fatal("a notice carries the secret")
	}
	// The value is in the store, bound to the connector's own origin, and read back.
	vault := credential.NewVault(root)
	value, entry, err := vault.Secret("depot")
	if err != nil {
		t.Fatal(err)
	}
	if value != "migrated-secret-2nv" || entry.Origin != "https://api.example.test:8443" {
		t.Fatalf("%q %+v", value, entry)
	}
	if vault.Has("hollow") {
		t.Fatal("a connector with an empty variable stored something")
	}
	// Running it again changes nothing and says nothing.
	if again := MigrateConnectorCredentials(&cfg, root); len(again) != 0 {
		t.Fatalf("a second load migrated again: %v", again)
	}
}

// Item 2q5 (e) CHECK 6: a config still holding the old defaults moves to 0.80/0.90;
// any other choice is left alone.
func TestOldCompactionDefaultsMoveToTheNewOnes2q5(t *testing.T) {
	for _, row := range []struct{ soft, summary, wantSoft, wantSummary float64 }{{.75, .85, .80, .90}, {.70, .85, .70, .85}, {.75, .95, .75, .95}} {
		cfg := Defaults(t.TempDir())
		cfg.Context.SoftPct, cfg.Context.SummaryPct = row.soft, row.summary
		ApplyDefaults(&cfg)
		if cfg.Context.SoftPct != row.wantSoft || cfg.Context.SummaryPct != row.wantSummary {
			t.Errorf("%.2f/%.2f became %.2f/%.2f", row.soft, row.summary, cfg.Context.SoftPct, cfg.Context.SummaryPct)
		}
	}
}

func TestRetiredSoftLineDoesNotConstrainTheSummaryLine2tv(t *testing.T) {
	cfg := Defaults(t.TempDir())
	cfg.Context.SoftPct = .95
	cfg.Context.SummaryPct = .90
	if err := cfg.Validate(); err != nil {
		t.Fatalf("retired soft line constrained summary line: %v", err)
	}
}

func TestFourRunsAreTheDefaultWithoutOverwritingAnExplicitOldValue2sg(t *testing.T) {
	if got := Defaults(t.TempDir()).Run.MaxConcurrent; got != 4 {
		t.Fatalf("new default max concurrent=%d, want 4", got)
	}
	cfg := Defaults(t.TempDir())
	cfg.Run.MaxConcurrent = 2
	ApplyDefaults(&cfg)
	if cfg.Run.MaxConcurrent != 2 {
		t.Fatalf("written max concurrent=%d, want unchanged 2", cfg.Run.MaxConcurrent)
	}
}

func TestRemovedLoopAndRoutingKeysLoadSilently2tc(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "harness.json")
	cfg := Defaults(root)
	denyBefore, _ := json.Marshal(cfg.Shell.Deny)
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	document["run"].(map[string]any)["max_consecutive_tool_errors"] = 3
	document["shell"].(map[string]any)["file_routing_guard"] = true
	data, _ = json.MarshalIndent(document, "", "  ")
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, _, _, err := Load(path)
	if err != nil || len(loaded.LoadNotices) != 0 {
		t.Fatalf("load err=%v notices=%#v", err, loaded.LoadNotices)
	}
	denyAfter, _ := json.Marshal(loaded.Shell.Deny)
	if !bytes.Equal(denyBefore, denyAfter) {
		t.Fatalf("shell deny list changed: before=%s after=%s", denyBefore, denyAfter)
	}
	if err := loaded.Save(path); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(path)
	if bytes.Contains(saved, []byte("max_consecutive_tool_errors")) || bytes.Contains(saved, []byte("file_routing_guard")) {
		t.Fatalf("retired keys were serialized again: %s", saved)
	}
}

func TestChatReadingSettingsUsePixelsAndMigrateNamedSteps2rg(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harness.json")
	cfg := Defaults(t.TempDir())
	cfg.Chat.TextSize = "large"
	cfg.Chat.TextColor = "cyan"
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(written, []byte(`"text_size": "large"`)) || !bytes.Contains(written, []byte(`"text_size": 14`)) {
		t.Fatalf("current save did not normalize named size: %s", written)
	}

	var document map[string]any
	if err := json.Unmarshal(written, &document); err != nil {
		t.Fatal(err)
	}
	document["chat"].(map[string]any)["text_size"] = "larger"
	legacy, _ := json.MarshalIndent(document, "", "  ")
	if err := os.WriteFile(path, append(legacy, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, migrated, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !migrated || loaded.Chat.TextSize != 16 {
		t.Fatalf("named size migration = (%v, %#v), want (true, 16)", migrated, loaded.Chat.TextSize)
	}
	rewritten, _ := os.ReadFile(path)
	if bytes.Contains(rewritten, []byte(`"larger"`)) || !bytes.Contains(rewritten, []byte(`"text_size": 16`)) {
		t.Fatalf("migration did not persist once as pixels: %s", rewritten)
	}

	for _, invalid := range []any{8, 33, 12.5, "enormous"} {
		candidate := *loaded
		candidate.Chat.TextSize = invalid
		if err := candidate.Validate(); err == nil {
			t.Errorf("text size %#v was accepted", invalid)
		}
	}
	loaded.Chat.TextColor = "infrared"
	if err := loaded.Validate(); err == nil {
		t.Error("unknown text colour was accepted")
	}
}

func TestGlobalSkillChoicesMigrateOnceToEveryAgentProfile2u7(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harness.json")
	cfg := Defaults(t.TempDir())
	cfg.Skills = map[string]SkillSetting{
		"alpha": {Enabled: true, Source: "existing"},
		"beta":  {Enabled: false, Source: "existing"},
	}
	cfg.Agents = append(cfg.Agents, Agent{Name: "Review", Toolset: FullToolset()})
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, migrated, _, err := Load(path)
	if err != nil || !migrated {
		t.Fatalf("first load migrated=%t err=%v", migrated, err)
	}
	for _, agent := range loaded.Agents {
		if !agent.Skills["alpha"].Enabled || agent.Skills["beta"].Enabled {
			t.Fatalf("agent %q skills=%+v", agent.Name, agent.Skills)
		}
	}
	loaded.Agents[0].Skills["alpha"] = SkillSetting{Enabled: false}
	if !loaded.Agents[1].Skills["alpha"].Enabled {
		t.Fatal("agent skill maps share mutable state")
	}
	if err := loaded.Save(path); err != nil {
		t.Fatal(err)
	}
	reloaded, migratedAgain, _, err := Load(path)
	if err != nil || migratedAgain || reloaded.Agents[0].Skills["alpha"].Enabled || !reloaded.Agents[1].Skills["alpha"].Enabled {
		t.Fatalf("second load migrated=%t agents=%+v err=%v", migratedAgain, reloaded.Agents, err)
	}
}

func TestConfigurationBelowCompatibilityFloorIsRefusedUnchanged2uk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harness.json")
	cfg := Defaults(t.TempDir())
	cfg.ConfigVersion = 13
	before, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	before = append(before, '\n')
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, migrated, created, err := Load(path)
	const message = "This configuration is from a release too old to update directly. Install v1.60.73 first, then update."
	if loaded != nil || migrated || created || err == nil || err.Error() != message {
		t.Fatalf("load = (%+v, %t, %t, %v), want (nil, false, false, %q)", loaded, migrated, created, err, message)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("refused configuration changed\nbefore: %s\nafter: %s", before, after)
	}
}
