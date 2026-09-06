# Projector golden masters

`manifest.json` is the selection record. Cases are chosen for distinct event shapes, not
volume; each entry records the ignored raw-log origin, its SHA-256, record count, covered
shapes, and why it belongs in the blocking set.

The checked-in `sources/*.events` files are deterministic, content-scrubbed projection
tapes. They preserve event order, sequence IDs, lifecycle values, cursor relationships,
tool inventories, numeric metadata, and result shapes. Free-form prompts, model text, file
contents, paths, URLs, commands, and diagnostic prose are replaced by stable hash markers.
Raw runtime JSONL is private and remains ignored.

Commands, run from the repository root:

- `go run ./cmd/projector-pins-update` regenerates only `golden/*.golden.json`. Review the
  resulting ordinary Git diff; this command is intentionally separate from `go test`.
- `go run ./cmd/projector-sweep` projects all session logs under the three approved local
  recording roots and compares them with `sweep-baseline.json`. Differences are reported but
  the command exits successfully, so the wide sweep is diagnostic rather than blocking.
- `go run ./cmd/projector-sweep -record` deliberately replaces the wide baseline.
- `go run ./cmd/projector-pins-import` recreates scrubbed sources only when the manifest's
  named raw files and exact hashes are present. It refuses paths outside the approved roots,
  including the protected alpha tree.

To add a case, first identify a missing event shape, add one manifest entry with its rationale
and source hash, import its scrubbed tape, regenerate pins, and review both diffs. Do not add a
near-duplicate merely to increase fixture count.
