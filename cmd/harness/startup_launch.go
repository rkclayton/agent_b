package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"harness/internal/buildinfo"
)

// launchFlags are the command line, parsed once by parseLaunchFlags.
type launchFlags struct {
	configOverride, applicationOverride, dataOverride, replayPaths, startupLog *string
	version, window                                                            *bool
	install, installQuiet, installAllUsers, reopenWindow, noStart              *bool
	installSource, installData, reopenSession, inspectInstallerPath            *string
	uninstall, uninstallWorker, purgeData                                      *bool
	uninstallParent                                                            *int
	uninstallRegistry, startMenuRoot, sendToRoot                               *string
	serviceHelper, serviceResult                                               *string
	passthrough                                                                []string
}

func parseLaunchFlags() launchFlags {
	var f launchFlags
	f.configOverride = flag.String("config", "", "configuration file (overrides AGENTB_CONFIG and installed/default locations)")
	f.applicationOverride = flag.String("app-root", "", "application root containing web, prompts, scripts, and harness.example.json")
	f.dataOverride = flag.String("data-root", "", "user data root containing configuration, credentials, logs, and memory")
	f.replayPaths = flag.String("replay", "", "comma-separated session JSONL files to replay")
	f.startupLog = flag.String("startup-log", "", "optional append-only startup diagnostic log")
	f.version = flag.Bool("version", false, "print this build's identity as JSON and exit")
	// our own window. One process and one binary still:
	// this is a mode of the server, not a second executable, and the browser
	// path stays supported for every case where the runtime is absent.
	f.window = flag.Bool("window", false, "host the page in Agent_b's own window (Windows, needs the WebView2 runtime)")
	// install mode. Everything after --install is the installer's,
	// so it is taken before flag.Parse and passed through untouched — the
	// PowerShell installer's parameters stay its own and cannot drift from a
	// second copy of them here.
	f.install = flag.Bool("install", false, "install Agent_b from this folder; everything after it is passed to the installer")
	f.installQuiet = flag.Bool("quiet", false, "with --install: print the installer's output to this console (the suite's path)")
	f.installSource = flag.String("install-source", "", "with --install: the candidate folder to install from (default: this executable's folder)")
	f.installData = flag.String("install-data", "", "with --install: the user data root that carries the marker and progress")
	f.installAllUsers = flag.Bool("all-users", false, "with --install: install machine-wide under Program Files (requires elevation)")
	f.reopenSession = flag.String("reopen-session", "", "with --install: reopen this chat after the installed app starts")
	f.reopenWindow = flag.Bool("reopen-window", false, "with --install: return an app-started update to its native window")
	f.inspectInstallerPath = flag.String("inspect-installer", "", "report whether an installer file carries the pinned release signatures")
	f.noStart = flag.Bool("NoStart", false, "with --install: install without starting Agent_b")
	f.uninstall = flag.Bool("uninstall", false, "remove the per-user Agent_b installation")
	f.uninstallWorker = flag.Bool("uninstall-worker", false, "complete a native uninstall after the installed process exits")
	f.uninstallParent = flag.Int("uninstall-parent", 0, "parent process to await before native uninstall")
	f.uninstallRegistry = flag.String("uninstall-registry-path", "", "with --uninstall: exact per-user registration to remove")
	f.startMenuRoot = flag.String("start-menu-root", "", "with --uninstall: exact Start-menu root used by the install")
	f.sendToRoot = flag.String("send-to-root", "", "with --uninstall: exact SendTo root used by the install")
	f.purgeData = flag.Bool("purge-data", false, "with --uninstall: remove user data")
	f.serviceHelper = flag.String("service-helper", "", "elevated native service-identity request")
	f.serviceResult = flag.String("service-result", "", "elevated native service-identity result")
	f.passthrough = installPassthrough(os.Args[1:])
	if err := flag.CommandLine.Parse(installFlagArgs(os.Args[1:])); err != nil {
		log.Fatal(err)
	}
	return f
}

