package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"harness/internal/agent"
	"harness/internal/buildinfo"
	"harness/internal/config"
	contextmgr "harness/internal/context"
	"harness/internal/credential"
	"harness/internal/delivery"
	"harness/internal/events"
	"harness/internal/hardening"
	"harness/internal/llm"
	"harness/internal/memory"
	"harness/internal/notifications"
	"harness/internal/operatorfiles"
	"harness/internal/progress"
	"harness/internal/projection"
	"harness/internal/serviceaccount"
	"harness/internal/session"
	"harness/internal/signing"
	"harness/internal/stats"
	"harness/internal/tools"
	"harness/internal/updater"
	webserver "harness/internal/web"
	workspaceinfo "harness/internal/workspace"
)

func main() {
	if err := startupElevationError(processIsElevated()); err != nil {
		log.Fatal(err)
	}
	// Item 2gm: where the two minutes go, measured on every start.
	phases := newStartupPhases()
	startupTimer = phases
	configOverride := flag.String("config", "", "configuration file (overrides AGENTB_CONFIG and installed/default locations)")
	applicationOverride := flag.String("app-root", "", "application root containing web, prompts, scripts, and harness.example.json")
	dataOverride := flag.String("data-root", "", "operator data root containing configuration, credentials, logs, and memory")
	replayPaths := flag.String("replay", "", "comma-separated session JSONL files to replay")
	startupLog := flag.String("startup-log", "", "optional append-only startup diagnostic log")
	version := flag.Bool("version", false, "print this build's identity as JSON and exit")
	// Item 2hc (v1.3.0/W2): our own window. One process and one binary still:
	// this is a mode of the server, not a second executable, and the browser
	// path stays supported for every case where the runtime is absent.
	window := flag.Bool("window", false, "host the page in Agent_b's own window (Windows, needs the WebView2 runtime)")
	// Item 2gl: install mode. Everything after --install is the installer's,
	// so it is taken before flag.Parse and passed through untouched — the
	// PowerShell installer's parameters stay its own and cannot drift from a
	// second copy of them here.
	install := flag.Bool("install", false, "install Agent_b from this folder; everything after it is passed to the installer")
	installQuiet := flag.Bool("quiet", false, "with --install: print the installer's output to this console (the suite's path)")
	installSource := flag.String("install-source", "", "with --install: the candidate folder to install from (default: this executable's folder)")
	installData := flag.String("install-data", "", "with --install: the operator data root that carries the marker and progress")
	passthrough := installPassthrough(os.Args[1:])
	if err := flag.CommandLine.Parse(installFlagArgs(os.Args[1:])); err != nil {
		log.Fatal(err)
	}
	if *install || setupExecutable(os.Args[0]) {
		os.Exit(runInstall(installOptions{
			quiet:     *installQuiet,
			sourceDir: *installSource,
			dataRoot:  *installData,
		}, passthrough))
	}
	if *version {
		if err := json.NewEncoder(os.Stdout).Encode(buildinfo.Current()); err != nil {
			log.Fatal(err)
		}
		return
	}
	if strings.TrimSpace(*startupLog) != "" {
		file, openErr := os.OpenFile(filepath.Clean(*startupLog), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if openErr != nil {
			log.Fatalf("open startup diagnostic log %s: %v", *startupLog, openErr)
		}
		log.SetOutput(io.MultiWriter(os.Stderr, file))
	}
	paths, err := resolveStartupPaths(*configOverride, *applicationOverride, *dataOverride)
	if err != nil {
		log.Fatal(err)
	}
	// Item 2gl: an install that did not finish says so, once, at the next
	// launch — the operator asked "should i re-run?" and nothing could answer
	// him. The marker is only ever cleared by an install that completed.
	if marker, found, markerErr := readInstallMarker(paths.Data); markerErr != nil {
		log.Printf("install marker: %v", markerErr)
	} else if found {
		log.Printf("INTERRUPTED INSTALL: %s", describeInterruptedInstall(marker))
	}
	phases.mark("config")
	cfg, migrated, created, err := config.LoadWithRoots(paths.Config, filepath.Join(paths.Application, "harness.example.json"), paths.Data)
	if err != nil {
		log.Fatal(err)
	}
	workspaceRoot, err := filepath.Abs(cfg.Workspace)
	if err != nil {
		log.Fatal(err)
	}
	paths.Workspace = filepath.Clean(workspaceRoot)
	roots := webserver.RuntimeRoots{Application: paths.Application, Data: paths.Data, Workspace: paths.Workspace}
	if created {
		log.Printf("created %s from %s - set servers[0].base_url and model", paths.Config, filepath.Join(paths.Application, "harness.example.json"))
	}
	if migrated {
		log.Printf("migrated %s to config schema %d", filepath.Base(paths.Config), config.CurrentConfigVersion)
	}
	for _, notice := range cfg.LoadNotices {
		log.Printf("config migration: %s", notice)
	}
	facts := readServingFacts(filepath.Join(paths.Application, "SERVING.md"))
	if !facts.Complete {
		log.Printf("debug: SERVING.md missing or partial; skipping /tokenize latency hint")
	} else if facts.TokenizeBlocksOnSlot == "yes" {
		log.Printf("tokenize blocks on the generation slot (%d ms measured busy); context.accounting: \"estimated\" avoids it", facts.TokenizeBusyMS)
	}
	if *window {
		hostWindowMode = true
		// The WebView2 user-data folder holds cache, cookies and crash dumps.
		// It belongs in the operator's data root: the application directory is
		// deliberately not writable by the running identity, and the workspace
		// is reachable by the model. Leaving it unset would default it beside
		// the executable, which IS the application directory - not neutral,
		// just the wrong answer chosen by omission.
		//
		// Set HERE, before the replay and first-run paths, because both of
		// them serve and return: setting it beside the last serve call gave a
		// first-run install no window at all, which is the one case where the
		// operator has never seen the product before.
		hostWindowUserData = filepath.Join(paths.Data, "webview2")
	}
	if strings.TrimSpace(*replayPaths) != "" {
		replay, loadErr := projection.LoadReplay(strings.Split(*replayPaths, ","))
		if loadErr != nil {
			log.Fatal(loadErr)
		}
		web := webserver.New(cfg, paths.Config, filepath.Join(paths.Application, "web"), roots, events.NewBus())
		web.SetReplay(replay)
		web.SetSigningManager(signing.New(filepath.Join(paths.Application, "scripts", "manage-signing.ps1")))
		signingContext, cancelSigning := context.WithTimeout(context.Background(), 15*time.Second)
		if err := web.RefreshSigningState(signingContext); err != nil {
			log.Printf("inspect installed signatures: %v", err)
		}
		cancelSigning()
		if err := serve(cfg, web.Handler(), nil); err != nil {
			log.Fatal(err)
		}
		return
	}
	logDir := cfg.LogDir
	if !filepath.IsAbs(logDir) {
		logDir = filepath.Join(paths.Data, logDir)
	}
	writers, err := events.NewWriters(logDir)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := writers.Close(); err != nil {
			log.Printf("close event logs: %v", err)
		}
	}()
	bus := events.NewBus()
	projector := projection.NewStore()
	bus.SetDurableSink(writers.WriteRecord, projector.Apply, projector.MarkStale)
	progressManager := progress.New(bus)
	progressManager.Start()
	defer progressManager.Close()
	web := webserver.New(cfg, paths.Config, filepath.Join(paths.Application, "web"), roots, bus)
	updateManager := updater.New(updater.Options{
		CurrentVersion: buildinfo.Current().Tag,
		DataRoot:       paths.Data,
		LatestURL:      updateLatestURL(),
		Enabled:        func() bool { return web.ConfigSnapshot().Updates.AutoCheck },
		Changed:        func(state updater.State) { bus.Publish(events.New(events.UpdateChanged, "", "", state)) },
	})
	web.SetUpdater(updateManager)
	updateManager.Start(context.Background())
	defer updateManager.Close()
	web.PublishPlanChanges()
	web.SetProjection(projector, writers)
	for _, notice := range cfg.LoadNotices {
		bus.Publish(events.New(events.ConfigChanged, "", "", map[string]any{"config": cfg.Masked(), "notice": notice}))
	}
	memoryManager := memory.New(paths.Data, web.ConfigSnapshot, func(ctx context.Context, serverID, text string) (int, error) {
		profile, ok := web.Profile(serverID)
		if !ok || !profile.Capabilities.Tokenize {
			return 0, fmt.Errorf("tokenizer unavailable")
		}
		return llm.New(profile).Tokenize(ctx, text, false)
	})
	workspaceManager := workspaceinfo.New(memoryManager.Dir(), memoryManager.Path)
	registry := session.NewRegistry(bus, writers, web.Profile, cfg.Run.MaxTurns, web.ConfigSnapshot)
	registry.SetMemoryLoader(memoryManager.Load)
	registry.SetAgentMemoryLoader(memoryManager.LoadAgent)
	registry.SetWorkspaceManager(workspaceManager)
	web.SetRegistry(registry)
	notificationStore, err := credential.NewNamed(paths.Data, cfg.Notifications.DiscordCredential)
	if err != nil {
		log.Fatal(err)
	}
	notificationManager := notifications.New(bus, registry.Label, "http://"+cfg.Listen)
	if value, readErr := notificationStore.Read(); readErr == nil {
		if configureErr := notificationManager.Configure(string(value)); configureErr != nil {
			log.Printf("Discord notification credential is invalid; notifications disabled: %v", configureErr)
		}
	} else if !errors.Is(readErr, credential.ErrNotStored) {
		log.Printf("load Discord notification credential: %v", readErr)
	}
	notificationManager.Start(context.Background())
	defer notificationManager.Close()
	web.SetNotifications(notificationManager, notificationStore)
	web.SetWorkspaceState(workspaceManager, memoryManager)
	operatorFiles := operatorfiles.New(paths.Data, logDir, web.ConfigSnapshot)
	operatorFiles.SetEventPublisher(func(event events.Event) { bus.Publish(event) })
	if err := operatorFiles.Ensure(); err != nil {
		log.Fatal(err)
	}
	if _, err := operatorFiles.ApplyRetention(); err != nil {
		log.Printf("apply log retention: %v", err)
	}
	web.SetOperatorFiles(operatorFiles)
	// Item 17-i: reflection runs after a run closes and on a daily tick. It
	// subscribes to the bus, so it is never in a run's path.
	web.StartReflection(24 * time.Hour)
	defer web.StopReflection()
	operatorContext, cancelOperatorFiles := context.WithCancel(context.Background())
	defer cancelOperatorFiles()
	go operatorFiles.RunRetention(operatorContext)
	operatorEvents, unsubscribeOperatorEvents := bus.Subscribe()
	defer unsubscribeOperatorEvents()
	go func() {
		for event := range operatorEvents {
			var snapshot *session.Snapshot
			if item, ok := registry.Get(event.SessionID); ok {
				value := item.Snapshot()
				snapshot = &value
			}
			operatorFiles.HandleEvent(event, snapshot)
		}
	}()
	statsManager := stats.New(paths.Data, registry, bus)
	web.SetStats(statsManager)
	renderer, err := agent.LoadTemplate(filepath.Join(paths.Application, "prompts", "system.md"))
	if err != nil {
		log.Fatal(err)
	}
	if err := renderer.LoadPlanner(filepath.Join(paths.Application, "prompts", "planner.md")); err != nil {
		log.Fatal(err)
	}

	// The worker brief is optional in the same way the planner's is: an install
	// without it simply has no worker, rather than refusing to start.
	if err := renderer.LoadWorker(filepath.Join(paths.Application, "prompts", "worker.md")); err != nil {
		log.Printf("debug: %v", err)
	}
	workspaces := session.NewWorkspaceRegistry()
	coordinator := tools.NewFileCoordinator(workspaces, registry.Label, bus)
	credentialStore := credential.New(paths.Data)
	fileIdentity := tools.NewFileIdentity(credentialStore)
	fileIdentity.Configure(*cfg)
	shellTool := tools.NewShell(cfg.Shell)
	shellTool.SetFileCoordinator(coordinator)
	shellTool.Configure(*cfg)
	shellTool.SetCredentialStore(credentialStore)
	shellTool.SetIdentityReporter(func(status tools.ShellIdentityStatus) {
		bus.Publish(events.New(events.ShellIdentity, "", "", status))
	})
	web.SetShellSecurity(credentialStore, shellTool)
	web.SetServiceAccountManager(serviceaccount.New(filepath.Join(paths.Application, "scripts", "setup-service-account.ps1")))
	web.SetHardeningManager(hardening.New(
		filepath.Join(paths.Application, "scripts", "apply-acls.ps1"),
		filepath.Join(paths.Application, "scripts", "apply-firewall-rule.ps1"),
		filepath.Join(paths.Application, "scripts", "apply-hardening.ps1"),
	))
	web.SetSigningManager(signing.New(filepath.Join(paths.Application, "scripts", "manage-signing.ps1")))
	// Item 2gm: inspecting the installed signatures runs PowerShell and used to
	// hold the port closed for seconds — on a fresh root with no chats at all
	// it was most of the 8.1 s before listen. It is not needed to serve a
	// request: Settings reads the state when it is ready, the same treatment
	// the probes get. Nothing waits on it, and a failure is still said.
	go func() {
		signingContext, cancelSigning := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelSigning()
		if err := web.RefreshSigningState(signingContext); err != nil {
			log.Printf("inspect installed signatures: %v", err)
		}
	}()
	// Item 2ho (v1.6.0): web_search follows fetch_url. It shares that tool's
	// guarded uTLS transport, so the older tools retain their relative order.
	fetchTool := tools.NewFetch(cfg.Tools.Fetch)
	toolRegistry := tools.New(
		fileIdentity.Wrap(tools.NewReadFile(cfg.Tools.ReadFile)),
		fileIdentity.Wrap(tools.NewListDir(cfg.Tools.ListDir)),
		fileIdentity.Wrap(tools.NewWriteFile(coordinator)),
		fileIdentity.Wrap(tools.NewEditFile(coordinator)),
		tools.NewSearch(
			fileIdentity.Wrap(tools.NewGrep(cfg.Tools.Grep, cfg.Tools.ListDir)),
			fileIdentity.Wrap(tools.NewGlob(cfg.Tools.FindFiles)),
		),
		shellTool,
		tools.NewRemember(memoryManager, bus),
		tools.NewRecall(memoryManager),
		fetchTool,
		tools.NewWebSearch(fetchTool, cfg.Tools.WebSearch),
		tools.NewRunScript(shellTool),
		tools.NewCallService(cfg.Services),
	)
	// Item 2ch (v1.2.5): the threshold under which a PDF is sent inline rather
	// than read from its extracted text.
	agent.SetInlineDocumentLimit(cfg.Tools.Attachments.InlineDocumentLimit())
	runner := agent.NewRunner(bus, toolRegistry, renderer, web.Profile, web.ConfigSnapshot)
	runner.SetSessionRenamer(registry.RenameBy)
	deliveryManager := delivery.New(bus, web.ConfigSnapshot)
	runner.SetDeliverer(func(item *session.Session, runID string, files []delivery.Source) delivery.Result {
		return deliveryManager.Deliver(item, runID, files)
	})
	scheduler := agent.NewScheduler(runner, registry, bus, web.ConfigSnapshot)
	runner.SetMailboxBoundary(func(_ context.Context, sessionID string, approvalPending bool) agent.BoundaryAction {
		action, err := operatorFiles.CheckInbox(sessionID, approvalPending)
		return agent.BoundaryAction{Stop: action.Stop, Revision: action.Revision, Delay: action.Delay, Err: err}
	})
	runner.Gate().SetMailboxDecision(func(sessionID string) (string, error) {
		action, err := operatorFiles.CheckInbox(sessionID, true)
		if action.Stop {
			scheduler.Stop(sessionID, false)
			return "deny", err
		}
		return action.Decision, err
	})
	web.SetRuntime(scheduler, runner, renderer)
	if len(cfg.Servers) == 0 {
		log.Printf("first-run setup required: no model profiles are configured")
	} else {
		mainAgentID := cfg.DefaultAgentID()
		if ready, reason := registry.AgentRunnable(mainAgentID); ready {
			log.Printf("startup agent %s ready from saved capabilities", mainAgentID)
		} else {
			log.Printf("startup agent %s not runnable: %s; use Connections > Test", mainAgentID, reason)
		}
		phases.mark("before restore")
		restored, floor, restoreErr := restoreRetainedChats(writers, registry, bus, retainedIDFloor(writers))
		if restoreErr != nil {
			log.Fatal(restoreErr)
		}
		phases.mark("restore retained chats")
		runner.ReserveIDs(floor)
		scheduler.ReserveIDs(floor)
		open := false
		for _, item := range restored {
			if !item.IsClosed() {
				open = true
			}
		}
		if !open {
			mainSession, createErr := registry.Create("main", mainAgentID, "")
			if createErr != nil {
				log.Fatal(createErr)
			}
			restored = append(restored, mainSession)
		}
		registry.RefreshRunnable()
		for _, item := range restored {
			if !item.IsClosed() {
				runner.PublishBudget(context.Background(), item)
			}
		}
	}
	publishPendingSigning(paths.Data, registry, bus)
	if err := serve(cfg, web.Handler(), newLifetime(paths.Data, time.Now)); err != nil {
		log.Fatal(err)
	}
}

