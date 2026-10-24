package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"harness/internal/agent"
	"harness/internal/buildinfo"
	"harness/internal/credential"
	"harness/internal/events"
	"harness/internal/llm"
	"harness/internal/memory"
	"harness/internal/notifications"
	"harness/internal/operatorfiles"
	"harness/internal/projection"
	"harness/internal/session"
	"harness/internal/stats"
	"harness/internal/updater"
	webserver "harness/internal/web"
	workspaceinfo "harness/internal/workspace"
)

// openJournal opens the profile's event logs and the bus, and builds the web server and updater on them.
func (a *harnessApp) openJournal() {
	a.logDir = a.cfg.LogDir
	if !filepath.IsAbs(a.logDir) {
		a.logDir = filepath.Join(a.profileRoot, a.logDir)
	}
	writers, err := events.NewWriters(a.logDir)
	if err != nil {
		log.Fatal(err)
	}
	a.writers = writers
	a.cleanup(func() {
		if err := a.writers.Close(); err != nil {
			log.Printf("close event logs: %v", err)
		}
	})
	a.bus = events.NewBus()
	// The tally folds the run's own events and puts the buckets and the
	// reliability counters onto run.stopped, before the journal is written, so the
	// chat line, Activity and the telemetry receiver all read the same numbers
	// from the same place and cannot disagree.
	stats.InstallRunTally(a.bus)
	a.projector = projection.NewStore()
	a.bus.SetDurableSink(retainedSink(a.writers), a.projector.Apply, a.projector.MarkStale)
	a.bus.SetTransientSink(a.projector.ApplyTransient)
	a.web = webserver.New(a.cfg, a.paths.Config, filepath.Join(a.paths.Application, "web"), a.roots, a.bus)
	a.web.SetProfiles(a.profileManager)
	attachHostWindowActions(a.web)
	updateManager := updater.New(updater.Options{
		CurrentVersion: buildinfo.Current().Tag,
		DataRoot:       a.paths.Data,
		// this instance updates where it lives, not where the operator
		// per-user install happens to be.
		ApplicationRoot: a.paths.Application,
		WorkspaceRoot:   a.paths.Workspace,
		LatestURL:       updateLatestURL(),
		Enabled:         func() bool { return a.web.ConfigSnapshot().Updates.AutoCheck },
		Changed:         func(state updater.State) { a.bus.Publish(events.New(events.UpdateChanged, "", "", state)) },
	})
	a.web.SetUpdater(updateManager)
	updateManager.Start(context.Background())
	a.cleanup(updateManager.Close)
	a.web.PublishPlanChanges()
	a.web.SetProjection(a.projector, a.writers)
	for _, notice := range a.cfg.LoadNotices {
		a.bus.Publish(events.New(events.ConfigChanged, "", "", map[string]any{"config": a.cfg.Masked(), "notice": notice}))
	}
}

