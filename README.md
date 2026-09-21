# Agent_b

Agent_b is a small Go coding agent for OpenAI-compatible model servers: one binary, a dependency-free browser interface, durable multi-chat work, and observable tool-using runs.

It keeps planning and execution distinct. An operator or planner owns the ordered work in `PLAN.md`; the worker executes the current order, records durable findings in append-only `NOTES.md`, and stops at explicit decision boundaries. Chat is for the work itself, Console exposes the live run, and Settings owns connections and host controls.

![Agent_b Chat with the agent tab strip](docs/images/chat.png)

## Quickstart

1. Provide an OpenAI-compatible endpoint, or let the Windows setup wizard install one of its pinned GGUF/llama.cpp combinations locally. Agent_b can also use Ollama, LM Studio, vLLM, llama.cpp, or a hosted API. Go 1.24+ is needed only for source development.
2. On Windows, double-click **`Agent_b-setup.exe`** from the candidate folder. That is the deployment package and the normal way the product is installed: one double-click, one UAC prompt, no console window. Progress and the result appear in the product's own Setup page, and the install finishes even if you close the window it started from. Setup upgrades the one canonical installation, refuses a live second location, and maintains one Agent_b entry in Add/Remove Programs; uninstalling preserves your data by default. `install-Agent_b.cmd` remains beside it as a thin wrapper for anyone with the older habit, and is what the test suite drives; `start-Agent_b.cmd` builds and runs from a checkout.
3. Follow the four setup questions: connect or install locally (or defer), inspect/optionally measure capability, assign the profile to `b`/`c`/`d`, and open Chat. A connection becomes runnable when its context size and required capabilities are known.

Agent_b opens in **its own window**: the tab strip is the window's top edge, with the gear left of
Windows' own minimise, maximise and close buttons. That window is a mode of `Agent_b.exe` itself -
one process, one binary - hosting the same page over loopback through the WebView2 runtime. If the
runtime is not installed, or the pinned `WebView2Loader.dll` is not beside the executable, Agent_b
says so in `logs\launcher.log` and opens the page in an Edge application window instead; nothing
else changes.

The installed app opens Chat at `http://127.0.0.1:8790/chat`. Reopening its shortcut focuses the healthy instance instead of starting another one. Replay recorded sessions without a model with:

```text
go run ./cmd/harness -config harness.json -replay logs/main.jsonl
```

## What it provides

- Durable open and closed chats, workspace memory, compaction, concurrent scheduling, and model-profile failover for summaries.
- Exact context accounting when the server exposes `/tokenize` and `/apply-template`; otherwise every estimate is labeled.
- Eleven tools in stable order: `read_file`, `list_dir`, `write_file`, `edit_file`, `search`, `shell`, `remember`, `recall`, `fetch_url`, `run_script`, and `call_service`.
- A Windows service-account boundary for shell and file tools, with explicit operator-identity decisions when work needs the launching user.
- Local attachment ingestion, OCR/extraction sidecars, delivered-file links, replay, and append-only event evidence.

## Choose a serving path

Any compatible endpoint is enough for the agent loop. llama.cpp additionally supports the full accounting and performance instrumentation; copy `serve/local.env.example` to ignored `serve/local.env`, set `MODEL_PATH` and `LLAMA_SERVER`, then run `serve/start.ps1` or `serve/start.sh`.

The reliability probe under `serve/probes/reliability/` scores a candidate model on repeated tool-using repair tasks. Results describe that model and machine, not a general recommendation.

## Documentation

- [Detailed installation, operation, configuration, model setup, and uninstall reference](docs/REFERENCE.md)
- [Windows host-hardening runbook](docs/HARDENING.md)
- [Security boundary and threat model](SECURITY.md)
- [Events, APIs, configuration, and tool contracts](INTERFACES.md)
- [Browser design contract](web/DESIGN.md)
- [Operator-file mailbox and bookshelf](docs/OPERATOR-FILES.md)
- [Third-party notices](docs/THIRD_PARTY_NOTICES.md)

The normal Windows installer separates immutable application files under `%ProgramFiles%`, operator-owned state under `%LocalAppData%`, and the service workspace under `%ProgramData%`. UAC, ownership checks, upgrade preservation, launcher modes, cleanup, capability degradation, deferred boundaries, and the desktop-packaging decision remain documented in the [detailed reference](docs/REFERENCE.md).

## Development

```text
go test ./...
npm ci
npm run test:ui
```

Node and Playwright are development-only. The shipped browser code has no runtime dependencies, and the application remains one Go process and one binary.

## License

Agent_b is licensed under the [Apache License 2.0](LICENSE); attribution and trademark notices are in [NOTICE](NOTICE). Dependency licenses are listed in [third-party notices](docs/THIRD_PARTY_NOTICES.md).