func setupExecutable(path string) bool {
	return strings.EqualFold(filepath.Base(path), "Agent_b-setup.exe")
}

// AGENTB_UPDATE_FIXTURE_URL is an acceptance-only seam. It is deliberately
// limited to loopback so an inherited environment cannot redirect the product's
// release trust path to another public host.
func updateLatestURL() string {
	raw := strings.TrimSpace(os.Getenv("AGENTB_UPDATE_FIXTURE_URL"))
	if raw == "" {
		return updater.LatestReleaseURL
	}
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Scheme != "http" || endpoint.User != nil || endpoint.Fragment != "" {
		return updater.LatestReleaseURL
	}
	host := net.ParseIP(endpoint.Hostname())
	if host == nil || !host.IsLoopback() {
		return updater.LatestReleaseURL
	}
	return endpoint.String()
}

// retainedIDFloor is the highest numeric id suffix a restored chat holds in a
// message id or a run id. Message and run ids come from counters that start at
// 1 in every process; without this floor the first run after a restart reused
// r1 and m-1 inside a chat that already had them (item 2es).
func retainedIDFloor(writers *events.Writers) int64 {
	paths, err := writers.DurableChatPaths()
	if err != nil || len(paths) == 0 {
		return 0
	}
	startupTimer.mark("list journals")
	// Item 2gm: the restore wants the sessions, not the patches.
	replay, err := projection.LoadReplayStates(paths)
	if err != nil {
		return 0
	}
	var floor int64
	note := func(id string) {
		end := len(id)
		start := end
		for start > 0 && id[start-1] >= '0' && id[start-1] <= '9' {
			start--
		}
		if start == end {
			return
		}
		if value, parseErr := strconv.ParseInt(id[start:end], 10, 64); parseErr == nil && value > floor {
			floor = value
		}
	}
	for _, snapshot := range replay.Sessions {
		for _, message := range snapshot.Messages {
			note(message.ID)
		}
		for _, entry := range snapshot.Chat {
			note(entry.RunID)
		}
		for _, event := range snapshot.Timeline {
			note(event.RunID)
		}
	}
	return floor
}

