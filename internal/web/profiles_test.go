package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"harness/internal/agent"
	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/profiles"
	"harness/internal/session"
	"harness/internal/tools"
)

func TestProfilesEndpointCreatesRenamesAndSwitches(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(t.TempDir())
	path := filepath.Join(root, "harness.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	manager, _, err := profiles.Open(root, path, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := New(&cfg, path, t.TempDir(), RuntimeRoots{Data: root, Profile: manager.Root(manager.Active())}, events.NewBus())
	server.SetProfiles(manager)
	var switched string
	server.SetProfileChanged(func(value string) error { switched = value; return nil })

	response := profileCall(t, server, `{"action":"create","name":"Second"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	response = profileCall(t, server, `{"action":"switch","name":"Second"}`)
	if response.Code != http.StatusOK || manager.Active() != "Second" || switched != manager.Root("Second") {
		t.Fatalf("switch: %d active=%q callback=%q body=%s", response.Code, manager.Active(), switched, response.Body.String())
	}
	response = profileCall(t, server, `{"action":"rename","name":"Second","new_name":"Work"}`)
	if response.Code != http.StatusOK || manager.Active() != "Work" {
		t.Fatalf("rename: %d active=%q body=%s", response.Code, manager.Active(), response.Body.String())
	}
	state := server.snapshot()
	profilesState := state["profiles"].(map[string]any)
	if profilesState["active"] != "Work" {
		t.Fatalf("state profiles=%+v", profilesState)
	}
}

func TestChatSkillRequestRaisesTheExistingCard2pc(t *testing.T) {
	root, workspace := t.TempDir(), t.TempDir()
	cfg := config.Defaults(root)
	configPath := filepath.Join(root, "harness.json")
	if err := cfg.Save(configPath); err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	server := New(&cfg, configPath, t.TempDir(), RuntimeRoots{Data: root, Profile: root}, bus)
	runner := agent.NewRunner(bus, tools.New(), nil, cfg.Connection, server.ConfigSnapshot)
	server.SetRuntime(nil, runner, nil)
	source := filepath.Join(t.TempDir(), "report-kit")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: report-kit\ndescription: Builds invented fixture reports.\n---\nProcedure.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	chat := &session.Session{ID: "chat", Workspace: workspace}
	offer := func(path, name, decision string) (map[string]any, string) {
		chat.Append(events.Message{Role: "assistant", Content: "Add " + path + " as a skill"})
		eventsCh, unsubscribe := bus.Subscribe()
		defer unsubscribe()
		done := make(chan string, 1)
		go func() { detail, _ := server.Proposal(context.Background(), chat, "run"); done <- detail }()
		event := <-eventsCh
		data := event.Data.(map[string]any)
		if err := runner.Gate().Decide(chat.ID, "skill-proposal-"+name, decision); err != nil {
			t.Fatal(err)
		}
		return data, <-done
	}
	card, detail := offer(source, "report-kit", "approve")
	if card["name"] != "Add skill report-kit?" || !strings.Contains(card["args"].(map[string]any)["skill_md"].(string), "Builds invented") || detail != "skill report-kit added" {
		t.Fatalf("card=%+v detail=%q", card, detail)
	}
	state := server.skillState()
	if len(state) != 1 || !state[0].Enabled || state[0].Source != "added in chat from "+source || !strings.Contains(server.Index(), "report-kit | Builds invented fixture reports.") {
		t.Fatalf("skills=%+v", state)
	}
	declined := filepath.Join(t.TempDir(), "declined-skill")
	_ = os.MkdirAll(declined, 0o700)
	_ = os.WriteFile(filepath.Join(declined, "SKILL.md"), []byte("---\nname: declined-skill\ndescription: Must not be copied.\n---\n"), 0o600)
	before, _ := os.ReadFile(filepath.Join(root, "skills", "report-kit", "SKILL.md"))
	_, detail = offer(declined, "declined-skill", "deny")
	after, _ := os.ReadFile(filepath.Join(root, "skills", "report-kit", "SKILL.md"))
	_, copiedErr := os.Stat(filepath.Join(root, "skills", "declined-skill"))
	if detail != "skill proposal declined" || string(before) != string(after) || !os.IsNotExist(copiedErr) {
		t.Fatalf("decline changed profile: %q", detail)
	}
	bad := filepath.Join(t.TempDir(), "bad")
	_ = os.MkdirAll(bad, 0o700)
	_ = os.WriteFile(filepath.Join(bad, "SKILL.md"), []byte("---\nname: BAD\ndescription: invalid\n---\n"), 0o600)
	missing := filepath.Join(t.TempDir(), "missing")
	_ = os.MkdirAll(missing, 0o700)
	for path, want := range map[string]string{missing: "SKILL.md is missing", bad: "name must be", source: "already exists"} {
		chat.Append(events.Message{Role: "assistant", Content: "Add " + path + " as a skill"})
		got, err := server.Proposal(context.Background(), chat, "run")
		if err != nil || !strings.Contains(got, want) {
			t.Fatalf("invalid %s: %q %v", path, got, err)
		}
	}
	prompt, _ := os.ReadFile(filepath.Join("..", "..", "prompts", "system.md"))
	line := "To add a folder or zip as a skill, reply exactly “Add <absolute path> as a skill”; Agent_b will show its existing skill card."
	if !strings.Contains(string(prompt), line) || len(strings.Fields(line)) > 40 {
		t.Fatalf("missing or long prompt rule")
	}
}

func TestSkillImportUsesNormalizedPathAndArrivesEnabled2pe(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(t.TempDir())
	path := filepath.Join(root, "harness.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	manager, _, err := profiles.Open(root, path, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := New(&cfg, path, t.TempDir(), RuntimeRoots{Data: root, Profile: manager.Root(manager.Active())}, events.NewBus())
	server.SetProfiles(manager)
	source := filepath.Join(t.TempDir(), "report-kit")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: report-kit\ndescription: Builds invented fixture reports.\n---\nProcedure.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `{"action":"import","path":` + strconv.Quote(`  "`+source+`" `) + `}`
	response := httptest.NewRecorder()
	server.skillsEndpoint(response, httptest.NewRequest(http.MethodPost, "/api/skills", strings.NewReader(body)))
	if response.Code != 200 {
		t.Fatalf("import=%d %s", response.Code, response.Body.String())
	}
	state := server.skillState()
	if len(state) != 1 || !state[0].Enabled || !strings.Contains(state[0].Source, source) {
		t.Fatalf("state=%+v", state)
	}
	response = httptest.NewRecorder()
	server.skillsEndpoint(response, httptest.NewRequest(http.MethodPost, "/api/skills", strings.NewReader(`{"action":"enable","name":"report-kit","enabled":false}`)))
	server.skillsEndpoint(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/skills", strings.NewReader(`{"action":"rescan"}`)))
	if response.Code != 200 || server.skillState()[0].Enabled {
		t.Fatalf("disable/rescan=%d %s state=%+v", response.Code, response.Body.String(), server.skillState())
	}
	restarted, _, _, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened, _, err := profiles.Open(root, path, restarted)
	if err != nil {
		t.Fatal(err)
	}
	afterRestart := New(restarted, path, t.TempDir(), RuntimeRoots{Data: root, Profile: reopened.Root(reopened.Active())}, events.NewBus())
	if afterRestart.skillState()[0].Enabled {
		t.Fatal("disabled skill came back on after restart")
	}
}

func TestSkillImportErrorsNameTheTriedPath2pe(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(root)
	server := New(&cfg, filepath.Join(root, "harness.json"), t.TempDir(), RuntimeRoots{Data: root, Profile: root}, events.NewBus())
	noSkill := t.TempDir()
	for _, tried := range []string{"", filepath.Join(root, "missing"), noSkill} {
		body := `{"action":"import","path":` + strconv.Quote(tried) + `}`
		response := httptest.NewRecorder()
		server.skillsEndpoint(response, httptest.NewRequest(http.MethodPost, "/api/skills", strings.NewReader(body)))
		var result struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(response.Body.Bytes(), &result)
		if response.Code != 400 || !strings.Contains(result.Error, displaySkillPath(tried)) || strings.Contains(result.Error, "open SKILL.md") {
			t.Errorf("tried=%q decoded=%q want=%q response=%d %s", tried, result.Error, displaySkillPath(tried), response.Code, response.Body.String())
		}
	}
}

func TestHermesPreviewAndImport2nd(t *testing.T) {
	root, source := t.TempDir(), filepath.Join(t.TempDir(), "hermes")
	planted := "secret-" + strings.Repeat("x", 17)
	plantedTwo := "secret-" + strings.Repeat("y", 17)
	profile := filepath.Join(root, "profile")
	for _, dir := range []string{"memories", "skills/writing/Report Kit/scripts", "skills/clean/portable", "cron", "sessions"} {
		if err := os.MkdirAll(filepath.Join(source, filepath.FromSlash(dir)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"SOUL.md": "persona must stay here", "memories/MEMORY.md": "remember ~/.hermes/library", "memories/USER.md": "user uses $HERMES_HOME/work",
		"skills/writing/Report Kit/SKILL.md":        "---\nname: Report Kit\ndescription: Builds reports.\nmetadata:\n  hermes:\nrequired_environment_variables:\n  - REPORT_KEY\n---\nUse skill_view(report-kit), then memory(note).\n",
		"skills/writing/Report Kit/scripts/run.ps1": "Write-Output 'stub-ok'; Write-Output 'HERMES_HOME/assets'", ".env": "FIRST_KEY=" + planted + "\nSECOND_KEY=" + plantedTwo + "\n",
		"skills/clean/portable/SKILL.md": "---\nname: portable\ndescription: Portable fixture.\n---\nRead the request.\n",
		"config.yaml":                    "model: fixture", "auth.json": "{}",
	}
	for name, body := range files {
		path := filepath.Join(source, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Defaults(root)
	server := New(&cfg, filepath.Join(root, "harness.json"), t.TempDir(), RuntimeRoots{Data: root, Profile: profile}, events.NewBus())
	call := func(action string, include []string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"action": action, "path": source, "include": include})
		response := httptest.NewRecorder()
		server.skillsEndpoint(response, httptest.NewRequest(http.MethodPost, "/api/skills", strings.NewReader(string(body))))
		return response
	}
	preview := call("hermes-preview", nil)
	if preview.Code != 200 || strings.Contains(preview.Body.String(), planted) || strings.Contains(preview.Body.String(), plantedTwo) {
		t.Fatalf("preview=%d %s", preview.Code, preview.Body.String())
	}
	for _, text := range []string{"SOUL.md", "not imported", "MEMORY.md", "USER.md", "report-kit", "FIRST_KEY", "SECOND_KEY", "cron", "sessions"} {
		if !strings.Contains(preview.Body.String(), text) {
			t.Errorf("preview misses %q: %s", text, preview.Body.String())
		}
	}
	if entries, _ := os.ReadDir(profile); len(entries) != 0 {
		t.Fatalf("preview wrote profile: %v", entries)
	}
	imported := call("hermes-import", []string{"memory:MEMORY.md", "memory:USER.md", "skill:portable", "skill:report-kit"})
	if imported.Code != 200 || strings.Contains(imported.Body.String(), planted) || strings.Contains(imported.Body.String(), plantedTwo) {
		t.Fatalf("import=%d %s", imported.Code, imported.Body.String())
	}
	destination := filepath.Join(profile, "skills", "report-kit")
	skill, err := os.ReadFile(filepath.Join(destination, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(skill), "~/.hermes") || strings.Contains(string(skill), "HERMES_HOME") || !strings.Contains(string(skill), "name: report-kit") {
		t.Fatalf("skill was not portable: %s", skill)
	}
	if setting := cfg.Skills["report-kit"]; setting.Enabled || !strings.Contains(strings.Join(strings.Fields(imported.Body.String()), " "), "memory") {
		t.Fatalf("setting=%+v report=%s", setting, imported.Body.String())
	}
	if !cfg.Skills["portable"].Enabled || !strings.Contains(server.Index(), "portable | Portable fixture.") {
		t.Fatalf("portable skill did not arrive on: %+v index=%s", cfg.Skills["portable"], server.Index())
	}
	memoryPath := filepath.Join(profile, "memory", "agent-"+cfg.DefaultAgentID()+".md")
	memoryBody, err := os.ReadFile(memoryPath)
	if err != nil || strings.Contains(string(memoryBody), "HERMES_HOME") || !strings.Contains(string(memoryBody), "imported from Hermes") {
		t.Fatalf("memory=%q err=%v", memoryBody, err)
	}
	run := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-File", filepath.Join(destination, "scripts", "run.ps1"))
	run.Dir = destination
	renamed := source + "-renamed"
	if err := os.Rename(source, renamed); err != nil {
		t.Fatal(err)
	}
	output, err := run.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "stub-ok") || strings.Contains(string(output), "HERMES_HOME") {
		t.Fatalf("stub=%q err=%v", output, err)
	}
	if err := os.Rename(renamed, source); err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), skill...)
	if err := os.WriteFile(filepath.Join(destination, "SKILL.md"), append(before, []byte("\noperator edit\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	again := call("hermes-import", []string{"memory:MEMORY.md", "memory:USER.md", "skill:portable", "skill:report-kit"})
	after, _ := os.ReadFile(filepath.Join(destination, "SKILL.md"))
	if again.Code != 200 || !strings.Contains(again.Body.String(), "nothing changed") || !strings.Contains(string(after), "operator edit") {
		t.Fatalf("again=%d %s skill=%s", again.Code, again.Body.String(), after)
	}
	sourceEnv, _ := os.ReadFile(filepath.Join(source, ".env"))
	if string(sourceEnv) != files[".env"] {
		t.Fatal("source secret file changed")
	}
}

func TestProfileSwitchRefusesLiveRun(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(t.TempDir())
	path := filepath.Join(root, "harness.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	manager, _, err := profiles.Open(root, path, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Create("Second"); err != nil {
		t.Fatal(err)
	}
	writers, err := events.NewWriters(filepath.Join(manager.Root(manager.Active()), "logs"))
	if err != nil {
		t.Fatal(err)
	}
	defer writers.Close()
	bus := events.NewBus()
	server := New(&cfg, path, t.TempDir(), RuntimeRoots{Data: root, Profile: manager.Root(manager.Active())}, bus)
	server.SetProfiles(manager)
	registry := session.NewRegistry(bus, writers, server.Connection, cfg.Run.MaxTurns, server.ConfigSnapshot)
	server.SetRegistry(registry)
	item, err := registry.Create("live", cfg.DefaultAgentID(), "")
	if err != nil {
		t.Fatal(err)
	}
	item.Run.Status = "running"
	response := profileCall(t, server, `{"action":"switch","name":"Second"}`)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "stop the run first") {
		t.Fatalf("response: %d %s", response.Code, response.Body.String())
	}
}

func TestConfigUpdatePersistsAgentsInActiveProfile(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(t.TempDir())
	path := filepath.Join(root, "harness.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	manager, _, err := profiles.Open(root, path, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	first := manager.Active()
	if err := manager.Create("Second"); err != nil {
		t.Fatal(err)
	}
	server := New(&cfg, path, t.TempDir(), RuntimeRoots{Data: root, Profile: manager.Root(manager.Active())}, events.NewBus())
	server.SetProfiles(manager)
	agents := append([]config.Agent(nil), cfg.Agents...)
	agents[0].Name = "Persisted agent"
	body, err := json.Marshal(map[string]any{"agents": agents})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	authorizeMutation(request, server)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("config update: %d %s", response.Code, response.Body.String())
	}
	if got := profileCall(t, server, `{"action":"switch","name":"Second"}`); got.Code != http.StatusOK {
		t.Fatalf("switch second: %d %s", got.Code, got.Body.String())
	}
	if got := profileCall(t, server, `{"action":"switch","name":"`+first+`"}`); got.Code != http.StatusOK {
		t.Fatalf("switch back: %d %s", got.Code, got.Body.String())
	}
	if cfg.Agents[0].Name != "Persisted agent" {
		t.Fatalf("active profile agent was not persisted: %+v", cfg.Agents)
	}
}

func profileCall(t *testing.T, server *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/profiles", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	authorizeMutation(request, server)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code == http.StatusOK {
		var value map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
	}
	return response
}

// Item 2m0 (e) and its acceptance: two profiles, a write through each route,
// and the machine file's per-profile sections unchanged.
//
// This is the test rel-1.20.0 did not have, and the defect it would have caught
// is that EVERY writer of the machine file marshals the live configuration,
// which is the merge. A route that came to change a connection wrote whichever
// profile was active into the machine's defaults as a side effect.
func TestMachineFileKeepsItsOwnPerProfileSections2m0(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(t.TempDir())
	path := filepath.Join(root, "harness.json")
	cfg.Chat.TextSize = "" // the machine's default: unset
	cfg.Agents = []config.Agent{{Name: "Coder", B: "local", Toolset: config.FullToolset()}}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	manager, _, err := profiles.Open(root, path, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := New(&cfg, path, t.TempDir(), RuntimeRoots{Data: root, Profile: manager.Root(manager.Active())}, events.NewBus())
	server.SetProfiles(manager)

	// The active profile takes an opinion about a per-profile setting.
	cfg.Chat.TextSize = "large"
	if err := manager.SaveActive(); err != nil {
		t.Fatal(err)
	}

	// A MACHINE write, through the boundary every machine-scoped route now uses.
	// Nothing about it concerns typography.
	next := cfg
	next.Shell.AllowLocalNetwork = true
	if err := server.saveMachineConfig(next); err != nil {
		t.Fatal(err)
	}
	onDisk, _, _, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.Chat.TextSize != "" {
		t.Errorf("a machine write carried the profile's text size into the machine file: %q", onDisk.Chat.TextSize)
	}
	if !onDisk.Shell.AllowLocalNetwork {
		t.Error("the machine write did not land")
	}

	// A PER-PROFILE write, through the other boundary: it must land, and it must
	// land in the PROFILE rather than in the machine file. (c)'s rejected fix
	// failed exactly here -- it preserved the machine file's sections from disk
	// and dropped this write entirely.
	cfg.Agents[0].C = "local" // an existing connection; the point is the file it lands in
	if err := server.saveProfileConfig(cfg); err != nil {
		t.Fatal(err)
	}
	settings, err := manager.Settings(manager.Active())
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Agents) == 0 || settings.Agents[0].C != "local" {
		t.Errorf("the agent-role write did not reach the profile: %+v", settings.Agents)
	}
	if onDisk, _, _, err := config.Load(path); err != nil {
		t.Fatal(err)
	} else if onDisk.Chat.TextSize != "" {
		t.Errorf("the per-profile write carried typography into the machine file: %q", onDisk.Chat.TextSize)
	}

	// And a second profile still inherits the MACHINE's value rather than the
	// first profile's, which is the consequence all of this exists to protect.
	if err := manager.Create("Second"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Switch("Second"); err != nil {
		t.Fatal(err)
	}
	if cfg.Chat.TextSize != "" {
		t.Errorf("the second profile inherited the first profile's text size: %q", cfg.Chat.TextSize)
	}
}
