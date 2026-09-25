# Agent_b detailed reference

Agent_b is a small Go coding agent for OpenAI-compatible model connections. It provides observable multi-session runs, thirteen governed tools, exact-or-labeled context accounting, compaction, durable workspace notes, and a unified Chat, Console, Plan, and Settings interface. The browser remains dependency-free.

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

The installed application opens Chat at `http://127.0.0.1:8790/chat`; its header switches among Chat, Console, Plan, and Settings. Settings has eight sections: Agents, Activity, Connections, Profiles, Chats, Notifications, Security, and About.

Replay one or more session logs without loading a model or enabling mutations:

```text
go run ./cmd/harness -config harness.json -replay logs/main.jsonl,logs/s2.jsonl
```

Connections hold an endpoint, model, sampling, reasoning, context settings, and measured capabilities. The settings sheet can add, duplicate, edit, test, and remove connections; full probes measure behavior while minimal/off modes label assumptions. A connection is runnable only with known context, streaming, structured tool calls, and non-truncating overflow behavior.

Compaction summaries use the optional `aux` connection when its fully rendered request fits that connection's context window. An unavailable, rejecting, or undersized aux connection falls back to the session's main connection; blank aux preserves the single-main-model path. Console timeline entries identify the serving connection and keep compaction inference input/output tokens separate from the main context-budget measurement.

Sessions are durable open-or-closed chats onto a workspace and may use different connections. Closing retains the JSONL, messages, workspace, memory, connection, and tool selection; it does not delete or stop work. Point several sessions at one workspace for a swarm; file-write conflicts force a re-read instead of silently overwriting another session. Agent_b schedules two runs by default, but a llama.cpp server started with `--parallel 1` interleaves their slot work instead of decoding two requests simultaneously.

With the service-account split enabled, shell and built-in file tools use the `agentb-svc` Windows identity. File tools accept absolute paths when that account's Windows permissions allow them; with the split disabled, they have the non-elevated operator's OS reach. Plan ownership and repository-policy paths remain protected in both modes. Shell is never workspace-confined: the workspace is only its initial working directory, so `cd ..` and absolute paths remain possible. Shell refuses execution-policy bypass, executable script artifacts, and execution of a script written through an agent file tool during the current run. An identity need raises **Run as you**; its chat grant is durable and revocable, uses the non-elevated operator, and never grants Administrator authority.

Settings → Security owns the longer-lived **Run everything as me for 20 minutes** control. Its off/on robot follows server events; while active, Chat's status strip shows the expiry time. Enabling runs subsequent shell and file tools as the non-elevated Windows account that launched Agent_b and explicitly defeats the service-account boundary; disabling is immediate. There is no absolute ceiling: the grant lapses after 20 minutes without agent tool activity by default, every ordinary tool execution resets that idle deadline at start and completion, and process exit always revokes it. A single call running beyond the idle window can therefore lapse while still executing. Per-chat **Run as you** remains scoped to that chat and can be revoked from its status-strip robot.

Use `fetch_url` for public HTTP/HTTPS text. It sends GET requests without model-supplied headers, cookies, or credentials; extracts readable HTML; refuses binary responses and private, loopback, or link-local destinations; and marks every result as untrusted external data. Results are UTF-8-safe byte windows: pass the returned `next_offset` unchanged as `offset` to receive the next non-overlapping window. `read_file` supports the same explicit byte cursor and a separate one-based `line`/`lines` mode; both return numbered source text and a next cursor when more remains. `tools.fetch.allow_domains` optionally limits public domains; when it is empty, `deny_domains` still blocks the default IP-geolocation hosts, while a nonempty allowlist supersedes that deny list. `allow_internal_hosts` is an exact-host exception for deliberately configured private endpoints. Defaults are a 20-second request timeout, five redirects, a 2 MiB response cap, a 16 KiB return window, and a 64 KiB maximum window.

The Plan page's **Go** button runs the plan's accepted items one at a time and reads Stop while a worker runs.

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
| Connections | `roles.{main,aux}` and `connections[].{id,label,base_url,model,credential,request_timeout_s,probe_mode,sampling,reasoning,context:{n_ctx,reserve_output},system_prompt_override,capabilities}` |
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