// restoreRetainedChats restores every retained chat and returns the id floor
// past everything it holds, including ids it re-minted (item 2fd rule 7).
func restoreRetainedChats(writers *events.Writers, registry *session.Registry, bus *events.Bus, floor int64) ([]*session.Session, int64, error) {
	paths, err := writers.DurableChatPaths()
	if err != nil {
		return nil, floor, err
	}
	if len(paths) == 0 {
		paths, err = writers.LatestOperationalSessionPaths()
		if err != nil {
			return nil, floor, err
		}
	}
	if len(paths) == 0 {
		return nil, floor, nil
	}
	replay, err := projection.LoadReplay(paths)
	if err != nil {
		return nil, floor, fmt.Errorf("load retained chats: %w", err)
	}
	startupTimer.mark("parse and project journals")
	ids := make([]string, 0, len(replay.Sessions))
	for id := range replay.Sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]*session.Session, 0, len(ids))
	for _, id := range ids {
		encoded, marshalErr := json.Marshal(replay.Sessions[id])
		if marshalErr != nil {
			return nil, floor, marshalErr
		}
		var saved session.Snapshot
		if unmarshalErr := json.Unmarshal(encoded, &saved); unmarshalErr != nil {
			return nil, floor, unmarshalErr
		}
		// Item 2et, anchored by item 2fd rule 3: results older than the newest
		// user message the journal names — surviving or folded — come back as
		// their elision stubs; the JSONL keeps the bytes.
		saved.Messages = contextmgr.StubResultsBefore(saved.Messages, newestRunUserMessage(replay.Sessions[id].Timeline), config.Defaults("").Tools.ReadFile.DefaultLimit)
		// Item 2fd rule 7: a repeated id gets a new one once, with a journal note.
		var reminted []contextmgr.Remint
		saved.Messages, reminted, floor = contextmgr.RemintDuplicateIDs(saved.Messages, floor)
		item, restoreErr := registry.RestoreWithTranscript(saved, replay.Sessions[id].Chat)
		if restoreErr != nil {
			// Item 2gg (v1.1.2/W4): one chat that cannot be restored is not a
			// reason to refuse to start. It used to return here and the caller
			// called log.Fatal, so a single retained chat naming an agent the
			// operator had since renamed or removed took the whole application
			// down and every other chat with it. The chat is left on disk
			// untouched — the journal is the record and nothing deletes it —
			// and its loss is said out loud rather than swallowed.
			log.Printf("retained chat %s was not restored and was left on disk: %v", id, restoreErr)
			bus.Publish(events.New(events.SessionRestoreFailed, id, "", map[string]any{"session_id": id, "error": restoreErr.Error()}))
			continue
		}
		if len(reminted) > 0 {
			changes := make([]any, 0, len(reminted))
			for _, change := range reminted {
				changes = append(changes, map[string]any{"index": change.Index, "from": change.From, "to": change.To})
			}
			bus.Publish(events.New(events.MessagesReminted, item.ID, "", map[string]any{"reminted": changes}))
		}
		result = append(result, item)
	}
	startupTimer.mark("register restored sessions")
	return result, floor, nil
}

