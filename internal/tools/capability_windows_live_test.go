//go:build windows

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/credential"
	"harness/internal/events"
	"harness/internal/session"
)

// TestCapabilitySuiteLiveServiceSplit is the fixed 2l-iv host suite. It is
// opt-in because it uses the installed service identity and public network.
func TestCapabilitySuiteLiveServiceSplit(t *testing.T) {
	if os.Getenv("AGENTB_CAPABILITY_LIVE") != "1" {
		t.Skip("set AGENTB_CAPABILITY_LIVE=1 with approved disposable roots")
	}
	workspaceParent, dataRoot, appRoot := os.Getenv("AGENTB_CAPABILITY_WORKSPACE"), os.Getenv("AGENTB_CAPABILITY_DATA"), os.Getenv("AGENTB_CAPABILITY_APP")
	if workspaceParent == "" || dataRoot == "" || appRoot == "" {
		t.Fatal("AGENTB_CAPABILITY_WORKSPACE, _DATA, and _APP are required")
	}
	workspace, err := os.MkdirTemp(workspaceParent, "agentb-capability-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := removeCapabilityFixture(workspace); err != nil {
			t.Errorf("remove capability fixture: %v", err)
		}
	})
	cfg := config.Defaults(workspace)
	if listen := strings.TrimSpace(os.Getenv("AGENTB_CAPABILITY_LISTEN")); listen != "" {
		cfg.Listen = listen
	}
	var connection struct {
		Shell struct {
			ServiceAccount struct {
				Enabled bool `json:"enabled"`
			} `json:"service_account"`
		} `json:"shell"`
	}
	configBytes, err := os.ReadFile(filepath.Join(dataRoot, "harness.json"))
	if err != nil {
		t.Fatalf("read gated configuration: %v", err)
	}
	if err := json.Unmarshal(configBytes, &connection); err != nil {
		t.Fatalf("decode gated configuration: %v", err)
	}
	serviceSplitEnabled := connection.Shell.ServiceAccount.Enabled
	if serviceSplitEnabled && os.Getenv("AGENTB_CAPABILITY_OWNS_SERVICE_ACCOUNT") != "1" {
		t.Log("not exercised: prerequisite — service split fixture does not own a disposable account and credential")
		serviceSplitEnabled = false
	}
	cfg.Shell.ServiceAccount.Enabled = serviceSplitEnabled
	t.Logf("gate configuration: service split enabled=%t", serviceSplitEnabled)
	guard := false
	cfg.Shell.FileRoutingGuard = &guard
	store := credential.New(dataRoot)
	shell := NewShell(cfg.Shell)
	shell.Configure(cfg)
	shell.SetCredentialStore(store)
	coordinator := NewFileCoordinator(session.NewWorkspaceRegistry(), func(string) string { return "capability" }, events.NewBus())
	shell.SetFileCoordinator(coordinator)
	fileIdentity := NewFileIdentity(store)
	fileIdentity.Configure(cfg)
	// Item 2gc: Windows' own utilities are resolved by their absolute path.
	// PATH-resolving this one found Git's POSIX whoami on a host whose PATH
	// reaches usr/bin first, and its path contains spaces, so the exec:
	// credential argv below split at the space and the call failed.
	whoami := filepath.Join(os.Getenv("SystemRoot"), "System32", "whoami.exe")
	if _, err := os.Stat(whoami); err != nil {
		t.Fatal(err)
	}
	identityOutput, err := exec.Command(whoami).Output()
	if err != nil {
		t.Fatal(err)
	}
	operatorIdentity := strings.TrimSpace(string(identityOutput))
	if operatorIdentity == "" || strings.EqualFold(filepath.Base(operatorIdentity), cfg.Shell.ServiceAccount.Account) || strings.HasSuffix(strings.ToLower(operatorIdentity), `\`+strings.ToLower(cfg.Shell.ServiceAccount.Account)) {
		t.Fatalf("operator identity evidence is not distinct from service account: %q", operatorIdentity)
	}
	serviceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/direct" && r.Header.Get("Authorization") == "" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"direct":true}`))
			return
		}
		if r.URL.Path != "/api/identity" || r.Header.Get("Authorization") != "Bearer "+operatorIdentity {
			http.Error(w, `{"title":"identity mismatch"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"identity_match":true}`))
	}))
	defer serviceServer.Close()
	_, phonePort, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		t.Fatalf("capability listen %q: %v", cfg.Listen, err)
	}
	t.Setenv("AGENTB_CAPABILITY_FAKE_DEVICE", "not-a-device-credential")
	cfg.Services = map[string]config.Service{"identity": {
		BaseURL: serviceServer.URL + "/api", Auth: "exec:" + whoami,
		AllowedMethods: []string{"GET"}, TimeoutS: 10, MaxBodyKB: 16,
	}, "phone-control": {
		BaseURL: "http://localhost:" + phonePort + "/api", Auth: "static_bearer:AGENTB_CAPABILITY_FAKE_DEVICE",
		AllowedMethods: []string{"GET", "POST"}, TimeoutS: 10, MaxBodyKB: 16,
	}}
	callService := NewCallService(cfg.Services)
	callService.Configure(cfg)
	toolRegistry := New(
		fileIdentity.Wrap(NewReadFile(cfg.Tools.ReadFile)), fileIdentity.Wrap(NewListDir(cfg.Tools.ListDir)),
		fileIdentity.Wrap(NewWriteFile(coordinator)), fileIdentity.Wrap(NewEditFile(coordinator)),
		fileIdentity.Wrap(NewGrep(cfg.Tools.Grep, cfg.Tools.ListDir)), NewFetch(cfg.Tools.Fetch), fileIdentity.Wrap(NewGlob(cfg.Tools.FindFiles)), callService,
	)
	enabled := map[string]bool{"read_file": true, "list_dir": true, "write_file": true, "edit_file": true, "search_text": true, "fetch_url": true, "find_files": true, "call_service": true}
	item := &session.Session{ID: "capability", Workspace: workspace, LastSeen: map[string]time.Time{}, ToolsEnabled: enabled}

	t.Run("absolute_file_tool_reach_2jt", func(t *testing.T) {
		outside, err := os.MkdirTemp(workspaceParent, "agentb-capability-outside-")
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := removeCapabilityFixture(outside); err != nil {
				t.Errorf("remove outside capability fixture: %v", err)
			}
		}()
		path := filepath.Join(outside, "reachable.txt")
		if err := os.WriteFile(path, []byte("absolute reach"), 0o600); err != nil {
			t.Fatal(err)
		}
		if result := toolRegistry.CallDetailed(context.Background(), item, "read_file", map[string]any{"path": path}); !result.OK || !strings.Contains(result.Content, "absolute reach") {
			t.Fatalf("absolute read=%+v", result)
		}
		if result := toolRegistry.CallDetailed(context.Background(), item, "list_dir", map[string]any{"path": outside}); !result.OK || !strings.Contains(result.Content, "reachable.txt") {
			t.Fatalf("absolute list=%+v", result)
		}
		t.Log("contract=changed-by-2jt: file tools reach an absolute folder outside scratch and registered plans")
	})

	t.Run("write_and_run_python_node_shell", func(t *testing.T) {
		if reason := capabilityApplicability("service split", serviceSplitEnabled); reason != "" {
			t.Skip(reason)
		}
		cases := []struct{ name, file, body, want string }{
			{"python", "capability.py", "print('python-capability')", "python-capability"},
			{"node", "capability.js", "console.log('node-capability')", "node-capability"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if result := toolRegistry.CallDetailed(context.Background(), item, "write_file", map[string]any{"path": tc.file, "content": tc.body}); !result.OK {
					t.Fatalf("write=%+v", result)
				}
				interpreter, err := exec.LookPath(tc.name)
				if err != nil {
					t.Skipf("%s not installed", tc.name)
				}
				command := "& " + quotePowerShell(interpreter) + " " + quotePowerShell(tc.file)
				detail := shell.CallDetailed(context.Background(), item, map[string]any{"command": command})
				if detail.OperatorOverrideReason != "" {
					output, overrideErr := shell.CallAsOperator(context.Background(), item, map[string]any{"command": command})
					if overrideErr != nil || !strings.Contains(output, tc.want) {
						t.Fatalf("service=%+v override=%q err=%v", detail, output, overrideErr)
					}
					t.Logf("moved behind operator decision: %s", detail.OperatorOverrideReason)
					return
				}
				if detail.Err != nil || !strings.Contains(detail.Content, tc.want) {
					t.Fatalf("detail=%+v", detail)
				}
			})
		}
	})

	t.Run("project_tests_shell", func(t *testing.T) {
		source := os.Getenv("AGENTB_CAPABILITY_SOURCE")
		if source == "" {
			t.Skip("AGENTB_CAPABILITY_SOURCE not set")
		}
		goExe := os.Getenv("AGENTB_CAPABILITY_GO")
		if goExe == "" {
			goExe = filepath.Join(source, ".tools", "go", "bin", "go.exe")
		}
		if _, err := os.Stat(goExe); err != nil {
			t.Skip(err)
		}
		command := "$env:AGENTB_CAPABILITY_LIVE=$null; Set-Location -LiteralPath " + quotePowerShell(source) + "; & " + quotePowerShell(goExe) + " test ./internal/tools"
		output, runErr := shell.CallAsOperator(context.Background(), item, map[string]any{"command": command, "timeout_s": 600})
		if runErr != nil || !strings.Contains(output, "ok  \tharness/internal/tools") {
			t.Fatalf("output=%q err=%v", output, runErr)
		}
	})

	t.Run("read_edit_search_find_file_tools", func(t *testing.T) {
		if reason := capabilityApplicability("service split", serviceSplitEnabled); reason != "" {
			t.Skip(reason)
		}
		path := filepath.Join(workspace, "capability-text.txt")
		if err := os.WriteFile(path, []byte("before needle   \r\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if result := toolRegistry.CallDetailed(context.Background(), item, "read_file", map[string]any{"path": path}); !result.OK {
			t.Fatalf("read=%+v", result)
		}
		if result := toolRegistry.CallDetailed(context.Background(), item, "edit_file", map[string]any{"path": path, "old_string": "before needle\n", "new_string": "after needle\n"}); !result.OK || !strings.Contains(result.Content, "strategy: whitespace-normalized") || !strings.Contains(result.Content, "--- a/capability-text.txt") {
			t.Fatalf("edit=%+v", result)
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != "after needle\r\n" {
			t.Fatalf("edit line-ending result=%q err=%v", data, err)
		}
		if result := toolRegistry.CallDetailed(context.Background(), item, "search_text", map[string]any{"path": workspace, "pattern": "needle"}); !result.OK || !strings.Contains(result.Content, "capability-text.txt") {
			t.Fatalf("search=%+v", result)
		}
		if result := toolRegistry.CallDetailed(context.Background(), item, "find_files", map[string]any{"path": workspace, "pattern": "capability-*"}); !result.OK || !strings.Contains(result.Content, "capability-text.txt") {
			t.Fatalf("find=%+v", result)
		}
		if result := toolRegistry.CallDetailed(context.Background(), item, "list_dir", map[string]any{"path": workspace}); !result.OK || !strings.Contains(result.Content, "capability-text.txt") {
			t.Fatalf("list=%+v", result)
		}
	})

	t.Run("d_plan_file_boundary_under_service_identity", func(t *testing.T) {
		if reason := capabilityApplicability("service split", serviceSplitEnabled); reason != "" {
			t.Skip(reason)
		}
		repositoryFile := filepath.Join(workspace, "d-repository-source.txt")
		if err := os.WriteFile(repositoryFile, []byte("repository evidence"), 0o600); err != nil {
			t.Fatal(err)
		}
		d := &session.Session{ID: "capability-d", Role: "d", Workspace: workspace, PlansRoot: filepath.Join(workspace, "d-plans"), LastSeen: map[string]time.Time{}, ToolsEnabled: enabled}
		if result := toolRegistry.CallDetailed(context.Background(), d, "write_file", map[string]any{"path": "plan.md", "content": "# Capability plan\n"}); !result.OK {
			t.Fatalf("d write=%+v", result)
		}
		if d.PlanID == "" || d.PlanDir == "" {
			t.Fatalf("d plan was not bound: %+v", d.Snapshot())
		}
		if result := toolRegistry.CallDetailed(context.Background(), d, "read_file", map[string]any{"path": repositoryFile}); !result.OK || !strings.Contains(result.Content, "repository evidence") {
			t.Fatalf("d repository read=%+v", result)
		}
		if result := toolRegistry.CallDetailed(context.Background(), d, "write_file", map[string]any{"path": filepath.Join(workspace, "d-reach.txt"), "content": "reachable"}); !result.OK {
			t.Fatalf("d ordinary absolute write=%+v", result)
		}
		sibling := filepath.Join(d.PlansRoot, "sibling")
		if err := os.MkdirAll(sibling, 0o700); err != nil {
			t.Fatal(err)
		}
		siblingPlan := filepath.Join(sibling, "plan.md")
		if err := os.WriteFile(siblingPlan, []byte("# Sibling plan\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if result := toolRegistry.CallDetailed(context.Background(), d, "read_file", map[string]any{"path": siblingPlan}); !result.OK || !strings.Contains(result.Content, "Sibling plan") {
			t.Fatalf("d plan-tree read did not use the union jail: %+v", result)
		}
		if result := toolRegistry.CallDetailed(context.Background(), d, "write_file", map[string]any{"path": siblingPlan, "content": "changed"}); result.OK || result.OperatorOverrideReason != "" {
			t.Fatalf("d wrote a sibling plan: %+v", result)
		}
		t.Log("contract=changed-by-2jt: ordinary absolute writes are reachable; sibling plan ownership remains enforced")
	})

	t.Run("service_identity_denies_operator_data_and_install_writes_2jz", func(t *testing.T) {
		if reason := capabilityApplicability("service split", serviceSplitEnabled); reason != "" {
			t.Skip(reason)
		}
		targets := []string{filepath.Join(dataRoot, "harness.json"), filepath.Join(appRoot, "capability-denied.txt")}
		if source := os.Getenv("AGENTB_CAPABILITY_SOURCE"); source != "" {
			targets = append(targets, filepath.Join(source, "capability-denied.txt"))
		}
		for _, target := range targets {
			command := `Set-Content -LiteralPath ` + quotePowerShell(target) + ` -Value refused`
			detail := shell.CallDetailed(context.Background(), item, map[string]any{"command": command})
			if detail.OperatorOverrideReason == "" || !strings.Contains(strings.ToLower(detail.Content+" "+detail.OperatorOverrideReason), "denied") {
				t.Fatalf("target=%s detail=%+v", target, detail)
			}
		}
		t.Log("contract=changed-by-2jz: service shell cannot mutate harness.json, the installed application, or the source repository")
	})

	t.Run("service_identity_writes_inside_plan_repo_2jz", func(t *testing.T) {
		if reason := capabilityApplicability("service split", serviceSplitEnabled); reason != "" {
			t.Skip(reason)
		}
		target := filepath.Join(workspace, "service-plan-write.txt")
		detail := shell.CallDetailed(context.Background(), item, map[string]any{"command": `Set-Content -LiteralPath ` + quotePowerShell(target) + ` -Value reachable`})
		if detail.Err != nil || detail.OperatorOverrideReason != "" {
			t.Fatalf("detail=%+v", detail)
		}
		data, err := os.ReadFile(target)
		if err != nil || !strings.Contains(string(data), "reachable") {
			t.Fatalf("plan repo write=%q err=%v", data, err)
		}
		t.Log("contract=changed-by-2jz: service shell writes inside an ACL-granted plan repository")
	})

	t.Run("fetch_public_text_fetch_url", func(t *testing.T) {
		value, err := NewFetch(cfg.Tools.Fetch).Call(context.Background(), item, map[string]any{"url": "https://example.com"})
		if err != nil || !strings.Contains(value, "Example Domain") {
			t.Fatalf("fetch=%q err=%v", value, err)
		}
	})

	t.Run("web_search_per_engine_minimums", func(t *testing.T) {
		fetch := NewFetch(cfg.Tools.Fetch)
		search := newWebSearch(fetch, cfg.Tools.WebSearch, defaultWebSearchAdapters(), false)
		client := fetch.client(cfg.Tools.Fetch)
		defer client.CloseIdleConnections()
		queries := map[string]string{
			"duckduckgo_html": "golang context cancellation", "duckduckgo_lite": "golang context cancellation",
			"bing": "golang context cancellation", "brave": "golang context cancellation",
			"wikipedia": "Go programming language wikipedia", "github": "github kubernetes repository",
			"hacker_news": "hacker news golang", "arxiv": "research paper large language models",
			"stackexchange": "golang context cancellation error", "pkg_go_dev": "golang context package",
			"npm": "npm react package",
		}
		// Item 2lq (d): the arm emits a TABLE, and every engine in the tool appears
		// in it with a state that is one of three things -- live, benched with the
		// date and reason it was benched, or retired with the date and reason it was
		// removed. Before this, an engine that had been quietly dropped from the
		// merge left no row at all, which is how the bench list came to be three
		// days stale without anyone reading it.
		type engineRow struct{ name, state, detail string }
		rows := []engineRow{}
		for _, adapter := range search.adapters {
			query := queries[adapter.Name()]
			// Item 2if: the table measures THE SHIPPED DEFAULT. It hard-coded 5, so it
			// could never have answered which adapters can fill ten -- rel-1.16.0/W0
			// found every engine reporting exactly 5 for that reason alone.
			rawURL, ok := adapter.URL(query, webSearchWeb, webSearchDefaultLimit)
			if !ok {
				t.Fatalf("%s did not route fixed query %q", adapter.Name(), query)
			}
			requestCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Tools.WebSearch.PerEngineTimeoutS)*time.Second)
			hits, searchErr := search.searchOne(requestCtx, client, cfg.Tools.Fetch, adapter, rawURL, webSearchDefaultLimit)
			cancel()
			if reason, benched := initiallyBenchedWebSearchEngines[adapter.Name()]; benched {
				rows = append(rows, engineRow{adapter.Name(), "benched", fmt.Sprintf("%s; probe returned %d results, error=%v", reason, len(hits), searchErr)})
				continue
			}
			// (c): a rate limit is reported as a rate limit. Brave answers 429 to this
			// very query, and calling that a failure is what made the old table
			// unreadable.
			var limited *webSearchRateLimited
			if errors.As(searchErr, &limited) {
				rows = append(rows, engineRow{adapter.Name(), "rate-limited", fmt.Sprintf("%s; benches itself on the first one, not the third", searchErr)})
				continue
			}
			if searchErr != nil || len(hits) < 1 {
				rows = append(rows, engineRow{adapter.Name(), "FAILING", fmt.Sprintf("results=%d error=%v", len(hits), searchErr)})
				t.Errorf("engine=%s state=FAILING results=%d error=%v -- a live engine that does not answer is either fixed, benched with a date, or retired", adapter.Name(), len(hits), searchErr)
				continue
			}
			rows = append(rows, engineRow{adapter.Name(), "live", fmt.Sprintf("results=%d", len(hits))})
		}
		// The retired engines have no adapter to probe, and that is exactly why they
		// need a row: otherwise their absence is indistinguishable from an oversight.
		retiredNames := make([]string, 0, len(retiredWebSearchEngines))
		for name := range retiredWebSearchEngines {
			retiredNames = append(retiredNames, name)
		}
		sort.Strings(retiredNames)
		for _, name := range retiredNames {
			rows = append(rows, engineRow{name, "retired", retiredWebSearchEngines[name]})
		}
		if len(rows) != len(search.adapters)+len(retiredWebSearchEngines) {
			t.Errorf("the table has %d rows for %d adapters and %d retired engines; every engine gets a row",
				len(rows), len(search.adapters), len(retiredWebSearchEngines))
		}
		t.Logf("web_search engine table (%d rows: %d in the tool, %d retired)", len(rows), len(search.adapters), len(retiredWebSearchEngines))
		for _, row := range rows {
			t.Logf("  engine=%-16s state=%-12s %s", row.name, row.state, row.detail)
		}
	})

	t.Run("internal_service_exec_identity_call_service", func(t *testing.T) {
		detail := toolRegistry.CallDetailed(context.Background(), item, "call_service", map[string]any{"service": "identity", "method": "GET", "path": "identity"})
		if !detail.OK || !detail.OperatorContext || !strings.Contains(detail.Content, `"identity_match":true`) || strings.Contains(detail.Content, operatorIdentity) {
			t.Fatalf("detail=%+v", detail)
		}
		t.Logf("call_service exec child identity: %s (service account: %s); contract=new/pass", operatorIdentity, cfg.Shell.ServiceAccount.Account)
	})

	t.Run("internal_service_direct_url_2ju", func(t *testing.T) {
		detail := toolRegistry.CallDetailed(context.Background(), item, "call_service", map[string]any{"service": serviceServer.URL + "/api/direct", "method": "GET"})
		if !detail.OK || detail.OperatorContext || !strings.Contains(detail.Content, `"direct":true`) {
			t.Fatalf("detail=%+v", detail)
		}
		t.Log("contract=changed-by-2ju: unregistered absolute URL called without a credential")
	})

	t.Run("internal_service_credential_host_mismatch_2ju", func(t *testing.T) {
		detail := toolRegistry.CallDetailed(context.Background(), item, "call_service", map[string]any{"service": "identity", "method": "GET", "path": "http://foreign.invalid/never"})
		if detail.OK || detail.OperatorContext || !strings.Contains(detail.Content, "credential host mismatch") {
			t.Fatalf("detail=%+v", detail)
		}
		t.Log("contract=changed-by-2ju: a registered service refuses a foreign credential host")
	})

	t.Run("control_plane_shell_get_and_post_refused_2jy", func(t *testing.T) {
		if reason := capabilityApplicability("service split", serviceSplitEnabled); reason != "" {
			t.Skip(reason)
		}
		base := "http://" + cfg.Listen
		for _, tc := range []struct{ name, command string }{
			{"state", `curl.exe -s -o NUL -w "%{http_code}" ` + quotePowerShell(base+"/api/state")},
			{"config", `curl.exe -s -o NUL -w "%{http_code}" -X POST -H "Content-Type: application/json" -d "{}" ` + quotePowerShell(base+"/api/config")},
		} {
			t.Run(tc.name, func(t *testing.T) {
				detail := shell.CallDetailed(context.Background(), item, map[string]any{"command": tc.command})
				if detail.Err != nil || strings.TrimSpace(detail.Content) != "401" {
					t.Fatalf("detail=%+v", detail)
				}
			})
		}
		t.Log("contract=changed-by-2jy: shell receives 401 for control-plane state and mutation")
	})

	t.Run("control_plane_run_script_refused_2jy", func(t *testing.T) {
		if reason := capabilityApplicability("service split", serviceSplitEnabled); reason != "" {
			t.Skip(reason)
		}
		source := `$base = ` + quotePowerShell("http://"+cfg.Listen) + "\n" +
			`curl.exe -s -o NUL -w "%{http_code}" "$base/api/state"` + "\n" +
			`Write-Output ""` + "\n" +
			`curl.exe -s -o NUL -w "%{http_code}" -X POST -H "Content-Type: application/json" -d "{}" "$base/api/config"`
		detail := NewRunScript(shell).CallDetailed(context.Background(), item, map[string]any{"language": "powershell", "source": source})
		statuses := strings.Fields(detail.Content)
		if detail.Err != nil || len(statuses) != 2 || statuses[0] != "401" || statuses[1] != "401" {
			t.Fatalf("detail=%+v", detail)
		}
		t.Log("contract=changed-by-2jy: run_script receives 401 for control-plane state and mutation")
	})

	t.Run("control_plane_call_service_refused_2jy", func(t *testing.T) {
		detail := toolRegistry.CallDetailed(context.Background(), item, "call_service", map[string]any{"service": "http://" + cfg.Listen + "/api/state", "method": "GET"})
		if detail.OK || !strings.Contains(detail.Content, "refused the Agent_b listener") {
			t.Fatalf("detail=%+v", detail)
		}
		t.Log("contract=changed-by-2jy: call_service refuses the Agent_b listener before dialing")
	})

	t.Run("phone_control_plane_shell_refused_2kl", func(t *testing.T) {
		if reason := capabilityApplicability("service split", serviceSplitEnabled); reason != "" {
			t.Skip(reason)
		}
		base := "http://" + cfg.Listen
		for _, command := range []string{`curl.exe -s -o NUL -w "%{http_code}" -X POST -H "Content-Type: application/json" -d "{\"name\":\"tool\"}" ` + quotePowerShell(base+"/api/phone/enrolment/redeem"), `curl.exe -s -o NUL -w "%{http_code}" -H "Authorization: Bearer not-a-device-credential" ` + quotePowerShell(base+"/api/state")} {
			detail := shell.CallDetailed(context.Background(), item, map[string]any{"command": command})
			if detail.Err != nil || strings.TrimSpace(detail.Content) != "401" {
				t.Fatalf("detail=%+v", detail)
			}
		}
		t.Log("contract=unchanged-by-2kl: shell gets 401 at enrolment and device-authenticated state")
	})

	t.Run("phone_control_plane_run_script_refused_2kl", func(t *testing.T) {
		if reason := capabilityApplicability("service split", serviceSplitEnabled); reason != "" {
			t.Skip(reason)
		}
		base := "http://" + cfg.Listen
		source := `curl.exe -s -o NUL -w "%{http_code}" -X POST -H "Content-Type: application/json" -d "{\"name\":\"tool\"}" ` + quotePowerShell(base+"/api/phone/enrolment/redeem") + "\n" +
			`Write-Output ""` + "\n" +
			`curl.exe -s -o NUL -w "%{http_code}" -H "Authorization: Bearer not-a-device-credential" ` + quotePowerShell(base+"/api/state")
		detail := NewRunScript(shell).CallDetailed(context.Background(), item, map[string]any{"language": "powershell", "source": source})
		statuses := strings.Fields(detail.Content)
		if detail.Err != nil || len(statuses) != 2 || statuses[0] != "401" || statuses[1] != "401" {
			t.Fatalf("detail=%+v", detail)
		}
		t.Log("contract=unchanged-by-2kl: run_script gets 401 at enrolment and device-authenticated state")
	})

	t.Run("phone_control_plane_call_service_refused_2kl", func(t *testing.T) {
		// This arm asserts what the phone control plane REFUSES, so it needs
		// something listening to do the refusing. Production is the only listener
		// on that port, a worker never starts it, and its two sibling arms already
		// report a missing prerequisite rather than failing. This one did not: it
		// reported a connection refused as a contract failure, which reads as "the
		// control plane accepted the call" to anyone skimming the suite. That is a
		// louder wrong answer than the silence it replaced.
		if address := phoneControlAddress(cfg); !somethingIsListening(address) {
			t.Skipf("not exercised: prerequisite — nothing is listening on %s (production is the only listener there, and a worker never starts it)", address)
		}
		for _, args := range []map[string]any{{"service": "phone-control", "method": "POST", "path": "phone/enrolment/redeem", "body": map[string]any{"name": "tool"}}, {"service": "phone-control", "method": "GET", "path": "state"}} {
			detail := toolRegistry.CallDetailed(context.Background(), item, "call_service", args)
			if !detail.OK || detail.Metadata["status"] != http.StatusUnauthorized || !strings.Contains(detail.Content, `"status":401`) {
				t.Fatalf("detail=%+v", detail)
			}
		}
		t.Log("contract=unchanged-by-2kl: call_service gets 401 at enrolment and device-authenticated state through a localhost alias; the exact-listener pre-dial refusal remains")
	})

	t.Run("boundary_file_tool_operator_decision", func(t *testing.T) {
		if reason := capabilityApplicability("service split", serviceSplitEnabled); reason != "" {
			t.Skip(reason)
		}
		wrapped := fileIdentity.Wrap(NewReadFile(cfg.Tools.ReadFile)).(DetailedTool)
		detail := wrapped.CallDetailed(context.Background(), item, map[string]any{"path": filepath.Join(dataRoot, "harness.json")})
		if detail.OperatorOverrideReason == "" {
			t.Fatalf("service boundary did not request operator decision: %+v", detail)
		}
	})

	t.Run("multiline_powershell_run_script", func(t *testing.T) {
		if reason := capabilityApplicability("service split", serviceSplitEnabled); reason != "" {
			t.Skip(reason)
		}
		detail := NewRunScript(shell).CallDetailed(context.Background(), item, map[string]any{"language": "powershell", "source": "$sum = 0\n1..4 | ForEach-Object { $sum += $_ }\n\"sum=$sum\""})
		if detail.Err != nil || detail.OperatorOverrideReason != "" || !strings.Contains(detail.Content, "sum=10") {
			t.Fatalf("detail=%+v", detail)
		}
	})
}

