# Agent_b detailed reference

Agent_b is a small Go coding agent for OpenAI-compatible model connections. It provides observable multi-session runs, fifteen governed tools, exact-or-labeled context accounting, compaction, durable workspace notes, and a unified Chat, Plan and Settings interface. The browser remains dependency-free.

Choose one serving path before you start.

On Windows, double-click **`Agent_b-setup.exe`** for the normal per-user installation. It needs no UAC prompt and puts the application under `%LocalAppData%\Programs\Agent_b`, operator data under `%LocalAppData%\Agent_b`, and the service workspace under `%LocalAppData%\Agent_b-workspace`. `install-Agent_b.cmd` is the source/candidate compatibility wrapper. An explicit elevated `--all-users` install uses Program Files and ProgramData. Upgrades preserve data, the shortcut focuses an existing healthy instance, and an unresponsive process is reported rather than killed.

Developers can instead double-click **`start-Agent_b.cmd`** to build and run directly from the checkout. Both paths find Go on `PATH` or in the ignored local `.tools\go` directory. No PowerShell command is required.

The test suites also need `node_modules` (Playwright). One command rebuilds both ignored tool folders, from `package-lock.json` and from an installed Go at or above `go.mod`'s version, and removes nothing it did not create:

    powershell -NoProfile -File tools\rebuild-tool-folders.ps1

Remove a linked git worktree only with `tools\remove-worktree.ps1 -Path <worktree>`. `git worktree remove` descends through junctions, so a worktree given junctions to this checkout's `node_modules` or `.tools\go` takes their contents with it.

The normal hidden launcher wrapper owns the Agent_b process and exits with it; pass `-Console` for an attached visible console. An installed launch failure is appended to `%LocalAppData%\Agent_b\logs\launcher-errors.log`; a source launch writes beneath the checkout. Test automation should use `Agent_b.cmd -Detached -NoBrowser -NoPause` (or the same switches with `start-Agent_b.cmd`): the server starts in a hidden background process and the launcher returns after its readiness check. A detached server does not stop when its browser closes, so automation must stop the exact Agent_b process it started after confirming sessions are idle.

Agent_b runs in the operator's Windows session, so it survives a locked screen, a disconnected session and Modern Standby, but not a sign-out. A Fast Startup shutdown, which Windows may present as the PC going to sleep, is a sign-out. The installer therefore also adds an `Agent_b` entry to the operator's Startup folder, which starts it detached and without a window at the next sign-in, and does nothing if it is already running. It can be turned off under Task Manager → Startup apps, and the uninstaller removes it. Every exit Agent_b can observe is appended to `launcher-errors.log` with its reason: a sign-out or shutdown, a close request, a signal, or a server failure. An exit it cannot observe, such as a forced termination or power loss, is recorded at the next start as ended without a recorded reason.

Agent_b refuses to start with an elevated Administrator token. Membership in the local Administrators group is fine: double-click the launcher normally, without **Run as administrator** and outside an elevated terminal.

To remove the installed application, use **Settings → Apps → Installed apps → Agent_b → Uninstall**. The uninstaller asks whether to remove operator data and the service workspace; choosing **No** removes only the application and keeps both data roots for a later reinstall. A purge refuses to delete an operator data root owned by another Windows user. The service account and host firewall policy may be shared, so remove Host protections in Agent_b Settings before uninstalling if they are no longer needed.

## Path A — an endpoint you already run (~5 minutes)

Prerequisites: Go 1.24+ and an OpenAI-compatible endpoint you already run, such as Ollama, LM Studio, vLLM, or a hosted API. Nothing else is required: no GPU, CUDA, or model download. The first source build needs access to the Go module proxy (or a populated module cache) for the `fetch_url` tool's pinned dependencies; running the built binary does not.

```text
git clone <repo-url>
cd AgentB
go build -o Agent_b ./cmd/harness
./Agent_b
```