// newestRunUserMessage is the user message id of the newest run.started in a
// chat's journal, or "" when it has none.
func newestRunUserMessage(timeline []events.Event) string {
	for index := len(timeline) - 1; index >= 0; index-- {
		if timeline[index].Type != events.RunStarted {
			continue
		}
		if data, ok := timeline[index].Data.(map[string]any); ok {
			if id, ok := data["user_message_id"].(string); ok && id != "" {
				return id
			}
		}
	}
	return ""
}

func publishPendingSigning(dataRoot string, registry *session.Registry, bus *events.Bus) {
	path := filepath.Join(dataRoot, "signing-applied.pending.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		log.Printf("read pending signing event: %v", err)
		return
	}
	var detail map[string]any
	if err := json.Unmarshal(data, &detail); err != nil {
		log.Printf("decode pending signing event: %v", err)
		return
	}
	bus.Publish(events.New(events.SigningApplied, "", "", detail))
	for _, item := range registry.List() {
		bus.Publish(events.New(events.SigningApplied, item.ID, "", detail))
	}
	if err := os.Remove(path); err != nil {
		log.Printf("remove pending signing event: %v", err)
	}
}

type startupPaths struct {
	Application string
	Data        string
	Workspace   string
	Config      string
}

func resolveStartupPaths(configOverride, applicationOverride, dataOverride string) (startupPaths, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return startupPaths{}, fmt.Errorf("resolve working directory: %w", err)
	}
	// Item 2gw (v1.2.5): the binary's OWN files - web, prompts, scripts, the
	// example config - live beside the binary, not beside whatever directory a
	// shell happened to be in. Resolving them against the working directory
	// meant the exe started from C:\ or from Explorer looked for its own assets
	// in C:\web and found nothing; the launcher worked only because it set the
	// working directory first. The data root and the workspace are unchanged:
	// those are the operator's and are config-driven.
	application := applicationOverride
	if application == "" {
		if executable, execErr := os.Executable(); execErr == nil {
			if resolved, linkErr := filepath.EvalSymlinks(executable); linkErr == nil {
				executable = resolved
			}
			application = filepath.Dir(executable)
		} else {
			application = cwd
		}
	}
	application, err = filepath.Abs(application)
	if err != nil {
		return startupPaths{}, fmt.Errorf("resolve application root: %w", err)
	}

	localData := ""
	if base := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); base != "" {
		localData = filepath.Join(base, "Agent_b")
	}
	configPath := strings.TrimSpace(configOverride)
	data := strings.TrimSpace(dataOverride)
	if configPath == "" {
		configPath = strings.TrimSpace(os.Getenv("AGENTB_CONFIG"))
	}
	if configPath != "" && data == "" {
		if localData != "" {
			data = localData
		} else {
			data = cwd
		}
	}
	if configPath == "" && localData != "" {
		candidate := filepath.Join(localData, "harness.json")
		if _, statErr := os.Stat(candidate); statErr == nil {
			configPath = candidate
			if data == "" {
				data = localData
			}
		} else if !os.IsNotExist(statErr) {
			return startupPaths{}, fmt.Errorf("inspect installed configuration: %w", statErr)
		}
	}
	if configPath == "" {
		configPath = filepath.Join(cwd, "harness.json")
		if data == "" {
			data = cwd
		}
	}
	configPath, err = filepath.Abs(configPath)
	if err != nil {
		return startupPaths{}, fmt.Errorf("resolve configuration path: %w", err)
	}
	data, err = filepath.Abs(data)
	if err != nil {
		return startupPaths{}, fmt.Errorf("resolve data root: %w", err)
	}
	return startupPaths{Application: filepath.Clean(application), Data: filepath.Clean(data), Config: filepath.Clean(configPath)}, nil
}

