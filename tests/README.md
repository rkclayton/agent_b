# Verification entry points

An order adds a gate only by naming it here with what it proves. One-off measurements run from
the order's evidence directory and are never committed.

## CI suites

| Gate | Entry point | Proves | Needs |
| --- | --- | --- | --- |
| Go unit and build-tag suites | `go test ./...` | Product packages compile and unit contracts hold | Go 1.24+; Windows runs Windows-tagged tests |
| Go vet | `go vet ./...` | Standard static checks pass | Go 1.24+ |
| Event race gate | `go test -race ./internal/events` | Event publication and subscriber shutdown are race-safe | Race-capable Go runner |
| Node unit suites | `node tests/run-node-tests.mjs` | Browser, plan-tool, helper, and workflow contracts hold | Node 24; no model or network |

## Operator release gates

| Gate | Entry point | Proves | Needs |
| --- | --- | --- | --- |
| Playwright | `npm run test:ui` | Connection editor and UI browser flows | npm dependencies and Edge; disposable roots |
| Chat acceptance and screenshots | `powershell -File tests/test-chat-acceptance.ps1` | Headless end-to-end chat and the 12 canonical surfaces | Edge; disposable install; no real model unless explicitly requested |
| Screenshot comparison | `node tests/screenshot-gate.mjs BASELINE CANDIDATE` | Pixels are exact, declared-mask-only, or explained rounding | Two capture directories |
| Installer matrix | `powershell -File tests/test-installer.ps1` | Candidate identity, single-file install, upgrades, rollback, launch, and exact runtime ship list | Windows; disposable roots; local test-signing identity |
| Capability acceptance | `go test ./internal/tools -run CapabilityWindowsLive` | Service-account and operator capability routing | Windows host policy and disposable configuration |
| Deploy gate | `powershell -File tests/test-deploy-gate.ps1` | Staging/signing/deploy ordering and manifest refusal | Windows; disposable fixtures, no publication |
| Release signing | `powershell -File tests/test-release-signing.ps1` | Test certificates and signing policy behave as contracted | Windows certificate stores; disposable test identity |
| Replay acceptance | `node tests/chat-replay-acceptance.mjs ...` | Recorded sessions render without a model | Disposable app/data roots and a replay tape |
| Connection replay | `node tests/connection-replay-acceptance.mjs ...` | Legacy connection data projects without loss | Disposable app/data roots and fixture data |
| Onboarding | `node tests/onboarding-acceptance.mjs ...` | Setup sequence and local capability choices | Running disposable harness and Edge |
| Real-template accounting | `node tests/accounting-real-template-acceptance.mjs ...` | Exact template/token accounting against a live model | Explicit model endpoint and network |

Kept source-text convention tests include `internal/signing/script_contract_windows_test.go`,
`cmd/harness/launcher_test.go`, `internal/buildinfo/release_consistency_test.go`, browser UI string
contracts under `web/js/*.test.mjs`, `tests/disposable-port.test.mjs`, and
`tests/ci-workflow.test.mjs`. They pin runtime filenames, the stable tool/product vocabulary,
production-port refusal, and the no-secrets CI boundary.
