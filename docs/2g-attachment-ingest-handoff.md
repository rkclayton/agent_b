# 2g attachment ingest — Fable handoff

Date: 2026-09-06  
Release: `v0.6.0`  
Release commit: `0db9ef2136709b1e3dfae5ba5dd3f7b51c8c07f9`

## Status

Attachment ingest is implemented, verified, pushed to `origin/main`, and tagged as the MINOR release `v0.6.0`. The tracked release tree was clean apart from the expected untracked `operatorbutton.zip`.

Production was deliberately not reinstalled. The request to reinstall conflicted with the same work order's explicit `Prod ... do not touch` and `Not installed to prod` instructions. Production PID 23252 / port 8790 and Alpha port 7337 / `C:\alpha` were untouched. A later production install requires an explicit override of that release boundary.

Implementation milestones:

- `44a09c2` — `feat: add chat attachment ingest`
- `0db9ef2` — `test: pin attachment ingest and replay`

## W0 discovery

### Non-UTF-8 `read_file`

`read_file` already refuses binary input. A NUL in the first 8 KiB returns `binary file refused`; otherwise invalid UTF-8 returns `text is not valid UTF-8`. The selected policy is refuse-with-note, not hexdump. Neither `read_file` nor the tool registry changed.

### Split workspace access

The HTTP server runs as the operator. The production-shaped workspace is operator-owned and grants `ASG01001\agentb-svc` inherited `Modify, Synchronize`, so direct HTTP ingest and later service-account reads are already permitted. No ACL change or write-file impersonation was needed.

### Multipart limit

The pre-existing 4 MiB `MaxBytesReader` applies to JSON decoding rather than globally. Attachment ingest uses streaming multipart parsing with `tools.attachments.max_bytes`, default 8 MiB, plus bounded multipart overhead.

### Composer drop target

Only `.window-titlebar` is the movable-window drag region. The composer is outside it and can accept file drops without conflicting with window movement.

## Delivered contract

- `POST /api/attachments` accepts one mutation-token-protected multipart `session_id`/`file` request and returns `path`, `bytes`, and `sha256`, plus tier information.
- Files land under `attachments/<sanitized-basename>` through `tools.Resolve`; no second jail implementation was added.
- Sanitization takes the basename, strips separators, control/Windows-invalid characters and leading dots, and refuses empty or Windows device names.
- Collisions receive numeric suffixes and never overwrite. A SHA-256-identical existing file is reused.
- `GET /api/exchange-files` projects regular top-level exchange-folder files and retrieves a selected basename. The browser never resolves a host path; selected bytes are fed through the same attachment ingest endpoint.
- Chat supports drag/drop, paste, picker, and exchange-folder selection. Pending attachments accompany the next message only.
- `/api/message` accepts attachment metadata and validates path, size, and SHA-256 against a regular direct child of the session attachment directory.
- `message.appended` carries attachments. Stored user text remains unchanged, and no new durable event type was added.
- Chat and timeline replay render download/open-folder chips. A file absent during replay renders `missing`.
- Request rendering adds one harness line per attachment. Native document/image bytes may be sent as content parts, but diagnostic model-request events remove those bytes before JSONL persistence.
- Text/code remains readable through `read_file`.
- DOCX/XLSX/PPTX receive a crude standard-library ZIP/XML `.txt` sidecar.
- PDF uses probed native document input, configured extraction into an explicitly untrusted sidecar, or a binary limitation note.
- Images use probed native image input or a binary limitation note.
- ZIP files are refused. No PDF dependency, OCR, or image-description implementation was added.
- `/api/files` was not changed.

Schema remains 5. The additive optional `tools.attachments.max_bytes` defaults to 8 MiB. Profiles gain optional `extract_url` plus probed `document_input` and `image_input` capabilities. The tool registry remains its existing 12 tools.

## Verification evidence

| Check | Result |
|---|---|
| `go test -count=1 ./...` | Pass |
| `go vet ./...` | Pass |
| Single `go build ./...` | Pass |
| Every browser JavaScript module under `node --check` | Pass |
| Browser tests | 53/53 pass |
| Default projector pins | Pass |
| `go test -count=1 -tags projector_slow ./internal/projectorpins` | Pass |
| `git diff --check` | Pass |
| `TestAttachmentIngestLiveServiceSplit` | Pass |
| `TestCapabilitySuiteLiveServiceSplit` | Pass |

Named sanitizer tests:

- `TestSanitizeAttachmentBasenameStripsPathsControlsAndLeadingDots`
- `TestSanitizeAttachmentBasenameRefusesEmptyAndWindowsDeviceNames`

Projector pin fixture: `attachment-message` in `internal/projection/testdata/pins/sources/attachment-message.events`.

The disposable split live instance ran at `127.0.0.1:59687`. Observed tiers were text → `text`, binary → `binary`, and DOCX → `office`. The model received the text `read_file` instruction and invoked it successfully as `agentb-svc`; the DOCX sidecar existed and was named in its harness line; binary was explicitly labeled unreadable for the profile. The fixture was removed.

Capability suite per-item result:

- Python write/run: **moved** behind an operator decision because the interpreter is operator-private.
- Node write/run: **unchanged/pass**.
- Project-test shell: **unchanged/pass**.
- Read/edit/search/find/list: **unchanged/pass**.
- Public fetch: **unchanged/pass**.
- Boundary decision: **unchanged/pass**.
- Multiline PowerShell: **unchanged/pass**.
- `call_service`: **unchanged/pass**; the exec child remained operator `asg01001\randy`.
- Removed capabilities: **none**.

## Follow-up cards

- Delete-attachment UI: define authority and lifecycle separately; no deletion route or UI exists.
- Attach to an already-dispatched run: pending files can accompany the next message, but cannot mutate a running request.
- OCR/image description: separate capability and trust decision.
- Sub-agent and memory attachment propagation: no automatic propagation or memory ingestion exists.
- `/api/files`: keep download delivery separate from ingest; any future widening needs its own work order.

Durable discovery and decision details are also appended to the gitignored operator record `NOTES.md` under `## 2g — attachment ingest`.
