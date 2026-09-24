# Screenshot release gate

`tests/chat-acceptance.mjs` captures the twelve release surfaces with a
1250×975 headless Edge viewport and software rendering. Compare two independent
captures with:

```text
node tests/screenshot-gate.mjs BASELINE/baseline-initial CANDIDATE/baseline-initial
```

Stable UI is compared pixel-for-pixel. Differences of at most two channel
levels are reported as renderer rounding; this bound comes from the fixed
etching layer crossing one-pixel borders at x300/y147 and x301/y148. A normal
one-pixel mutation still reports `UNEXPLAINED` and fails.

Authorized dynamic rectangles are written beside each PNG. They follow the
exact changing value, not its row. In particular, About masks only the build
identity, server-started clock value, and update-checked clock value; labels,
punctuation, release status, controls, layout, and surrounding pixels remain
exact. Candidate rectangles are trusted only where they overlap the baseline's
same named region.

The microphone is not masked. Host speech probing can render its unchanged SVG
at disabled opacity, so the capture fixture waits for the probe and normalizes
availability before photographing it. Separate browser scenarios continue to
verify speech behavior. The region previously described as the close glyph at
x1219–1228/y915–928 is the microphone control.

Missing captures and every difference outside an authorized dynamic region or
the bounded rounding allowance are `UNEXPLAINED`; either result blocks an
ordinary release order.
