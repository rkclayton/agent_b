# Agent_b

Agent_b is a Windows-first coding agent for OpenAI-compatible models. It is one Go process with a dependency-free browser UI, durable chats and plans, governed tools, context accounting, and an optional low-privilege service identity.

![Agent_b Chat](docs/images/chat.png)

## Install and first run

1. Download the signed [Agent_b-setup.exe](https://github.com/rkclayton/agent_b/releases/latest/download/Agent_b-setup.exe) and double-click it. The normal per-user install needs no UAC prompt or console window.
2. Open Agent_b from Start. Setup can connect an existing OpenAI-compatible endpoint, install a pinned local llama.cpp model, or defer model setup.
3. To use the Windows service-account boundary, approve **Set up service identity** once in Settings → Security. Until that succeeds, executing and mutating tools remain unavailable; you may instead disable the split deliberately.

The installed app is a singleton. Its own WebView2 window falls back to an Edge app window when the runtime or loader is unavailable. Reopening the shortcut focuses the healthy instance.

## Everything else

[Operator reference](docs/REFERENCE.md) — the surfaces, the tools, configuration, updates, and where your data lives.

## Source development

Go 1.24+ is required. `start-Agent_b.cmd` builds and runs a checkout. A compatible endpoint is enough; llama.cpp additionally enables exact template/token accounting when it exposes `/tokenize` and `/apply-template`.

```text
go test ./...
node tests/run-node-tests.mjs
npm ci
npm run test:ui
```

Node and Playwright are test-only dependencies. See the [operator reference](docs/REFERENCE.md), [hardening runbook](docs/HARDENING.md), [security model](SECURITY.md), [runtime contracts](INTERFACES.md), and [test gates](tests/README.md).

Apache-2.0. See [NOTICE](NOTICE) and [third-party notices](docs/THIRD_PARTY_NOTICES.md).
