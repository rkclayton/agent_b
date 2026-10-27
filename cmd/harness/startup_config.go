package main

import (
	"context"
	"log"
	"path/filepath"
	"strings"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/profiles"
	"harness/internal/projection"
	"harness/internal/signing"
	webserver "harness/internal/web"
)

// loadConfig loads the configuration and the active profile, and fixes the workspace and runtime roots.
func (a *harnessApp) loadConfig() {
	a.phases.mark("config")
	cfg, migrated, created, err := config.LoadWithRoots(a.paths.Config, filepath.Join(a.paths.Application, "harness.example.json"), a.paths.Data)
	if err != nil {
		startupFatal(err)
	}
	a.cfg = cfg
	// every static_bearer connector moves into the credential store here,
	// once, before anything can use one. A connector whose variable is empty or whose
	// value does not verify is left unauthenticated and says so; nothing falls back to
	// the environment.
	if notices := config.MigrateConnectorCredentials(a.cfg, a.paths.Data); len(notices) > 0 {
		for _, notice := range notices {
			log.Printf("credentials: %s", notice)
		}
		if err := a.cfg.Save(a.paths.Config); err != nil {
			log.Printf("credentials: the migrated configuration could not be saved: %v", err)
		}
	}
	profileManager, profileMigrated, err := profiles.Open(a.paths.Data, a.paths.Config, a.cfg)
	if err != nil {
		startupFatal(err)
	}
	a.profileManager = profileManager
	a.profileRoot = a.profileManager.Root(a.profileManager.Active())
	workspacePath := a.cfg.Workspace
	if !filepath.IsAbs(workspacePath) {
		workspacePath = filepath.Join(a.profileRoot, workspacePath)
	}
	workspaceRoot, err := filepath.Abs(workspacePath)
	if err != nil {
		startupFatal(err)
	}
	a.paths.Workspace = filepath.Clean(workspaceRoot)
	a.roots = webserver.RuntimeRoots{Application: a.paths.Application, Data: a.paths.Data, Profile: a.profileRoot, Workspace: a.paths.Workspace}
	if created {
		log.Printf("created %s from %s - set connections[0].base_url and model", a.paths.Config, filepath.Join(a.paths.Application, "harness.example.json"))
	}
	if migrated {
		log.Printf("migrated %s to config schema %d", filepath.Base(a.paths.Config), config.CurrentConfigVersion)
	}
	if profileMigrated {
		log.Printf("migrated operator data into profile %s", a.profileManager.Active())
	}
	for _, notice := range a.cfg.LoadNotices {
		log.Printf("config migration: %s", notice)
	}
	facts := readServingFacts(filepath.Join(a.paths.Application, "SERVING.md"))
	if !facts.Complete {
		log.Printf("debug: SERVING.md missing or partial; skipping /tokenize latency hint")
	} else if facts.TokenizeBlocksOnSlot == "yes" {
		log.Printf("tokenize blocks on the generation slot (%d ms measured busy); context.accounting: \"estimated\" avoids it", facts.TokenizeBusyMS)
	}
	// every server can host its window, not only one started with
	// -window, because a later launch may ask a background server for it.
	hostWindowMode = *a.flags.window
	hostWindowDataRoot = a.paths.Data
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
		hostWindowUserData = filepath.Join(a.paths.Data, "webview2")
	}
}

// serveReplay serves recorded sessions instead of a live harness; it reports whether it did.
func (a *harnessApp) serveReplay() bool {
	if strings.TrimSpace(*a.flags.replayPaths) == "" {
		return false
	}
	replay, loadErr := projection.LoadReplay(strings.Split(*a.flags.replayPaths, ","))
	if loadErr != nil {
		startupFatal(loadErr)
	}
	web := webserver.New(a.cfg, a.paths.Config, filepath.Join(a.paths.Application, "web"), a.roots, events.NewBus())
	web.SetProfiles(a.profileManager)
	web.SetReplay(replay)
	web.SetSigningManager(signing.New(filepath.Join(a.paths.Application, "scripts", "manage-signing.ps1")))
	signingContext, cancelSigning := context.WithTimeout(context.Background(), 15*time.Second)
	if err := web.RefreshSigningState(signingContext); err != nil {
		log.Printf("inspect installed signatures: %v", err)
	}
	cancelSigning()
	if err := serve(a.cfg, web.Handler(), nil, "", web.BrowserBootstrapToken()); err != nil {
		startupFatal(err)
	}
	return true
}