// runOneShotMode runs a mode that does its one job and ends the process; it reports whether it did.
func runOneShotMode(f launchFlags) bool {
	if *f.inspectInstallerPath != "" {
		line, err := inspectInstaller(*f.inspectInstallerPath)
		fmt.Println(line)
		if err != nil {
			os.Exit(1)
		}
		return true
	}
	if *f.install || setupExecutable(os.Args[0]) {
		os.Exit(runInstall(installOptions{
			quiet:         *f.installQuiet,
			sourceDir:     *f.installSource,
			dataRoot:      *f.installData,
			noStart:       *f.noStart,
			allUsers:      *f.installAllUsers,
			reopenSession: *f.reopenSession,
			reopenWindow:  *f.reopenWindow,
		}, f.passthrough))
	}
	if *f.uninstall || *f.uninstallWorker {
		if err := runNativeUninstall(*f.applicationOverride, *f.dataOverride, *f.startMenuRoot, *f.sendToRoot, *f.uninstallRegistry, *f.purgeData, *f.uninstallWorker, *f.uninstallParent); err != nil {
			log.Fatal(err)
		}
		return true
	}
	if *f.serviceHelper != "" {
		if err := runServiceHelper(*f.serviceHelper, *f.serviceResult); err != nil {
			log.Fatal(err)
		}
		return true
	}
	if *f.version {
		if err := json.NewEncoder(os.Stdout).Encode(buildinfo.Current()); err != nil {
			log.Fatal(err)
		}
		return true
	}
	return false
}

// prepareLaunch opens the startup log, resolves the paths and records the crash context; it
// reports false when an existing instance was activated and this process should end.
func (a *harnessApp) prepareLaunch() bool {
	if strings.TrimSpace(*a.flags.startupLog) != "" {
		file, openErr := os.OpenFile(filepath.Clean(*a.flags.startupLog), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if openErr != nil {
			log.Fatalf("open startup diagnostic log %s: %v", *a.flags.startupLog, openErr)
		}
		// A GUI-subsystem launch has no stderr handle. Put the durable file first:
		// io.MultiWriter stops at the first failed writer, and stderr-first made a
		// startup failure's diagnostic file empty when no console existed.
		log.SetOutput(io.MultiWriter(file, os.Stderr))
	}
	paths, err := resolveStartupPaths(*a.flags.configOverride, *a.flags.applicationOverride, *a.flags.dataOverride)
	if err != nil {
		log.Fatal(err)
	}
	a.paths = paths
	// before anything that can fault. The data root is the first
	// thing a crash record needs and this is the first line that knows it.
	build := buildinfo.Current()
	installCrashRecord(a.paths.Data, map[string]any{"tag": build.Tag, "commit": build.Commit, "dirty": build.Dirty, "executable_sha256": build.ExecutableSHA256}, *a.flags.window)
	// an installed launch is one application, not a race to bind the
	// same port. The serving process's marker names its install root and port;
	// a second window launch validates both, asks that exact process to foreground
	// its window, writes one launcher line, and exits before config work or a bind.
	if *a.flags.window && strings.TrimSpace(*a.flags.replayPaths) == "" {
		if pid, activated := activateExistingInstance(a.paths.Data, a.paths.Application); activated {
			message := fmt.Sprintf("Agent_b activated existing PID %d and exited; no new server started.", pid)
			appendLauncherMessage(a.paths.Data, message)
			log.Print(message)
			return false
		}
	}
	// an install that did not finish says so, once, at the next
	// launch — the user asked "should i re-run?" and nothing could answer
	// him. The marker is only ever cleared by an install that completed.
	if marker, found, markerErr := readInstallMarker(a.paths.Data); markerErr != nil {
		log.Printf("install marker: %v", markerErr)
	} else if found {
		log.Printf("INTERRUPTED INSTALL: %s", describeInterruptedInstall(marker))
	}
	return true
}