// phoneControlAddress is where the phone-control service points. The refusal
// this arm asserts happens at that listener, so its absence is a prerequisite.
func phoneControlAddress(cfg config.Config) string {
	service, ok := cfg.Services["phone-control"]
	if !ok {
		return "localhost:8790"
	}
	if parsed, err := url.Parse(service.BaseURL); err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return service.BaseURL
}

func somethingIsListening(address string) bool {
	connection, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func capabilityApplicability(feature string, enabled bool) string {
	if enabled {
		return ""
	}
	return "not applicable — " + feature + " disabled"
}

func TestCapabilityApplicability(t *testing.T) {
	if got := capabilityApplicability("service split", false); got != "not applicable — service split disabled" {
		t.Fatalf("disabled=%q", got)
	}
	if got := capabilityApplicability("service split", true); got != "" {
		t.Fatalf("enabled=%q", got)
	}
}

func TestRemoveCapabilityFixture(t *testing.T) {
	path := os.Getenv("AGENTB_CAPABILITY_CLEANUP_WORKSPACE")
	if path == "" {
		t.Skip("set AGENTB_CAPABILITY_CLEANUP_WORKSPACE to an exact capability fixture")
	}
	if err := removeCapabilityFixture(path); err != nil {
		t.Fatal(err)
	}
}

func removeCapabilityFixture(path string) error {
	clean, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(strings.ToLower(filepath.Base(clean)), "agentb-") {
		return fmt.Errorf("refusing non-capability fixture %q", clean)
	}
	return os.RemoveAll(clean)
}
