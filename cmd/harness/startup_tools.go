package main

import (
	"context"
	"log"
	"path/filepath"
	"time"

	"harness/internal/agent"
	"harness/internal/credential"
	"harness/internal/cron"
	"harness/internal/delivery"
	"harness/internal/entra"
	"harness/internal/events"
	"harness/internal/hardening"
	"harness/internal/serviceaccount"
	"harness/internal/session"
	"harness/internal/signing"
	"harness/internal/tools"
	webserver "harness/internal/web"
)

// wireTools builds the file, shell and service tools, registers them in their fixed order, and
// builds the runner, the scheduler and cron on them.
func (a *harnessApp) wireTools() {
	workspaces := session.NewWorkspaceRegistry()
	coordinator := tools.NewFileCoordinator(workspaces, a.registry.Label, a.bus)
	a.fileUndo = tools.NewFileUndoStore(a.profileRoot) // 2wp: the profile's, as after a switch
	coordinator.SetUndoStore(a.fileUndo)
	a.web.SetFileUndoStore(a.fileUndo)
	// the store is pinned to the service account it holds the
	// password for, so a machine-scoped blob's access list can name that account
	// and the read-side check can insist on it.
	credentialStore := credential.New(a.paths.Data).ForAccount(credential.ServiceAccount)
	fileIdentity := tools.NewFileIdentity(credentialStore)
	fileIdentity.Configure(*a.cfg)
	shellTool := tools.NewShell(a.cfg.Shell)
	shellTool.SetFileCoordinator(coordinator)
	shellTool.Configure(*a.cfg)
	shellTool.SetCredentialStore(credentialStore)
	diagnostics := tools.NewLanguageDiagnostics(shellTool, a.cfg.Tools.Diagnostics)
	coordinator.SetDiagnostics(diagnostics)
	a.cleanup(func() { diagnostics.Close() })
	a.web.SetShellSecurity(credentialStore, shellTool)
	a.web.SetServiceAccountManager(serviceaccount.NewNative())
	if a.cfg.Shell.ServiceAccount.Enabled {
		migrationContext, cancelMigration := context.WithTimeout(context.Background(), 30*time.Second)
		migrationErr := a.web.ReconcileServiceIdentityAtStartup(migrationContext)
		cancelMigration()
		if migrationErr != nil {
			log.Printf("disable unready service identity during update: %v", migrationErr)
		}
		*a.cfg = a.web.ConfigSnapshot()
	}
	fileIdentity.Configure(*a.cfg)
	shellTool.SetIdentityReporter(func(status tools.ShellIdentityStatus) {
		a.bus.Publish(events.New(events.ShellIdentity, "", "", status))
	})
	hardeningManager := hardening.NewNative()
	a.web.SetHardeningManager(hardeningManager)
	a.registry.SetPlanGrant(func(repository string) error {
		if !a.web.ConfigSnapshot().Shell.ServiceAccount.Enabled {
			return nil
		}
		grantContext, cancelGrant := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelGrant()
		return hardeningManager.GrantPlan(grantContext, a.web.ConfigSnapshot().Shell.ServiceAccount.Account, repository)
	})
	a.web.SetSigningManager(signing.New(filepath.Join(a.paths.Application, "scripts", "manage-signing.ps1")))
	// inspecting the installed signatures runs PowerShell and used to
	// hold the port closed for seconds — on a fresh root with no chats at all
	// it was most of the 8.1 s before listen. It is not needed to serve a
	// request: Settings reads the state when it is ready, the same treatment
	// the probes get. Nothing waits on it, and a failure is still said.
	go func() {
		signingContext, cancelSigning := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelSigning()
		if err := a.web.RefreshSigningState(signingContext); err != nil {
			log.Printf("inspect installed signatures: %v", err)
		}
	}()
	// web_search follows fetch_url. It shares that tool's
	// guarded uTLS transport, so the older tools retain their relative order.
	rememberTool := tools.NewRemember(a.memoryManager, a.bus)
	rememberTool.SetConfig(a.web.ConfigSnapshot)
	rememberTool.SetPlanner(a.web.PlannerNotes())
	fetchTool := tools.NewFetch(a.cfg.Tools.Fetch)
	delegateTool := tools.NewDelegate()
	callServiceTool := tools.NewCallService(a.cfg.Services)
	callServiceTool.SetShell(shellTool)
	a.cleanup(func() { callServiceTool.Close() })
	go callServiceTool.RunMCPMaintenance()
	// the named-credential store lives beside the connection credentials, under
	// the operator data root, and the tool reads it through one seam.
	vault := credential.NewVault(a.paths.Data)
	callServiceTool.SetVault(vault)
	entraManager := entra.New(vault)
	callServiceTool.SetTokenProvider("entra", entraManager)
	a.cronManager = cron.New(a.profileRoot, nil, nil)
	imageTool := tools.NewImage()
	imageTool.Configure(*a.cfg)
	processTool := tools.NewProcess(shellTool)
	a.cleanup(func() { processTool.KillAll() })
	toolRegistry := tools.New(
		fileIdentity.Wrap(tools.NewReadFile(a.cfg.Tools.ReadFile)),
		fileIdentity.Wrap(tools.NewListDir(a.cfg.Tools.ListDir)),
		fileIdentity.Wrap(tools.NewWriteFile(coordinator)),
		fileIdentity.Wrap(tools.NewEditFile(coordinator)),
		tools.NewSearch(
			fileIdentity.Wrap(tools.NewGrep(a.cfg.Tools.Grep, a.cfg.Tools.ListDir)),
			fileIdentity.Wrap(tools.NewGlob(a.cfg.Tools.FindFiles)),
		),
		shellTool,
		rememberTool,
		tools.NewRecall(a.memoryManager, func() tools.ChatHistoryReader { return a.writers }),
		fetchTool,
		tools.NewWebSearch(fetchTool, a.cfg.Tools.WebSearch),
		tools.NewRunScript(shellTool),
		callServiceTool,
		delegateTool,
		tools.NewCronjob(a.cronManager),
		imageTool,
		tools.NewTodo(),
		tools.NewAsk(),
		processTool,
		fileIdentity.Wrap(tools.NewCode(diagnostics)),
	)
	// the threshold under which a PDF is sent inline rather
	// than read from its extracted text.
	agent.SetInlineDocumentLimit(a.cfg.Tools.Attachments.InlineDocumentLimit())
	a.runner = agent.NewRunner(a.bus, toolRegistry, a.renderer, a.web.Connection, a.web.ConfigSnapshot)
	a.runner.SetDelegateFileCoordinator(coordinator)
	a.runner.SetTrustedFolderWriter(a.web.TrustFolders)
	a.runner.Gate().SetStandingGrantStore(filepath.Join(a.paths.Data, "standing-grants.json"))
	callServiceTool.SetConnectorWriter(a.web.ApplyConnector)
	webserver.SetCredentialVault(vault)
	webserver.SetIdentityProvider("entra", entraManager)
	a.runner.BindDelegate(delegateTool)
	a.runner.SetSessionRenamer(a.registry.RenameBy)
	deliveryManager := delivery.New(a.bus, a.web.ConfigSnapshot)
	a.runner.SetDeliverer(func(item *session.Session, runID string, files []delivery.Source) delivery.Result {
		return deliveryManager.Deliver(item, runID, files)
	})
	a.scheduler = agent.NewScheduler(a.runner, a.registry, a.bus, a.web.ConfigSnapshot)
	a.scheduled = &scheduledRuns{profile: a.profileRoot, cfg: a.web.ConfigSnapshot, registry: a.registry, scheduler: a.scheduler, bus: a.bus, web: a.web, sessions: map[string]string{}}
	a.cronManager.SetRunner(a.scheduled.run)
	a.cronManager.SetHooks(nil, a.scheduled.finish)
	cronContext, cancelCron := context.WithCancel(context.Background())
	a.cleanup(cancelCron)
	go a.cronManager.Run(cronContext)
	a.runner.SetMailboxBoundary(func(_ context.Context, sessionID string, approvalPending bool) agent.BoundaryAction {
		action, err := a.operatorFiles.CheckInbox(sessionID, approvalPending)
		return agent.BoundaryAction{Stop: action.Stop, Revision: action.Revision, Delay: action.Delay, Err: err}
	})
	a.runner.Gate().SetMailboxDecision(func(sessionID string) (string, error) {
		action, err := a.operatorFiles.CheckInbox(sessionID, true)
		if action.Stop {
			a.scheduler.Stop(sessionID, false)
			return "deny", err
		}
		return action.Decision, err
	})
	a.web.SetRuntime(a.scheduler, a.runner, a.renderer)
	a.web.StartConnectionHealth(context.Background())
}
