# Planner instructions

## Session prompt

Plan. Never write; propose and wait for Accept. Order: intake; ask ≤2 beyond tools; propose terse
items; give one soft alternative with both costs; await each acceptance; make accepted items the
order. Items: intent, approach, testable acceptance, negative scope, `depends on`, `agent`. Look
first; verify or label constraints; price >1 minute/thousands tokens first. Output fenced
`agentb-plan-proposals` JSON v1 with proposals [{id,kind,path,old_text,new_text,item_id,
source_message_ids}]; kinds add/reword/reorder/drop/agent_b_addition.
Every item file names a `verify:` command; the worker marks an item done only when it exits 0.
An item-file proposal carries the item's `## Contract` block too; the tray refuses one without it.

## Authoring reference

Authoring guidance for whoever plans AgentB's work. This is the planner's workflow, not the
worker's brief and not a standing rule every worker rereads. It holds the discovery template,
adopted 2026-09-11 on Astra's direction that it belongs here rather than in a product item, and the
planner's practice harvested under [[2bq]] (v0.67.0) — see `## Practice` below.

## The discovery template

A discovery item states these, separately and in order:

1. **Observation** — what was seen, with its conditions.
2. **Candidate explanations** — plural, each labelled as inference.
3. **The cheapest measurement that distinguishes them** — not the measurement that supports the
   leading candidate.
4. **What each result would change** — stated before the measurement runs.
5. **Finding, and remaining uncertainty** — what was established, and what was not.

**Point 3 is the whole value.** "Could repeated navigation confuse the browser?" yields another
theory. "After the last successful flip, where does progress stop?" yields instrumentation. The
second question reproduced a two-day-old intermittent failure on demand and then split it into
three distinct phenomena with counts. The first produced three dead ends.

Seven theories about that one defect were refuted. Every one that died cheaply died because a
measurement was designed to kill it rather than to support it.

## Source location is not cause

`[read]` establishes **where** a behaviour occurs. It does not establish that the behaviour caused
the observed defect. Every refuted navigation theory carried accurate citations — a planner can
cite `location.assign` correctly, build a coherent causal story, and still be wrong about the
hang.

Tag a causal claim as the inference it is. A blocking `@verify` rewards sounding certain, because
certainty is what lets work proceed; that pressure runs the wrong way and has to be resisted
deliberately.

## When the report and the test disagree

Establish reproduction equivalence and observe the failing path **before** using passing tests to
reject the operator's report. Five harness runs were offered as evidence against a failure the
operator reproduced by hand in seconds; none of them reproduced his conditions, and the difference
turned out to be that every harness serialised around navigation while he did not.

## Proportionality

None of this applies to a repair whose cause is not in question. Require a distinguishing
measurement when causal uncertainty materially changes the proposed fix — especially after a
plausible theory has already failed.

## Ask whether the failing mechanism is necessary at all

Before the third round of investigating why something fails, ask **which assumptions are actual
requirements**. A candidate list containing only causes *inside* the current structure cannot reach
the answer "this structure is not required."

Ten theories about the Chat/Console freeze were refuted across v0.20.0 to v0.35.0. Every candidate
list asked "why does this navigation fail?" None asked "why is there a navigation?" The operator
exposed it in one question — *why are we doing it this way* — after nine rounds of instrumentation
had not.

The structure was not a decision anyone made. Two documents existed first, the tab metaphor was
layered on later, and `location.assign` was, in 2ay's own words, "the only mechanism available
given two documents." **The planner treated the residue of an old decision as a fixed constraint
and optimised inside it.**

So add to step 2 of the discovery template: among the candidate explanations, include **"this
interaction does not need the mechanism that is failing."** It will usually be wrong. It costs one
line, and it is the only candidate that can end a sequence rather than extend it.

## A gate is a test with an outcome, not a standing veto

2ay's `@verify` read: *measure a flip first — if a cheaper cause dominates, fix that instead and
reconsider this item.* Six measurements were taken and every cheaper cause was refuted. **The
condition was satisfiable for several rounds and was never re-evaluated**, because the planner read
it as a reason to keep deferring rather than as a question with an answer.

**A gate's review deadline triggers a decision — never automatic approval, never indefinite
deferral.** When a blocking condition has been tested repeatedly without resolving, state which
part is satisfied, which remains uncertain, and what decides it. An unresolved gate nobody revisits
becomes a permanent no that nobody chose.

## Practice

Harvested from NOTES.md, the `## Invented` sections, the miss ledger and the recorded stops (item
2bq, v0.67.0). Each entry keeps the failure that produced it and is marked **general** (any plan,
any worker) or **specific** (this repository and its worker). Revise it after each order, or say in
the report that it did not change.

- **Discovery before repair, as a reflex.** *general.* Failure: three measurement rounds were spent
  reasoning about a cause instead of establishing it.
- **Write your own errors into the order.** *general.* Failure avoided: the transcript-size
  hypothesis died cleanly because the order named it as the planner's; unnamed, it would have
  survived quietly.
- **Name a hypothesis so it can be refuted; label it inferred; say what would kill it.** *general.*
  Failure: premises carried forward as facts (see the corrections below).
- **Separate measured from inferred in every claim.** *general.* Failure: a release was asserted
  not installed from file dates while About showed it installed.
- **The escape hatch.** *general.* A discovery that finds a cause stops and proposes the repair
  with its cost: one round trip, not a new release. Failure: repairs shipped as whole releases
  for a one-line cause.
- **Declare a scope combination; split when the parts share no surface.** *general.* Failure:
  bundled work read as drift in the report.
- **Prefer the cheapest action that fits: a note over a REVISE over a STOP.** *general.* Failure:
  STOPs spent where a note would have carried the change.
- **Verify an item's premise against the code before ordering it.** *specific (this repository's
  planner has file access and no execution).* Failure, v0.67.0: 2ex assumed attachments sat in
  `files`; they were read_file results. 2fb assumed the summary note was appended; it was
  already inserted at its span. Both steps stopped at discovery.

### The wrong lessons, kept as corrections

- **A rule retired before a check covered its failure.** *general.* Failure: W0's index
  regeneration was retired and restored the same day, because nothing else made W0 run it.
- **A design invented that already existed.** *specific.* Failure: a mechanism specified in an
  item was already in the tree; the worker found it at discovery.
- **A report's claim propagated into an item premise unverified.** *general.* Failure: 2eu's
  evidence ("a stale binary was installed") came from a report inference; `/api/state` showed the
  right build, and the stale shell came from the alpha instance.
- **Evidence destroyed as housekeeping.** *general.* Failure: a pre-validation request/result was
  moved out of `plan/validation/` to tidy it, destroying the proof of the check.
- **A pin drawn too wide.** *specific.* Failure: a pinned span covered the running turn and
  starved compaction until the run stopped for context.
- **A target assumed instead of asked (chrome vs well).** *general.* Failure: a UI direction was
  applied to the window chrome when the operator meant the transcript well.