For a source checkout, the first start copies `harness.example.json` to the ignored local `harness.json`; the checkout remains both the application and data root for development. An empty first-run connection list opens `/setup`, a four-question guide also available from Settings: connect and Test a running endpoint, install one of the pinned local GGUF/llama.cpp combinations, or defer; inspect probed capability and optionally run the ten-brief N=1 measurement under its five-minute cap; assign connections to `b`, `c`, and `d`; then open Chat. Every question can be skipped. Local detection is read-only and reports hardware, known connections, accelerators, and interpreter access. Local installation downloads only the server-owned pinned catalog, verifies exact byte counts and SHA-256 before renaming or extracting, installs below the data root, creates a sign-in launcher, starts `llama-server` on a free loopback port, creates the connection, and probes it. The browser submits catalog IDs, never URLs or hashes. If an endpoint needs an API key, entering it stores the secret in a named user-scoped DPAPI credential; `harness.json` keeps only its `credential` reference. A connection is not runnable until its own context size is known. Startup never waits on a model probe; a successful explicit test stays labeled `ready`. Settings → Security can create the local service account, verify the identity split, and apply/verify the application, data, workspace, and outbound firewall policies through Windows UAC without running Agent_b itself as Administrator; progress and errors remain visible after refresh. Installed host protection uses the separated roots documented above. Follow the [Windows host hardening](HARDENING.md) sequence. Agent_b refuses to guess a context ceiling.

The system prompt includes the current date, OS timezone, and (when Windows provides it) a coarse region code. It does not request precise device location or invent a city; location-specific tasks should name their location when the OS does not provide one.

Without llama.cpp's accounting endpoints, the budget meter uses calibrated estimation instead of exact categories, the cached-token readout is hidden, and the prefill and tok/s readouts stay dark. The agent loop, tools, approvals, sessions, memory, compaction, chat, and replay remain available once the endpoint passes the baseline connection checks. See [Capability degradation](#capability-degradation) for the complete behavior.

## Path B — local llama.cpp with full instrumentation (~1 hour)

Prerequisites: Go 1.24+, an NVIDIA GPU with a CUDA 12.8+ driver, about 15 GB of free disk, `curl`, a current llama.cpp `llama-server`, and a GGUF model you supply.

Copy `serve/local.env.example` to the ignored `serve/local.env`, set `MODEL_PATH` and `LLAMA_SERVER`, and adjust `CTX`, `KV_TYPE`, `PORT`, or `MTP` if needed. Start the model with `serve/start.ps1` on Windows or `serve/start.sh` on Unix, then build and run Agent_b as in Path A; set the connection's model to the `MODEL_ALIAS` value before selecting **Test**.

Exact context accounting requires llama.cpp's `/tokenize` and `/apply-template` endpoints. This path exposes both, which is why it can provide the real per-category meter along with cached-token, prefill, and generation-rate instrumentation. Prompt 1 produces the machine-local `SERVING.md`; [SERVING.example.md](../SERVING.example.md) shows its public-safe Facts shape.

For either path, Node.js remains optional for building and running Agent_b. Development verification uses it for `node --check`, the dependency-free frontend unit tests, and Playwright acceptance tests. Run `npm ci` on a build or verification machine with Microsoft Edge installed, then `npm run test:ui`; Playwright is configured for the existing `msedge` channel and does not require a browser in the shipped application. The release browser gate (`tests/test-chat-acceptance.ps1`) runs that Edge headless by default, with scrollbars still drawn, so it passes on a locked or unattended desktop; `-Headless false` shows a run for watching and is never the release path. The paint counter (`the retained order evidence`) runs headed on its own, because headless Edge composites without painting, and it is not a release condition. Neither Node, Playwright, `node_modules`, nor a browser is installed or distributed by the Agent_b installer. The four IBM Plex WOFF2 files are committed under `web/assets/fonts/`, so building and serving the UI never contacts npm or another font host.

## Use Agent_b

The installed application opens Chat at `http://127.0.0.1:8790/chat`; a fixed, resizable list at left holds New chat, pinned chats, folders, unfiled chats, Archived, and the Settings action, while the header retains the connection and native window controls. Drag the list edge to resize it, drag fully left to hide it, and drag the left-edge handle to restore it. Settings opens as a bounded window over the dimmed Chat, with one X in its header, and has nine sections: Agents, Activity, Plan, Connections, Profiles, Chats, Notifications, Security, and About. Plan is drawn in that window like every other section.

Replay one or more session logs without loading a model or enabling mutations:

```text
go run ./cmd/harness -config harness.json -replay logs/main.jsonl,logs/s2.jsonl
```

Connections hold one server: its name, endpoint, credential, timeout, default model and concurrency. Each model listed beneath that connection keeps its own attachment, image, sampling, reasoning, context, prompt, capability and Eval settings. Picking another listed model resolves that model's stored settings, or creates them from server-published context metadata and the shipped defaults, without duplicating the connection. The settings sheet can add, duplicate, edit, test, and remove connections; Test, Eval and Recommended operate on the picked default model, full probes measure behavior while minimal/off modes label assumptions, and switching back restores that model's values. A connection is runnable only when its default model has known context, streaming, structured tool calls, and non-truncating overflow behavior.