func startupElevationError(elevated bool) error {
	if !elevated {
		return nil
	}
	return fmt.Errorf("SECURITY: Agent_b refuses to run with an elevated Administrator token; local administrators can launch it normally by double-clicking start-Agent_b.cmd in File Explorer (do not use Run as administrator)")
}

// serve binds, then records the process lifetime (when life is non-nil) from
// the moment the listener exists until the server stops, whatever stops it.
func serve(cfg *config.Config, handler http.Handler, life *lifetime) error {
	httpServer := &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	stopped := func(string) {}
	closeRequests := make(chan struct{}, 1)
	if life != nil {
		life.begin()
		watchSessionEnd(life.stopped, func() {
			select {
			case closeRequests <- struct{}{}:
			default:
			}
		})
		stopped = life.stopped
	}
	if hostWindowMode {
		startHostWindow(cfg.Listen, closeRequests)
	}
	errors := make(chan error, 1)
	go func() {
		log.Printf("Agent_b listening on http://%s", cfg.Listen)
		startupTimer.report()
		errors <- httpServer.Serve(listener)
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	select {
	case sig := <-signals:
		log.Printf("stopping on %s", sig)
		stopped(fmt.Sprintf("signal %s", sig))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(ctx)
	case <-closeRequests:
		log.Printf("stopping on a close request")
		stopped("asked to close (the installer's graceful stop)")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(ctx)
	case err := <-errors:
		if err != nil && err != http.ErrServerClosed {
			stopped(fmt.Sprintf("the HTTP server failed: %v", err))
			return err
		}
		stopped("the HTTP server closed")
		return nil
	}
}

type servingFacts struct {
	TokenizeIdleMS       int
	TokenizeBusyMS       int
	TokenizeBlocksOnSlot string
	Complete             bool
}

// readServingFacts reads only the accounting facts needed at startup. Missing or
// malformed values remain zero-valued so a documentation issue cannot stop Agent_b.
func readServingFacts(path string) servingFacts {
	file, err := os.Open(path)
	if err != nil {
		return servingFacts{}
	}
	defer file.Close()

	var facts servingFacts
	var idleFound, busyFound, blocksFound bool
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, found := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if !found {
			continue
		}
		switch key {
		case "tokenize_idle_ms":
			facts.TokenizeIdleMS, err = strconv.Atoi(strings.TrimSpace(value))
			idleFound = err == nil
		case "tokenize_busy_ms":
			facts.TokenizeBusyMS, err = strconv.Atoi(strings.TrimSpace(value))
			busyFound = err == nil
		case "tokenize_blocks_on_slot":
			facts.TokenizeBlocksOnSlot = strings.ToLower(strings.TrimSpace(value))
			blocksFound = facts.TokenizeBlocksOnSlot == "yes" || facts.TokenizeBlocksOnSlot == "no" || facts.TokenizeBlocksOnSlot == "partial"
		}
	}
	facts.Complete = scanner.Err() == nil && idleFound && busyFound && blocksFound
	return facts
}

// Item 2hc (v1.3.0/W2): the host window, started beside the server in the same
// process. It runs on its own OS thread because a window and its message pump
// belong to one thread, and closing it asks for the same graceful stop the
// installer's channel uses, so every exit still records its reason.
//
// A missing or unusable WebView2 runtime is NOT a failure: the server keeps
// serving, the reason is logged, and the browser window remains the way in.
var (
	hostWindowMode     bool
	hostWindowUserData string
)

func startHostWindow(listen string, closeRequests chan struct{}) {
	go func() {
		url := "http://" + listen + "/chat"
		if version, err := hostWindowAvailable(); err != nil {
			log.Printf("host window: unavailable, using the browser instead (%v)", err)
			return
		} else {
			log.Printf("host window: WebView2 runtime %s", version)
		}
		if err := runHostWindow(url, hostWindowUserData, "Agent_b"); err != nil {
			log.Printf("host window: could not open, using the browser instead (%v)", err)
			return
		}
		log.Printf("host window: closed")
		select {
		case closeRequests <- struct{}{}:
		default:
		}
	}()
}
