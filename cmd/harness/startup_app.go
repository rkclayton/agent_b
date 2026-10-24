package main

import (
	"harness/internal/agent"
	"harness/internal/config"
	"harness/internal/credential"
	"harness/internal/cron"
	"harness/internal/events"
	"harness/internal/memory"
	"harness/internal/notifications"
	"harness/internal/operatorfiles"
	"harness/internal/profiles"
	"harness/internal/projection"
	"harness/internal/session"
	"harness/internal/stats"
	"harness/internal/tools"
	webserver "harness/internal/web"
	workspaceinfo "harness/internal/workspace"
)

// harnessApp carries what one startup step makes and a later one needs (item 2wm). main builds
// it and passes it down; nothing here is package-level state. A field a later step reassigns
// (the profile switch's writers, projector, workspace manager and notification store) is read
// through the field, as the closures that captured main's locals read them before.
type harnessApp struct {
	flags               launchFlags
	phases              *startupPhases
	paths               startupPaths
	cfg                 *config.Config
	profileManager      *profiles.Manager
	profileRoot         string
	roots               webserver.RuntimeRoots
	logDir              string
	writers             *events.Writers
	bus                 *events.Bus
	projector           *projection.Store
	web                 *webserver.Server
	memoryManager       *memory.Manager
	workspaceManager    *workspaceinfo.Manager
	registry            *session.Registry
	notificationStore   *credential.Store
	notificationManager *notifications.Manager
	operatorFiles       *operatorfiles.Manager
	statsManager        *stats.Manager
	renderer            *agent.PromptRenderer
	fileUndo            *tools.FileUndoStore
	cronManager         *cron.Manager
	runner              *agent.Runner
	scheduler           *agent.Scheduler
	scheduled           *scheduledRuns
	// cleanups are main's former defers, in the order they were deferred; close runs them last first.
	cleanups []func()
}

func (a *harnessApp) cleanup(step func()) { a.cleanups = append(a.cleanups, step) }

func (a *harnessApp) close() {
	for index := len(a.cleanups) - 1; index >= 0; index-- {
		a.cleanups[index]()
	}
}