// wireServices builds memory, the session registry, notifications, the operator's files,
// reflection, startup telemetry, stats and the prompt templates.
func (a *harnessApp) wireServices() {
	a.memoryManager = memory.New(a.profileRoot, a.web.ConfigSnapshot, func(ctx context.Context, connectionID, text string) (int, error) {
		connection, ok := a.web.Connection(connectionID)
		if !ok || !connection.Capabilities.Tokenize {
			return 0, fmt.Errorf("tokenizer unavailable")
		}
		return llm.New(connection).Tokenize(ctx, text, false)
	})
	a.workspaceManager = workspaceinfo.New(a.memoryManager.Dir(), a.memoryManager.Path)
	a.registry = session.NewRegistry(a.bus, a.writers, a.web.Connection, 0, a.web.ConfigSnapshot)
	a.registry.SetMemoryLoader(a.memoryManager.Load)
	a.registry.SetAgentMemoryLoader(a.memoryManager.LoadAgent)
	a.registry.SetMachineMemoryLoader(a.memoryManager.LoadMachine)
	a.registry.SetWorkspaceManager(a.workspaceManager)
	a.web.SetRegistry(a.registry)
	// The address is built in, so this is normally
	// broker.DefaultURL and a phone can be paired on a fresh install. Constructing the
	// client loads this install's protected identity and any durable pairing. It opens
	// nothing unless a pairing is under way or already exists (2nu (d), 2ob).
	var notificationPhone notifications.Phone
	if address := strings.TrimSpace(a.cfg.Broker.URL); address != "" {
		if brokerClient, brokerErr := webserver.NewBrokerClient(address, a.paths.Data); brokerErr != nil {
			log.Printf("broker: %v", brokerErr)
		} else {
			a.web.SetBrokerHost(brokerClient)
			notificationPhone = brokerClient
		}
	}
	notificationStore, err := credential.NewNamed(a.profileRoot, a.cfg.Notifications.DiscordCredential)
	if err != nil {
		log.Fatal(err)
	}
	a.notificationStore = notificationStore
	a.notificationManager = notifications.New(a.bus, a.registry.Label, "http://"+a.cfg.Listen)
	if states, stateErr := a.web.ChatNoticeStates(); stateErr != nil {
		log.Printf("load chat notification state: %v", stateErr)
	} else {
		a.notificationManager.SetChatPolicy(states, a.web.SetChatNotice)
	}
	a.notificationManager.SetChannels(desktopNotifier{}, notificationPhone, func(group string) bool {
		settings := a.web.ConfigSnapshot().Notifications
		if !settings.Desktop {
			return false
		}
		switch group {
		case "needs_you":
			return settings.DesktopNeedsYou
		case "finished":
			return settings.DesktopFinished
		case "scheduled":
			return settings.DesktopScheduled
		}
		return true
	})
	if fixture := notificationFixtureURL(); fixture != "" {
		if configureErr := a.notificationManager.ConfigureLoopbackFixture(fixture); configureErr != nil {
			log.Printf("notification acceptance fixture is invalid; notifications disabled: %v", configureErr)
		}
	} else if value, readErr := a.notificationStore.Read(); readErr == nil {
		if configureErr := a.notificationManager.Configure(string(value)); configureErr != nil {
			log.Printf("Discord notification credential is invalid; notifications disabled: %v", configureErr)
		}
	} else if !errors.Is(readErr, credential.ErrNotStored) {
		log.Printf("load Discord notification credential: %v", readErr)
	}
	a.notificationManager.Start(context.Background())
	a.cleanup(a.notificationManager.Close)
	a.web.SetNotifications(a.notificationManager, a.notificationStore)
	a.web.SetWorkspaceState(a.workspaceManager, a.memoryManager)
	a.operatorFiles = operatorfiles.New(a.profileRoot, a.logDir, a.web.ConfigSnapshot)
	if profiles, err := filepath.Glob(filepath.Join(a.paths.Data, "profiles", "*")); err == nil {
		if _, err := operatorfiles.SweepChatExports(append(profiles, a.paths.Data)); err != nil {
			log.Printf("chat export sweep: %v", err)
		}
	}
	a.operatorFiles.SetEventPublisher(func(event events.Event) { a.bus.Publish(event) })
	if err := a.operatorFiles.Ensure(); err != nil {
		log.Fatal(err)
	}
	if _, err := a.operatorFiles.ApplyRetention(); err != nil {
		log.Printf("apply log retention: %v", err)
	}
	a.web.SetOperatorFiles(a.operatorFiles)
	// reflection runs after a run closes and on a daily tick. It
	// subscribes to the bus, so it is never in a run's path.
	a.web.StartReflection(24 * time.Hour)
	a.web.ApplyTelemetry()
	startupApp = a.web.App()
	crashes := pendingCrashReports(a.paths.Data)
	previousCrashed = len(crashes) > 0
	for _, report := range crashes {
		if a.web.QueueStartupTelemetry("error", report.At, report.Data) {
			if err := markCrashReported(report.Path); err != nil {
				log.Printf("mark crash telemetry: %v", err)
			}
		}
	}
	a.cleanup(a.web.StopReflection)
	operatorContext, cancelOperatorFiles := context.WithCancel(context.Background())
	a.cleanup(cancelOperatorFiles)
	go a.operatorFiles.RunRetention(operatorContext)
	operatorEvents := a.bus.Listen(operatorContext, "file retention")
	go func() {
		for event := range operatorEvents {
			var snapshot *session.Snapshot
			if item, ok := a.registry.Get(event.SessionID); ok {
				value := item.Snapshot()
				snapshot = &value
			}
			a.operatorFiles.HandleEvent(event, snapshot)
		}
	}()
	a.statsManager = stats.New(a.profileRoot, a.registry, a.bus)
	a.web.SetStats(a.statsManager)
	renderer, err := agent.LoadTemplate(filepath.Join(a.paths.Application, "prompts", "system.md"))
	if err != nil {
		log.Fatal(err)
	}
	a.renderer = renderer
	if err := a.renderer.LoadPlanner(filepath.Join(a.paths.Application, "prompts", "planner.md")); err != nil {
		log.Fatal(err)
	}

	// The worker brief is optional in the same way the planner's is: an install
	// without it simply has no worker, rather than refusing to start.
	if err := a.renderer.LoadWorker(filepath.Join(a.paths.Application, "prompts", "worker.md")); err != nil {
		log.Printf("debug: %v", err)
	}
	if err := a.renderer.LoadDelegate(filepath.Join(a.paths.Application, "prompts", "delegate.md")); err != nil {
		log.Printf("debug: %v", err)
	}
}