Compaction summaries use the optional `aux` connection when its fully rendered request fits that connection's context window. An unavailable, rejecting, or undersized aux connection falls back to the session's main connection; blank aux preserves the single-main-model path. Settings -> Activity timeline entries identify the serving connection and keep compaction inference input/output tokens separate from the main context-budget measurement.

Two kinds of JSONL live under the data root and they are not the same thing. `chats\<id>.jsonl` is the DURABLE JOURNAL of one chat: one file per chat, appended to for as long as the chat exists, never rotated and never subject to log retention — it is the record. `logs\<id>-<timestamp>.jsonl` is an OPERATIONAL TAPE: a new generation is opened every time the application starts, seeded with a snapshot of the session so the generation can be read on its own, and those generations are pruned by `operator_files.log_retention_days` (30 by default). A launch therefore leaves one new tape per open chat, which is intended and is not the journal growing.

Startup reads each chat's PROJECTED STATE from `cache\projection\<id>.json` rather than projecting its whole journal, and records the byte offset it projected through; a journal that has grown since is carried forward by projecting only the records past that offset. The cache is rebuildable and holds nothing the journals do not: deleting it costs one slow launch. Measured on a 218 MB set of 33 chats, two of them 56 MB and 48 MB: projecting all of it took 6.2 seconds, and reading the projections takes 50 ms.

Sessions are durable open-or-closed chats onto a workspace and may use different connections. Closing retains the JSONL, messages, workspace, memory, connection, and tool selection; it does not delete or stop work. Point several sessions at one workspace for a swarm; file-write conflicts force a re-read instead of silently overwriting another session. Agent_b schedules two runs by default, but a llama.cpp server started with `--parallel 1` interleaves their slot work instead of decoding two requests simultaneously.

With the service-account split enabled, shell and built-in file tools use the `agentb-svc` Windows identity. File tools accept absolute paths when that account's Windows permissions allow them; with the split disabled, they have the non-elevated operator's OS reach. Plan ownership and repository-policy paths remain protected in both modes. Shell is never workspace-confined: the workspace is only its initial working directory, so `cd ..` and absolute paths remain possible. Shell refuses execution-policy bypass, executable script artifacts, and execution of a script written through an agent file tool during the current run. An identity need raises **Run as you**; its chat grant is durable and revocable, uses the non-elevated operator, and never grants Administrator authority.

Settings → Security owns the longer-lived **Run everything as me for 20 minutes** control. Its off/on robot follows server events; while active, Chat's status strip shows the expiry time. Enabling runs subsequent shell and file tools as the non-elevated Windows account that launched Agent_b and explicitly defeats the service-account boundary; disabling is immediate. There is no absolute ceiling: the grant lapses after 20 minutes without agent tool activity by default, every ordinary tool execution resets that idle deadline at start and completion, and process exit always revokes it. A single call running beyond the idle window can therefore lapse while still executing. Per-chat **Run as you** remains scoped to that chat and can be revoked from its status-strip robot.

Use `fetch_url` for public HTTP/HTTPS text. It sends GET requests without model-supplied headers, cookies, or credentials; extracts readable HTML; refuses binary responses and private, loopback, or link-local destinations; and marks every result as untrusted external data. Results are UTF-8-safe byte windows: pass the returned `next_offset` unchanged as `offset` to receive the next non-overlapping window. `read_file` supports the same explicit byte cursor and a separate one-based `line`/`lines` mode; both return numbered source text and a next cursor when more remains. `tools.fetch.allow_domains` optionally limits public domains; `deny_domains` optionally refuses listed public hosts when no allowlist is set. `allow_internal_hosts` is an exact-host exception for deliberately configured private endpoints. Defaults are a 20-second request timeout, five redirects, a 2 MiB response cap, a 16 KiB return window, and a 64 KiB maximum window.

The Plan page's **Go** button runs the plan's accepted items one at a time and reads Stop while a worker runs.

## Where your files and data live

