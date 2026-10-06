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
	"harness/internal/chatstore"
	"harness/internal/config"
	"harness/internal/cron"
	contextmgr "harness/internal/context"
	"harness/internal/credential"
	"harness/internal/delivery"
	"harness/internal/entra"
	"harness/internal/events"
	"harness/internal/hardening"
	"harness/internal/llm"
	"harness/internal/memory"
	"harness/internal/notifications"
	"harness/internal/operatorfiles"
	"harness/internal/profiles"
	"harness/internal/progress"
	"harness/internal/projection"
	"harness/internal/recorder"
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
	if err := startupElevationError(processIsElevated() && !allUsersInstallRequested(os.Args[0], os.Args[1:])); err != nil {
		log.Fatal(err)
	}
	// Item 2gm: where the two minutes go, measured on every start.
	phases := newStartupPhases()
	startupTimer = phases
	configOverride := flag.String("config", "", "configuration file (overrides AGENTB_CONFIG and installed/default locations)")
	applicationOverride := flag.String("app-root", "", "application root containing web, prompts, scripts, and harness.example.json")
	dataOverride := flag.String("data-root", "", "user data root containing configuration, credentials, logs, and memory")
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
	installData := flag.String("install-data", "", "with --install: the user data root that carries the marker and progress")
	installAllUsers := flag.Bool("all-users", false, "with --install: install machine-wide under Program Files (requires elevation)")
	reopenSession := flag.String("reopen-session", "", "with --install: reopen this chat after the installed app starts")
	noStart := flag.Bool("NoStart", false, "with --install: install without starting Agent_b")
	uninstall := flag.Bool("uninstall", false, "remove the per-user Agent_b installation")
	uninstallWorker := flag.Bool("uninstall-worker", false, "complete a native uninstall after the installed process exits")
	uninstallParent := flag.Int("uninstall-parent", 0, "parent process to await before native uninstall")
	uninstallRegistry := flag.String("uninstall-registry-path", "", "with --uninstall: exact per-user registration to remove")
	startMenuRoot := flag.String("start-menu-root", "", "with --uninstall: exact Start-menu root used by the install")
	sendToRoot := flag.String("send-to-root", "", "with --uninstall: exact SendTo root used by the install")
	purgeData := flag.Bool("purge-data", false, "with --uninstall: remove user data")
	serviceHelper := flag.String("service-helper", "", "elevated native service-identity request")
	serviceResult := flag.String("service-result", "", "elevated native service-identity result")
	passthrough := installPassthrough(os.Args[1:])
	if err := flag.CommandLine.Parse(installFlagArgs(os.Args[1:])); err != nil {
		log.Fatal(err)
	}
	if *install || setupExecutable(os.Args[0]) {
		os.Exit(runInstall(installOptions{
			quiet:         *installQuiet,
			sourceDir:     *installSource,
			dataRoot:      *installData,
			noStart:       *noStart,
			allUsers:      *installAllUsers,
			reopenSession: *reopenSession,
		}, passthrough))
	}
	if *uninstall || *uninstallWorker {
		if err := runNativeUninstall(*applicationOverride, *dataOverride, *startMenuRoot, *sendToRoot, *uninstallRegistry, *purgeData, *uninstallWorker, *uninstallParent); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *serviceHelper != "" {
		if err := runServiceHelper(*serviceHelper, *serviceResult); err != nil {
			log.Fatal(err)
		}
		return
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
		// A GUI-subsystem launch has no stderr handle. Put the durable file first:
		// io.MultiWriter stops at the first failed writer, and stderr-first made a
		// startup failure's diagnostic file empty when no console existed.
		log.SetOutput(io.MultiWriter(file, os.Stderr))
	}
	paths, err := resolveStartupPaths(*configOverride, *applicationOverride, *dataOverride)
	if err != nil {
		log.Fatal(err)
	}
	// Item 2mt (a): before anything that can fault. The data root is the first
	// thing a crash record needs and this is the first line that knows it.
	build := buildinfo.Current()
	installCrashRecord(paths.Data, map[string]any{"tag": build.Tag, "commit": build.Commit, "dirty": build.Dirty, "executable_sha256": build.ExecutableSHA256}, *window)
	// Item 2hx: an installed launch is one application, not a race to bind the
	// same port. The serving process's marker names its install root and port;
	// a second window launch validates both, asks that exact process to foreground
	// its window, writes one launcher line, and exits before config work or a bind.
	if *window && strings.TrimSpace(*replayPaths) == "" {
		if pid, activated := activateExistingInstance(paths.Data, paths.Application); activated {
			message := fmt.Sprintf("Agent_b activated existing PID %d and exited; no new server started.", pid)
			appendLauncherMessage(paths.Data, message)
			log.Print(message)
			return
		}
	}
	// Item 2gl: an install that did not finish says so, once, at the next
	// launch — the user asked "should i re-run?" and nothing could answer
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
	// Item 2nv (h): every static_bearer connector moves into the credential store here,
	// once, before anything can use one. A connector whose variable is empty or whose
	// value does not verify is left unauthenticated and says so; nothing falls back to
	// the environment.
	if notices := config.MigrateConnectorCredentials(cfg, paths.Data); len(notices) > 0 {
		for _, notice := range notices {
			log.Printf("credentials: %s", notice)
		}
		if err := cfg.Save(paths.Config); err != nil {
			log.Printf("credentials: the migrated configuration could not be saved: %v", err)
		}
	}
	profileManager, profileMigrated, err := profiles.Open(paths.Data, paths.Config, cfg)
	if err != nil {
		log.Fatal(err)
	}
	profileRoot := profileManager.Root(profileManager.Active())
	workspacePath := cfg.Workspace
	if !filepath.IsAbs(workspacePath) {
		workspacePath = filepath.Join(profileRoot, workspacePath)
	}
	workspaceRoot, err := filepath.Abs(workspacePath)
	if err != nil {
		log.Fatal(err)
	}
	paths.Workspace = filepath.Clean(workspaceRoot)
	roots := webserver.RuntimeRoots{Application: paths.Application, Data: paths.Data, Profile: profileRoot, Workspace: paths.Workspace}
	if created {
		log.Printf("created %s from %s - set connections[0].base_url and model", paths.Config, filepath.Join(paths.Application, "harness.example.json"))
	}
	if migrated {
		log.Printf("migrated %s to config schema %d", filepath.Base(paths.Config), config.CurrentConfigVersion)
	}
	if profileMigrated {
		log.Printf("migrated operator data into profile %s", profileManager.Active())
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
	// Item 2o0: every server can host its window, not only one started with
	// -window, because a later launch may ask a background server for it.
	hostWindowMode = *window
	hostWindowDataRoot = paths.Data
	{
		// The WebView2 user-data folder holds cache, cookies and crash dumps.
		// It belongs in the user's data root: the application directory is
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
		web.SetProfiles(profileManager)
		web.SetReplay(replay)
		web.SetSigningManager(signing.New(filepath.Join(paths.Application, "scripts", "manage-signing.ps1")))
		signingContext, cancelSigning := context.WithTimeout(context.Background(), 15*time.Second)
		if err := web.RefreshSigningState(signingContext); err != nil {
			log.Printf("inspect installed signatures: %v", err)
		}
		cancelSigning()
		if err := serve(cfg, web.Handler(), nil, "", web.BrowserBootstrapToken()); err != nil {
			log.Fatal(err)
		}
		return
	}
	logDir := cfg.LogDir
	if !filepath.IsAbs(logDir) {
		logDir = filepath.Join(profileRoot, logDir)
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
	// Item 2ji (a): every run already journals its timings; nothing added them up.
	// The tally folds the run's own events and puts the buckets and the
	// reliability counters onto run.stopped, before the journal is written, so the
	// chat line, Activity and the telemetry receiver all read the same numbers
	// from the same place and cannot disagree.
	stats.InstallRunTally(bus)
	projector := projection.NewStore()
	bus.SetDurableSink(writers.WriteRecord, projector.Apply, projector.MarkStale)
	bus.SetTransientSink(projector.ApplyTransient)
	progressManager := progress.New(bus)
	progressManager.Start()
	defer progressManager.Close()
	web := webserver.New(cfg, paths.Config, filepath.Join(paths.Application, "web"), roots, bus)
	web.SetProfiles(profileManager)
	if *window {
		web.SetHostWindowAction(requestHostWindowAction)
	}
	updateManager := updater.New(updater.Options{
		CurrentVersion: buildinfo.Current().Tag,
		DataRoot:       paths.Data,
		// Item 2lh: this instance updates where it lives, not where the operator
		// per-user install happens to be.
		ApplicationRoot: paths.Application,
		WorkspaceRoot:   paths.Workspace,
		LatestURL:       updateLatestURL(),
		Enabled:         func() bool { return web.ConfigSnapshot().Updates.AutoCheck },
		Changed:         func(state updater.State) { bus.Publish(events.New(events.UpdateChanged, "", "", state)) },
	})
	web.SetUpdater(updateManager)
	updateManager.Start(context.Background())
	defer updateManager.Close()
	web.PublishPlanChanges()
	web.SetProjection(projector, writers)
	for _, notice := range cfg.LoadNotices {
		bus.Publish(events.New(events.ConfigChanged, "", "", map[string]any{"config": cfg.Masked(), "notice": notice}))
	}
	memoryManager := memory.New(profileRoot, web.ConfigSnapshot, func(ctx context.Context, connectionID, text string) (int, error) {
		connection, ok := web.Connection(connectionID)
		if !ok || !connection.Capabilities.Tokenize {
			return 0, fmt.Errorf("tokenizer unavailable")
		}
		return llm.New(connection).Tokenize(ctx, text, false)
	})
	workspaceManager := workspaceinfo.New(memoryManager.Dir(), memoryManager.Path)
	registry := session.NewRegistry(bus, writers, web.Connection, cfg.Run.MaxTurns, web.ConfigSnapshot)
	registry.SetMemoryLoader(memoryManager.Load)
	registry.SetAgentMemoryLoader(memoryManager.LoadAgent)
	registry.SetMachineMemoryLoader(memoryManager.LoadMachine)
	registry.SetWorkspaceManager(workspaceManager)
	web.SetRegistry(registry)
	// Item 2kq (f) as item 2nu left it: the address is built in, so this is normally
	// broker.DefaultURL and a phone can be paired on a fresh install. Constructing the
	// client loads this install's protected identity and any durable pairing. It opens
	// nothing unless a pairing is under way or already exists (2nu (d), 2ob).
	if address := strings.TrimSpace(cfg.Broker.URL); address != "" {
		if brokerClient, brokerErr := webserver.NewBrokerClient(address, paths.Data); brokerErr != nil {
			log.Printf("broker: %v", brokerErr)
		} else {
			web.SetBrokerHost(brokerClient)
		}
	}
	notificationStore, err := credential.NewNamed(profileRoot, cfg.Notifications.DiscordCredential)
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
	operatorFiles := operatorfiles.New(profileRoot, logDir, web.ConfigSnapshot)
	// Item 2py (c): once per profile, the transcripts earlier versions wrote on delete.
	if profiles, err := filepath.Glob(filepath.Join(paths.Data, "profiles", "*")); err == nil {
		if _, err := operatorfiles.SweepChatExports(append(profiles, paths.Data)); err != nil {
			log.Printf("chat export sweep: %v", err)
		}
	}
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
	web.ApplyTelemetry()
	startupApp = web.App()
	crashes := pendingCrashReports(paths.Data)
	previousCrashed = len(crashes) > 0
	for _, report := range crashes {
		if web.QueueStartupTelemetry("error", report.At, report.Data) {
			if err := markCrashReported(report.Path); err != nil {
				log.Printf("mark crash telemetry: %v", err)
			}
		}
	}
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
	statsManager := stats.New(profileRoot, registry, bus)
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
	if err := renderer.LoadDelegate(filepath.Join(paths.Application, "prompts", "delegate.md")); err != nil {
		log.Printf("debug: %v", err)
	}
	workspaces := session.NewWorkspaceRegistry()
	coordinator := tools.NewFileCoordinator(workspaces, registry.Label, bus)
	// Item 2li (b): the store is pinned to the service account it holds the
	// password for, so a machine-scoped blob's access list can name that account
	// and the read-side check can insist on it.
	credentialStore := credential.New(paths.Data).ForAccount(credential.ServiceAccount)
	fileIdentity := tools.NewFileIdentity(credentialStore)
	fileIdentity.Configure(*cfg)
	shellTool := tools.NewShell(cfg.Shell)
	shellTool.SetFileCoordinator(coordinator)
	shellTool.Configure(*cfg)
	shellTool.SetCredentialStore(credentialStore)
	web.SetShellSecurity(credentialStore, shellTool)
	web.SetServiceAccountManager(serviceaccount.NewNative())
	if cfg.Shell.ServiceAccount.Enabled {
		migrationContext, cancelMigration := context.WithTimeout(context.Background(), 30*time.Second)
		migrationErr := web.ReconcileServiceIdentityAtStartup(migrationContext)
		cancelMigration()
		if migrationErr != nil {
			log.Printf("disable unready service identity during update: %v", migrationErr)
		}
		*cfg = web.ConfigSnapshot()
	}
	fileIdentity.Configure(*cfg)
	shellTool.SetIdentityReporter(func(status tools.ShellIdentityStatus) {
		bus.Publish(events.New(events.ShellIdentity, "", "", status))
	})
	hardeningManager := hardening.NewNative()
	web.SetHardeningManager(hardeningManager)
	registry.SetPlanGrant(func(repository string) error {
		if !web.ConfigSnapshot().Shell.ServiceAccount.Enabled {
			return nil
		}
		grantContext, cancelGrant := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelGrant()
		return hardeningManager.GrantPlan(grantContext, web.ConfigSnapshot().Shell.ServiceAccount.Account, repository)
	})
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
	// Item 2jf (d): the tool enforces the layer budget, so it needs the number.
	rememberTool := tools.NewRemember(memoryManager, bus)
	rememberTool.SetConfig(web.ConfigSnapshot)
	fetchTool := tools.NewFetch(cfg.Tools.Fetch)
	delegateTool := tools.NewDelegate()
	callServiceTool := tools.NewCallService(cfg.Services)
	// Item 2nv: the named-credential store lives beside the connection credentials, under
	// the operator data root, and the tool reads it through one seam.
	vault := credential.NewVault(paths.Data)
	callServiceTool.SetVault(vault)
	entraManager := entra.New(vault)
	callServiceTool.SetTokenProvider("entra", entraManager)
	cronManager := cron.New(profileRoot, nil, nil)
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
		rememberTool,
		tools.NewRecall(memoryManager),
		fetchTool,
		tools.NewWebSearch(fetchTool, cfg.Tools.WebSearch),
		tools.NewRunScript(shellTool),
		callServiceTool,
		delegateTool,
		tools.NewChatHistory(func() tools.ChatHistoryReader { return writers }),
		tools.NewCronjob(cronManager),
	)
	// Item 2ch (v1.2.5): the threshold under which a PDF is sent inline rather
	// than read from its extracted text.
	agent.SetInlineDocumentLimit(cfg.Tools.Attachments.InlineDocumentLimit())
	runner := agent.NewRunner(bus, toolRegistry, renderer, web.Connection, web.ConfigSnapshot)
	runner.SetTrustedFolderWriter(web.TrustFolders)
	runner.Gate().SetStandingGrantStore(filepath.Join(paths.Data, "standing-grants.json"))
	callServiceTool.SetConnectorWriter(web.ApplyConnector)
	webserver.SetCredentialVault(vault)
	webserver.SetIdentityProvider("entra", entraManager)
	runner.BindDelegate(delegateTool)
	runner.SetSessionRenamer(registry.RenameBy)
	deliveryManager := delivery.New(bus, web.ConfigSnapshot)
	runner.SetDeliverer(func(item *session.Session, runID string, files []delivery.Source) delivery.Result {
		return deliveryManager.Deliver(item, runID, files)
	})
	scheduler := agent.NewScheduler(runner, registry, bus, web.ConfigSnapshot)
	scheduled := &scheduledRuns{profile:profileRoot,cfg:web.ConfigSnapshot,registry:registry,scheduler:scheduler,bus:bus,web:web,sessions:map[string]string{}}
	cronManager.SetRunner(scheduled.run)
	cronManager.SetHooks(nil, scheduled.finish)
	cronContext, cancelCron := context.WithCancel(context.Background()); defer cancelCron()
	go cronManager.Run(cronContext)
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
	web.StartConnectionHealth(context.Background())
	web.SetProfileChanged(func(nextRoot string) error {
		nextLogDir := cfg.LogDir
		if !filepath.IsAbs(nextLogDir) {
			nextLogDir = filepath.Join(nextRoot, nextLogDir)
		}
		nextWriters, openErr := events.NewWriters(nextLogDir)
		if openErr != nil {
			return openErr
		}
		nextNotificationStore, openErr := credential.NewNamed(nextRoot, cfg.Notifications.DiscordCredential)
		if openErr != nil {
			_ = nextWriters.Close()
			return openErr
		}
		web.StopReflection()
		memoryManager.SetBaseDir(nextRoot)
		nextWorkspaceManager := workspaceinfo.New(memoryManager.Dir(), memoryManager.Path)
		if switchErr := registry.SwitchProfile(nextWriters, memoryManager.Load, memoryManager.LoadAgent, nextWorkspaceManager, filepath.Join(nextRoot, "plans")); switchErr != nil {
			_ = nextWriters.Close()
			web.StartReflection(24 * time.Hour)
			web.ApplyTelemetry()
			return switchErr
		}
		web.SetRegistry(registry)
		nextProjector := projection.NewStore()
		bus.SetDurableSink(nextWriters.WriteRecord, nextProjector.Apply, nextProjector.MarkStale)
		bus.SetTransientSink(nextProjector.ApplyTransient)
		web.SetProjection(nextProjector, nextWriters)
		if closeErr := writers.Close(); closeErr != nil {
			log.Printf("close previous profile event logs: %v", closeErr)
		}
		writers, projector = nextWriters, nextProjector
		workspaceManager = nextWorkspaceManager
		operatorFiles.SetRoot(nextRoot, nextLogDir)
		if ensureErr := operatorFiles.Ensure(); ensureErr != nil {
			return ensureErr
		}
		statsManager.SetRoot(nextRoot)
		notificationStore = nextNotificationStore
		web.SetNotifications(notificationManager, notificationStore)
		if value, readErr := notificationStore.Read(); readErr == nil {
			if configureErr := notificationManager.Configure(string(value)); configureErr != nil {
				return configureErr
			}
		} else if errors.Is(readErr, credential.ErrNotStored) {
			_ = notificationManager.Configure("")
		} else {
			return readErr
		}
		restored, floor, restoreErr := restoreRetainedChats(writers, registry, bus, 0)
		if restoreErr != nil {
			return restoreErr
		}
		runner.ReserveIDs(floor)
		scheduler.ReserveIDs(floor)
		registry.RefreshRunnable()
		for _, item := range restored {
			if !item.IsClosed() {
				runner.PublishBudget(context.Background(), item)
			}
			// rel-1.23.0 card 5: a message that was queued when the process ended is
			// still queued, in the same place.
			if count := scheduler.RestoreQueue(item); count > 0 {
				log.Printf("restored chat %s came back with %d queued message(s), held until your next message", item.ID, count)
			}
		}
		web.SetWorkspaceState(nextWorkspaceManager, memoryManager)
		scheduled.setProfile(nextRoot)
		if err := cronManager.SetProfileRoot(nextRoot); err != nil { return err }
		web.StartReflection(24 * time.Hour)
		web.ApplyTelemetry()
		web.PublishPlanChanges()
		return nil
	})
	if len(cfg.Connections) == 0 {
		log.Printf("first-run setup required: no model connections are configured")
	} else {
		mainAgentID := cfg.DefaultAgentID()
		if ready, reason := registry.AgentRunnable(mainAgentID); ready {
			log.Printf("startup agent %s ready from saved capabilities", mainAgentID)
		} else {
			log.Printf("startup agent %s not runnable: %s; use Connections > Test", mainAgentID, reason)
		}
		phases.mark("before restore")
		// restoreRetainedChats computes the id floor from the same projection it
		// restores. Projecting every journal here first used to do the dominant
		// startup work twice.
		restored, floor, restoreErr := restoreRetainedChats(writers, registry, bus, 0)
		if restoreErr != nil {
			log.Fatal(restoreErr)
		}
		phases.mark("restore retained chats")
		runner.ReserveIDs(floor)
		scheduler.ReserveIDs(floor)
		registry.RefreshRunnable()
		for _, item := range restored {
			if !item.IsClosed() {
				runner.PublishBudget(context.Background(), item)
			}
			if count := scheduler.RestoreQueue(item); count > 0 {
				log.Printf("restored chat %s came back with %d queued message(s), held until your next message", item.ID, count)
			}
		}
	}
	publishPendingSigning(paths.Data, registry, bus)
	if err := serve(cfg, web.Handler(), newLifetime(paths.Data, time.Now), paths.Application, web.BrowserBootstrapToken()); err != nil {
		log.Fatal(err)
	}
}

func serviceIdentityStartupNotice(service config.ShellServiceAccount, status credential.Status) string {
	credentialState := "missing"
	if status.Stored {
		credentialState = "stored"
	}
	return fmt.Sprintf("service identity not set up: enabled=%t; account=%s; credential=%s", service.Enabled, service.Account, credentialState)
}

func publishServiceSetupInvitation(registry *session.Registry, bus *events.Bus) {
	var newest *session.Session
	for _, item := range registry.List() {
		if item.IsClosed() || item.Role == "c" {
			continue
		}
		if newest == nil || item.CreatedAt.After(newest.CreatedAt) || (item.CreatedAt.Equal(newest.CreatedAt) && item.ID > newest.ID) {
			newest = item
		}
	}
	if newest == nil {
		return
	}
	bus.Publish(events.New(events.ServiceIdentityUnavailable, newest.ID, "", map[string]any{
		"message": "Agent_b sets up its service identity now; Windows will ask once",
		"action":  "provision",
		"launch":  true,
	}))
}

func allUsersInstallRequested(executable string, arguments []string) bool {
	hasInstall, allUsers := setupExecutable(executable), false
	for _, argument := range arguments {
		switch flagName(argument) {
		case "install":
			hasInstall = true
		case "all-users":
			allUsers = true
		}
	}
	return hasInstall && allUsers
}

func setupExecutable(path string) bool {
	// Setup names are a Windows contract even when this pure parser is tested
	// on a Unix CI runner, where filepath.Base does not recognize backslashes.
	name := strings.ToLower(filepath.Base(strings.ReplaceAll(path, `\`, "/")))
	if name == "agent_b-setup.exe" {
		return true
	}
	const prefix = "agent_b-setup ("
	const suffix = ").exe"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	number := name[len(prefix) : len(name)-len(suffix)]
	if number == "" {
		return false
	}
	for _, digit := range number {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

// AGENTB_UPDATE_SOURCE_URL lets an organisation publish the same release API
// shape at its own HTTPS endpoint. The downloaded release.json digest and the
// Authenticode verification remain mandatory on that path.
// AGENTB_UPDATE_FIXTURE_URL is an acceptance-only seam. It is deliberately
// limited to loopback so an inherited environment cannot redirect the product's
// release trust path to another public host.
func updateLatestURL() string {
	if raw := strings.TrimSpace(os.Getenv("AGENTB_UPDATE_SOURCE_URL")); raw != "" {
		endpoint, err := url.Parse(raw)
		if err == nil && endpoint.Scheme == "https" && endpoint.Hostname() != "" && endpoint.User == nil && endpoint.Fragment == "" {
			return endpoint.String()
		}
	}
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
// restoreProjections is item 2m5 (b)'s whole mechanism: read each chat's PROJECTED
// STATE when the journal it came from has not changed, and project only the journals
// that have.
//
// Measured on a copy of the user's own chats before this existed: projecting all
// 33 journals, 218,150,298 bytes, took 6.244s; the snapshots they produce total
// 6,723,596 bytes and decode in 45ms. His launches were spending 6,797 ms of 8,513 ms
// there. A journal grows with every delta ever streamed; a projection is bounded by
// what the chat shows, and that is the difference the cache turns into launch time.
//
// A miss is never an error: the chat is projected exactly as it always was. Nothing
// under chats/ is read for anything but projection and nothing there is written.
func restoreProjections(writers *events.Writers, paths []string) (map[string]projection.Snapshot, string, int, error) {
	cacheDir := writers.ProjectionCacheDir()
	sessions := make(map[string]projection.Snapshot, len(paths))
	cache := projection.NewCache()
	hits := 0
	for _, path := range paths {
		id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if snapshot, ok := projection.LoadCachedSnapshot(cacheDir, path); ok && snapshot.ID != "" {
			snapshot.ID = id
			sessions[id] = snapshot
			hits++
			continue
		}
		snapshot, err := cache.ProjectFile(path, 0)
		if err != nil {
			return nil, cacheDir, hits, fmt.Errorf("load retained chats: %w", err)
		}
		snapshot.ID = id
		snapshot.LogPath = path
		sessions[id] = snapshot
		projected := int64(0)
		if info, statErr := os.Stat(path); statErr == nil {
			projected = info.Size()
		}
		if storeErr := projection.StoreCachedSnapshot(cacheDir, path, snapshot, projected); storeErr != nil {
			// The product starts whether or not it can cache. Saying so once is
			// better than a silent slow launch every time.
			log.Printf("projection cache not written for %s: %v", id, storeErr)
		}
	}
	projection.PruneCachedSnapshots(cacheDir, paths)
	return sessions, cacheDir, hits, nil
}

// restoreRetainedChats restores every retained chat and returns the id floor
// past everything it holds, including ids it re-minted (item 2fd rule 7).
func restoreRetainedChats(writers *events.Writers, registry *session.Registry, bus *events.Bus, floor int64) ([]*session.Session, int64, error) {
	paths, err := writers.DurableChatPaths()
	if err != nil {
		return nil, floor, err
	}
	hadDurable := len(paths) > 0
	if _, archived, scanErr := chatstore.New(writers.ChatDir()).List(); scanErr == nil && len(archived) > 0 {
		hidden := make(map[string]bool, len(archived))
		for _, entry := range archived {
			hidden[entry.Metadata.ID] = true
		}
		kept := paths[:0]
		for _, path := range paths {
			if !hidden[strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))] {
				kept = append(kept, path)
			}
		}
		paths = kept
	}
	if !hadDurable {
		paths, err = writers.LatestOperationalSessionPaths()
		if err != nil {
			return nil, floor, err
		}
	}
	if len(paths) == 0 {
		return nil, floor, nil
	}
	sessions, _, hits, err := restoreProjections(writers, paths)
	if err != nil {
		return nil, floor, err
	}
	startupTimer.mark("parse and project journals")
	log.Printf("startup projections: %d chat(s), %d read from the projection cache, %d projected from the journal", len(sessions), hits, len(sessions)-hits)
	replay := &projection.Replay{Sessions: sessions}
	ids := make([]string, 0, len(replay.Sessions))
	for id := range replay.Sessions {
		ids = append(ids, id)
	}
	// Item 2m5 (b): the floor is taken over EVERY restored chat before the first
	// remint, because a remint in s1 must not collide with an id s9 already holds.
	// It used to be a second full projection of the same journals (retainedIDFloor,
	// now gone); it is a pass over the snapshots this function already has.
	for _, id := range ids {
		floor = snapshotIDFloor(replay.Sessions[id], floor)
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
		// Item 2mt (e): A RUN THE PROCESS DIED IN IS CLOSED HERE, and the item
		// asked me to verify this already happened. It did not. The projector
		// takes `run.status` from `run.started` and `run.stopped`, so a journal
		// that stops on a `model.delta` — which is exactly what the user's
		// chat s34 does, run r643 started at 08:09:04 and 6,544 deltas in when
		// the drag killed the process — projects as STILL RUNNING, and stayed
		// that way on every later start. A chat that claims to be thinking
		// forever is worse than one that says it was cut.
		saved.Run = closeRunCutByProcessDeath(saved.Run, replay.Sessions[id].Timeline)
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

func snapshotIDFloor(snapshot projection.Snapshot, floor int64) int64 {
	note := func(id string) {
		end, start := len(id), len(id)
		for start > 0 && id[start-1] >= '0' && id[start-1] <= '9' {
			start--
		}
		if start == end {
			return
		}
		if value, err := strconv.ParseInt(id[start:end], 10, 64); err == nil && value > floor {
			floor = value
		}
	}
	for _, message := range snapshot.Messages {
		note(message.ID)
	}
	for _, entry := range snapshot.Chat {
		note(entry.RunID)
	}
	for _, event := range snapshot.Timeline {
		note(event.RunID)
	}
	return floor
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
	// those are the user's and are config-driven.
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
func serve(cfg *config.Config, handler http.Handler, life *lifetime, applicationRoot, browserBootstrap string) error {
	httpServer := &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	stopped := func(string) {}
	closeRequests := make(chan string, 1)
	if life != nil {
		life.applicationRoot = applicationRoot
		life.listen = cfg.Listen
		life.begin()
		setActiveLifetime(life)
		defer setActiveLifetime(nil)
		if startupApp != nil {
			startupApp.NoteLifecycleStart(life.previousExit)
			life.onExit = func(cause string, uptime int64) {
				startupApp.NoteLifecycleExit(cause, uptime)
				startupApp.SendNow()
			}
		}
		watchSessionEnd(applicationRoot, life.stopped, func() {
			select {
			case closeRequests <- "installer":
			default:
			}
		})
		stopped = life.stopped
	}
	startHostWindow(cfg.Listen, applicationRoot, browserBootstrap, closeRequests)
	errors := make(chan error, 1)
	go func() {
		log.Printf("Agent_b listening on http://%s", cfg.Listen)
		startupTimer.report()
		if startupApp != nil && startupTimer != nil {
			exit := "clean"
			if previousCrashed {
				exit = "crash"
			} else if life != nil && life.unclean {
				exit = "unclean"
			}
			startupApp.NoteListening(time.Since(startupTimer.started).Milliseconds(), exit)
		}
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
	case cause := <-closeRequests:
		log.Printf("stopping on a close request")
		if life != nil {
			detail := "asked to close"
			if cause == "installer" { detail = "asked to close (the installer's graceful stop)" }
			life.stoppedCause(cause, detail)
		} else {
			stopped("asked to close")
		}
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
//
// Item 2o0: the window's fate is written to the startup log AND launcher.log
// for every launch, and the window is supervised rather than attempted once.
// Five mornings the sign-in start (-Detached -NoBrowser, so no -window) left a
// server with no window and, because only a window owned the activation event,
// nothing a later launch could signal: the Start menu handoff failed silently.
// The server now owns that event for its whole life; a request with no window
// creates one, and a failed creation is retried with a bounded backoff.
var (
	hostWindowMode         bool
	hostWindowUserData     string
	hostWindowDataRoot     string
	hostWindowRunner       = runHostWindow
	hostWindowRetryDelays  = []time.Duration{time.Second, 3 * time.Second}
	hostWindowAvailability = hostWindowAvailable
)

func startHostWindow(listen, applicationRoot, browserBootstrap string, closeRequests chan string) {
	requests := make(chan struct{}, 1)
	watchActivation(applicationRoot, func() {
		if raiseHostWindow() {
			return
		}
		select {
		case requests <- struct{}{}:
		default:
		}
	})
	url := "http://" + listen + "/chat#agentb-bootstrap=" + browserBootstrap
	go superviseHostWindow(url, applicationRoot, hostWindowMode, requests, closeRequests)
}

func superviseHostWindow(url, applicationRoot string, wanted bool, requests <-chan struct{}, closeRequests chan<- string) {
	if !wanted {
		recordWindowFate("host window: not requested by this launch (a background start); the next launch opens it")
		<-requests
	}
	for {
		if openHostWindow(url, applicationRoot) {
			log.Printf("host window: closed")
			select {
			case closeRequests <- "user":
			default:
			}
			return
		}
		<-requests
	}
}

// openHostWindow reports whether a window opened and was then closed by the
// operator. Every other outcome is recorded and leaves the server serving.
func openHostWindow(url, applicationRoot string) bool {
	if version, err := hostWindowAvailability(); err != nil {
		recordWindowFate(fmt.Sprintf("host window: unavailable, using the browser instead (%v)", err))
		return false
	} else {
		log.Printf("host window: WebView2 runtime %s", version)
	}
	for attempt := 1; ; attempt++ {
		err := hostWindowRunner(url, hostWindowUserData, "Agent_b", applicationRoot)
		if err == nil {
			return true
		}
		if attempt > len(hostWindowRetryDelays) {
			recordWindowFate(fmt.Sprintf("host window: could not open after %d attempts, using the browser instead (%v); the next launch tries again", attempt, err))
			return false
		}
		delay := hostWindowRetryDelays[attempt-1]
		recordWindowFate(fmt.Sprintf("host window: attempt %d failed (%v); retrying in %s", attempt, err, delay))
		time.Sleep(delay)
	}
}

// recordWindowFate writes one window line to the startup log and to the
// launcher log the user reads, in the launcher's own line format.
// Item 2q7 (a): the app aggregator, and whether the last instance crashed;
// package level because the listen and window lines live outside main().
var (
	startupApp      *recorder.App
	previousCrashed bool
)

func recordWindowFate(message string) {
	log.Print(message)
	if message == "host window: opened" && startupApp != nil {
		startupApp.NoteWindowShown()
	}
	if hostWindowDataRoot == "" {
		return
	}
	path := filepath.Join(hostWindowDataRoot, "logs", "launcher.log")
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	if file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		_, _ = fmt.Fprintf(file, "%s %s\r\n", time.Now().Format("2006-01-02 15:04:05 -07:00"), printable(message))
		file.Close()
	}
}
