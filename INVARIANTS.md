# Product invariants

These are release gates, not aspirations. The rule text changes only when the operator says it changes.

I1 — While Agent_b runs it owns exactly one window; no console and no second window from any way of starting it — 2026-10-01, “said 20+ times” — `tests/test-one-window-invariant.ps1`
I2 — Nobody picks a folder: no picker, no folder control, no typed path; the word “workspace” appears in nothing he sees — 2026-09-14 — `web/js/operator-language.test.mjs`
I3 — The agent cannot reach its own control plane or rewrite itself — 2026-09-24 — `internal/web/security_test.go:TestControlPlaneRequiresBrowserSession2jy`
I4 — Nothing in the product, its fixtures or its public pages is specific to a client — 2026-09-29 — `tests/docs-terms.test.mjs:the client-terms gate catches a planted term`
I5 — Nothing pushed names a model, tool or vendor; commits are his identity only — 2026-09-27 — `tools/check-invariants.test.mjs:public commits carry only the configured operator identity`
I6 — No button, menu, toggle or field exists that he did not ask for in his own words — 2026-09-14 — `web/js/settings-density.test.mjs`
I7 — Unattended mode and scheduled runs never ask; a would-be prompt is denied and recorded — 2026-09-20/2026-10-06 — `internal/agent/gate_unattended_test.go:TestUnattendedRefusesEveryCardKindAndRecordsIt`
I8 — Six colours, dark, no nested and no horizontal scrolling — standing since 0.x — `web/js/shell-contract.test.mjs`
I9 — Nothing the product, its installer or its tests start takes his screen or his focus — 2026-09-27 — `tests/docs-terms.test.mjs:no spawn site in the tooling can take the operator's screen`
I10 — Installing and updating never need elevation, and signing never lands on a user — 2026-09-23 — `cmd/harness/main_test.go:TestStartupElevationGuard`
I11 — Anonymous data is content-free and off when its switch is off — 2026-09-23 — `internal/telemetry/sender_test.go:TestTheCountsAreNotQueuedWhenTelemetryIsOff2lx`
I12 — A per-event path costs the same however much is stored; no file work on the UI thread; nothing grows without bound — 2026-10-01 — `tools/check-invariants.mjs:I12 engineering-floor suite`
I13 — Nothing proprietary to him, and especially nothing identifying, is in anything tracked, built or pushed — 2026-10-07 — `tools/privacy-gate.mjs:tracked, message and binary scan`
I14 — No release ships that cannot find the next one — 2026-10-07 — `tools/check-invariants.mjs:I14 release-source suite`