Files created by a run appear as download chips and, by default, are also copied to `%USERPROFILE%\Agent_b`. The paperclip accepts local files or files from that folder. Operator state, chats, plans, memory and logs live under `%LocalAppData%\Agent_b`; the normal per-user application lives under `%LocalAppData%\Programs\Agent_b`. An all-users install (`--all-users`, elevated) puts the application under `%ProgramFiles%\Agent_b` and the shared workspace under `%ProgramData%\Agent_b`, with each operator's own data still under their `%LocalAppData%`.

A managed deployment can ship a machine already pointed at a connection: `-SeedConfiguration <fragment.json>` merges a JSON fragment into the configuration **on first install only**, so a redeploy over a machine someone is using changes nothing they have set. A fragment carrying an API key or any other credential value is refused at install; name a credential reference instead and each user supplies the key once. See `seed.example.json`.

### The command-line agent

An install carries a second executable, `agentb.exe`, in the same application
directory as `Agent_b.exe` and signed with it. It runs one task in the current
directory without the app — see the README for what it does.

**The installer does not put it on your PATH, and that is a decision, not an
oversight** (item 2lz). Four things were possible: a per-user PATH entry, a
machine-wide one, a shim dropped in a directory Windows already has on PATH, or
nothing. Nothing won, for three reasons:

- **An installer that edits PATH is the most surprising thing it can do on a
  machine nobody is watching.** Agent_b can be deployed by a management tool and
  re-run on every pass; PATH is shared, ordered, length-limited state, and a
  deployment that appends to it repeatedly is a well-known way to break a
  machine that was working.
- **The shim option means writing into `%LocalAppData%\Microsoft\WindowsApps`**,
  which is already on your PATH — but it is Microsoft's App Execution Aliases
  directory, not ours, and it has no all-users equivalent, so it would serve one
  install mode and not the other.
- **Adding it yourself is one line**, and it is your PATH.

So: run it by its path, or add the directory once.

**Per-user install** — the application lives at
`%LocalAppData%\Programs\Agent_b`:

    & "$env:LOCALAPPDATA\Programs\Agent_b\agentb.exe" "your task"

To put it on your PATH permanently, from any PowerShell — no elevation, no
reboot, and new shells pick it up:

    [Environment]::SetEnvironmentVariable('Path',
      [Environment]::GetEnvironmentVariable('Path','User') + ';' +
      "$env:LOCALAPPDATA\Programs\Agent_b", 'User')

**All-users install** — the application lives at `%ProgramFiles%\Agent_b`:

    & "$env:ProgramFiles\Agent_b\agentb.exe" "your task"

Adding that one to the machine PATH needs an elevated shell, and is the same
call with `'Machine'` in place of `'User'`.

Either way, `agentb --help` from a fresh shell tells you the rest.

## Updates

Agent_b checks for a release hourly and reports one in About. Selecting **Update** downloads the signed installer, verifies its digest and its Authenticode signature, and only then launches it; it never installs silently. An update installs where the instance asking for it lives. Uninstall preserves operator data unless purge is explicitly selected.

## Bring your own model

`serve/probes/reliability/` is an onboarding check for the question “can my model handle tool calling well enough?” It generates a small Go repair fixture, runs two tool-using tasks three times each, and reports a score as passes out of six using explicit completion, tool-choice, argument, and turn-count rules.

Provide two candidate GGUF paths and run the platform script:

```text
# PowerShell
$env:C1_MODEL = '<first-candidate.gguf>'
$env:C2_MODEL = '<second-candidate.gguf>'
.\serve\probes\reliability\run.ps1

# Unix shell
C1_MODEL='<first-candidate.gguf>' C2_MODEL='<second-candidate.gguf>' serve/probes/reliability/run.sh
```

The generated JSONL, workspaces, server logs, score sheets, and summary stay under the ignored `serve/probes/reliability/runs/` directory. Use the numeric result as evidence for your own model and hardware rather than as a general model recommendation.

## Capability degradation

| Missing capability | Behavior |
|---|---|
| `/tokenize` | Budget categories use a calibrated estimate, remain visibly estimated, and retain a guard margin. |
| `/apply-template` | Per-role and schema overhead use documented estimates; no value is presented as exact. |
| tool-aware `/apply-template` | The tools category alone is estimated. |
| cached-token reporting | The cached-token readout is hidden rather than displayed as zero. |
| timings or prompt progress | Prefill and tok/s readouts stay dark; elapsed time remains available. |
| structured tool calls, streaming, or known context | The connection is `not_runnable` and states what must be fixed. |
| silent context truncation | The connection is refused; Agent_b never silently truncates a prompt. |

## Configuration

