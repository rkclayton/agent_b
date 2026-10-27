package main

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"time"

	"harness/internal/chatstore"
	"harness/internal/credential"
	"harness/internal/events"
	"harness/internal/projection"
	workspaceinfo "harness/internal/workspace"
)

// onProfileChanged moves the running harness onto another profile's root: its logs, journal,
// projection, files, notifications and retained chats.
func (a *harnessApp) onProfileChanged(nextRoot string) error {
	nextLogDir := a.cfg.LogDir
	if !filepath.IsAbs(nextLogDir) {
		nextLogDir = filepath.Join(nextRoot, nextLogDir)
	}
	nextWriters, openErr := events.NewWriters(nextLogDir)
	if openErr != nil {
		return openErr
	}
	nextNotificationStore, openErr := credential.NewNamed(nextRoot, a.cfg.Notifications.DiscordCredential)
	if openErr != nil {
		_ = nextWriters.Close()
		return openErr
	}
	a.web.StopReflection()
	a.memoryManager.SetBaseDir(nextRoot)
	nextWorkspaceManager := workspaceinfo.New(a.memoryManager.Dir(), a.memoryManager.Path)
	if switchErr := a.registry.SwitchProfile(nextWriters, a.memoryManager.Load, a.memoryManager.LoadAgent, nextWorkspaceManager, filepath.Join(nextRoot, "plans")); switchErr != nil {
		_ = nextWriters.Close()
		a.web.StartReflection(24 * time.Hour)
		a.web.ApplyTelemetry()
		return switchErr
	}
	a.web.SetRegistry(a.registry)
	nextProjector := projection.NewStore()
	a.bus.SetDurableSink(retainedSink(nextWriters), nextProjector.Apply, nextProjector.MarkStale)
	a.bus.SetTransientSink(nextProjector.ApplyTransient)
	a.web.SetProjection(nextProjector, nextWriters)
	if closeErr := a.writers.Close(); closeErr != nil {
		log.Printf("close previous profile event logs: %v", closeErr)
	}
	a.writers, a.projector = nextWriters, nextProjector
	a.workspaceManager = nextWorkspaceManager
	a.operatorFiles.SetRoot(nextRoot, nextLogDir)
	a.fileUndo.SetRoot(nextRoot)
	if ensureErr := a.operatorFiles.Ensure(); ensureErr != nil {
		return ensureErr
	}
	a.statsManager.SetRoot(nextRoot)
	a.notificationStore = nextNotificationStore
	a.web.SetNotifications(a.notificationManager, a.notificationStore)
	if value, readErr := a.notificationStore.Read(); readErr == nil {
		if configureErr := a.notificationManager.Configure(string(value)); configureErr != nil {
			return configureErr
		}
	} else if errors.Is(readErr, credential.ErrNotStored) {
		_ = a.notificationManager.Configure("")
	} else {
		return readErr
	}
	restored, floor, restoreErr := restoreRetainedChats(a.writers, a.registry, a.bus, 0)
	if restoreErr != nil {
		return restoreErr
	}
	a.runner.ReserveIDs(floor)
	a.scheduler.ReserveIDs(floor)
	a.registry.RefreshRunnable()
	for _, item := range restored {
		if !item.IsClosed() {
			a.runner.PublishBudget(context.Background(), item)
		}
		// a message that was queued when the process ended is
		// still queued, in the same place.
		if count := a.scheduler.RestoreQueue(item); count > 0 {
			log.Printf("restored chat %s came back with %d queued message(s), held until your next message", item.ID, count)
		}
	}
	a.web.SetWorkspaceState(nextWorkspaceManager, a.memoryManager)
	a.scheduled.setProfile(nextRoot)
	if err := a.cronManager.SetProfileRoot(nextRoot); err != nil {
		return err
	}
	a.web.StartReflection(24 * time.Hour)
	a.web.ApplyTelemetry()
	a.web.PublishPlanChanges()
	return nil
}

// restoreAtStartup cleans a first run's leftover chat folders, or restores the retained chats
// and their queued messages.
func (a *harnessApp) restoreAtStartup() {
	if len(a.cfg.Connections) == 0 {
		paths, cleanupErr := a.writers.DurableChatPaths()
		if cleanupErr != nil {
			startupFatal(cleanupErr)
		}
		slimRetainedChats(a.writers, paths)
		journals := make(map[string]bool, len(paths))
		for _, path := range paths {
			journals[strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))] = true
		}
		cleanup, cleanupErr := chatstore.New(a.writers.ChatDir()).Cleanup(journals)
		if cleanupErr != nil {
			startupFatal(cleanupErr)
		}
		a.registry.ReserveChatIDs(cleanup.IDs)
		log.Printf("startup chat cleanup: removed %d leftover folder(s)", cleanup.Removed)
		for _, path := range cleanup.Kept {
			relative, _ := filepath.Rel(a.writers.ChatDir(), path)
			log.Printf("startup chat cleanup kept %s: contains files not written by Agent_b", relative)
		}
		log.Printf("first-run setup required: no model connections are configured")
		return
	}
	mainAgentID := a.cfg.DefaultAgentID()
	if ready, reason := a.registry.AgentRunnable(mainAgentID); ready {
		log.Printf("startup agent %s ready from saved capabilities", mainAgentID)
	} else {
		log.Printf("startup agent %s not runnable: %s; use Connections > Test", mainAgentID, reason)
	}
	a.phases.mark("before restore")
	// restoreRetainedChats computes the id floor from the same projection it
	// restores.
	restored, floor, restoreErr := restoreRetainedChats(a.writers, a.registry, a.bus, 0)
	if restoreErr != nil {
		startupFatal(restoreErr)
	}
	a.phases.mark("restore retained chats")
	a.runner.ReserveIDs(floor)
	a.scheduler.ReserveIDs(floor)
	a.registry.RefreshRunnable()
	for _, item := range restored {
		if !item.IsClosed() {
			a.runner.PublishBudget(context.Background(), item)
		}
		if count := a.scheduler.RestoreQueue(item); count > 0 {
			log.Printf("restored chat %s came back with %d queued message(s), held until your next message", item.ID, count)
		}
	}
}
