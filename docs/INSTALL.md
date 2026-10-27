# Install, upgrade and uninstall

The one page for how Agent_b gets onto a Windows machine, replaces itself, and leaves.
Item 2x0 wrote it after an upgrade on a second machine ended in a window that showed nothing.

## The two folders

| Folder | Holds | An uninstall |
|---|---|---|
| Program: `%LOCALAPPDATA%\Programs\Agent_b` | `Agent_b.exe`, `agentb.exe`, `web`, `prompts`, `scripts`, `docs` | removes it |
| Data: `%LOCALAPPDATA%\Agent_b` | `harness.json`, `profiles\` (chats, memory), `logs\`, credentials | keeps it, and says so once |

## Install and upgrade — `Agent_b-setup.exe`

The setup executable is `Agent_b.exe` with its files appended; started under that name it installs
(`cmd/harness/install_mode.go`, `runInstall`). The per-user path is native Go
(`cmd/harness/install_native.go`, `installPerUserNative`):

1. A running copy is asked to stop and waited for (`stopNativeInstalledProcess`). A copy that will not
   stop fails the install before anything changes.
2. Each folder is copied in beside the old one and swapped in by rename (`replaceDirectory`); a file
   another process holds is waited for, up to ten seconds. A failure leaves the old folder in place.
3. `harness.json` is created from `harness.example.json` only when there is none; an existing one is kept.
4. The app starts again. A setup he ran by hand always ends with the app open in its window, fresh
   or over the top; the in-app update does the same and reopens his chat.

Any failure shows a message box naming the reason and the install log
(`%LOCALAPPDATA%\Agent_b\logs\install-*.log`); it is safe to run the setup again.

## A configuration from an older release

`harness.json` carries `config_version`. One at or above the floor (`MinimumConfigVersion`,
`internal/config/load_save.go`) is migrated on the first start. One below it is renamed beside itself,
byte for byte, to `harness.json.schema<N>-<time>`, and the app starts with new settings from the
template, keeping the profile catalog so its chats and memory are found. The chat says so once.
Connections are entered again; their keys are still in the credential store under the same names.

## When the app cannot start

A launch from the Start menu has no console. If start-up fails, or the app has not answered within
120 seconds, a window titled "Agent_b could not start" shows the reason, the last lines of
`logs\launcher-errors.log` and the app's own last log lines, with one **Copy** button
(`cmd/harness/startup_failure.go`). It never appears when the start works. The window and its one
line in `launcher-errors.log` are the only report; the launcher adds a line only for an app that never
started.

To see the window, run this; it stops before reading any folder of yours:

```
& "$env:LOCALAPPDATA\Programs\Agent_b\Agent_b.exe" -window -startup-log "$env:TEMP\no-such-folder\startup.log"
```

## Uninstall

Installed apps runs the registered command, `Agent_b.exe --uninstall …`. It stops the app, removes the
program folder, the shortcuts and the registration, keeps the data folder, and ends with one notice
naming it. Installing again finds the chats and settings there.

## The gate

Hosted CI's `install-cycle` job (`tests/install-cycle.ps1`) runs all of this on a clean runner in
disposable folders: install and launch; an older configuration and the setup over the running copy;
an unreadable configuration named; the registered uninstall; a fresh install over the kept data.
`tools/tag-release.mjs` refuses a release tag until that job is green on the commit.