Startup locates configuration in this order: an explicit launcher `-config` argument, `AGENTB_CONFIG`, `%LocalAppData%\Agent_b\harness.json` when it exists, then `harness.json` relative to the current directory as the deliberate development fallback. The first-run template is resolved from the application root, independently of the live config location. Installed relative log and memory paths resolve beneath the LocalAppData data root; the per-user workspace defaults to `%LocalAppData%\Agent_b-workspace`.

| Area | Keys |
|---|---|
| Process | `listen`, `workspace`, `log_dir` |
| Connections | `agents[].{b,c,d}` and `connections[].{id,label,base_url,extract_url,model,credential,request_timeout_s,probe_mode,max_concurrent,models:[{model,attachment_handling,reads_images,sampling,reasoning,context:{n_ctx,reserve_output},system_prompt_override,capabilities,measurement}]}` |
| Runs | `run.{max_turns,max_wall_clock_seconds,max_tool_calls,cycle_window,max_consecutive_tool_errors,max_concurrent,queue_depth}`, `approval.mode` |
| Context and memory | `context.{soft_pct,summary_pct,accounting}`, `memory.{enabled,dir,max_tokens}` |
| Operator files | `operator_files.{allow_mailbox_approvals,log_retention_days}` (mailbox approvals default off; live-log retention defaults to 30 days and never prunes evidence archives) |
| Tool caps | `tools.{read_file,list_dir,grep,shell,fetch,web_search,find_files,attachments}`, `services`, `sandbox`, and `shell` (the service identity defaults enabled but must be provisioned once; operator context defaults off and lapses after 20 idle minutes) |
| Signing | `signing.{thumbprint,timestamp_url}` is retained release metadata for file-edited/operator tooling; the application has no signing UI or signing API |

See [web/DESIGN.md](../web/DESIGN.md) for the UI contract, [INTERFACES.md](../INTERFACES.md) for events and APIs, [operator files](OPERATOR-FILES.md) for the local mailbox/bookshelf convention, and [SECURITY.md](../SECURITY.md) plus [Windows host hardening](HARDENING.md) before granting a model shell access. The UI uses only the six-color industrial-console system; artwork and vendored fonts live under `web/assets/`.

`approval.mode` accepts `boundary-only` (the default; no generic confirmations), `mutating` (confirm `write_file`, `edit_file`, `shell`, and `run_script`), and `all` (confirm every tool call). The deprecated `off` value remains accepted as an alias for `boundary-only` but is hidden from Settings. A risky action under those rules uses **Allow this**; an operator-identity need uses **Run as you**. With the service account disabled, tools follow the selected mode normally. No mode disables the mandatory identity decision after a Windows identity or permission denial.

Dependency licenses and included transitive modules are recorded in [docs/THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## Deferred boundaries

- OS-level shell sandboxing; the shell is not jailed, and its file routing, deny list, approvals, and process-tree timeout are not a security boundary.
- API authentication. Agent_b intentionally binds loopback; an EventSource query-string token was rejected because it leaks through logs/history without addressing a present network threat. Authentication must be designed when `listen` moves off loopback.
- Dynamic MCP discovery and a Discord bot process. Operator-requested MCP/HTTP connectors can be added through the existing approval card and `call_service`; Agent_b does not discover or install connectors on its own.
- Orchestration or handoff between independent sessions, and a second local `--parallel 2` server slot.

## Decided against

**Desktop packaging (Electron/Tauri), 2026-09-04.** Evaluated and rejected. This is a decision, not a deferral. Packaging would have supplied global hotkeys, tray-resident operation with OS notifications, native file dialogs, taskbar progress and badges, single-instance enforcement, a pinned Chromium, and guaranteed freedom from background throttling.

Background throttling was fixed by reconciling operator state on every SSE open and foreground resume. Attachment ingest also needs no desktop package: drag-and-drop, paste, and the picker deliver bytes to the browser, which copies them into the chat's attachment folder without exposing the source path.

Against that, packaging adds a bundled Chromium and a Node toolchain to a project whose rules are one process, one binary, and a dependency-free browser; it makes Chromium patching a local responsibility; it assumes a local operator, which conflicts with any later move off loopback; and its main process runs as the operator with full Node access beside the OS-level boundary this project treats as its security model. The remaining benefits, tray residency, notifications, and an always-on-top chat window, are polish and do not carry the trade on their own. Revisit only if a requirement appears that a browser client genuinely cannot serve.
