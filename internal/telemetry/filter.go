// Package telemetry sends diagnostic counts to a receiver the operator runs.
//
// Item 2jg. Three things make an opt-out defensible, and all three live here:
// an ALLOW-LIST rather than a deny-list, so a new event type is silently
// dropped and loudly unclassified; REDACTION applied to everything that leaves,
// as a belt against a field that turns out to carry more than it should; and an
// OFF SWITCH that detaches rather than filters.
//
// Item 2lr: the RECEIVER is the VPS. Nothing in this package receives anything.
package telemetry

import (
	"regexp"
	"strings"
)

// SchemaVersion is docs/TELEMETRY.md's version. The receiver rejects what it
// does not know, so this moves only when that document does.
const SchemaVersion = 1

// Classification is what the allow-list says about one event type. There are
// exactly two answers and "not mentioned" is not one of them: an unclassified
// type is a test failure, because absence is indistinguishable from an
// oversight, which is the same defect item 2lq found in a bench list.
type Classification struct {
	// Fields are the event-data keys that may be sent, in the order
	// docs/TELEMETRY.md lists them. An empty Fields with Sent true means the
	// event is sent with its type and time alone.
	Fields []string
	Sent   bool
}

// dropped is the classification for an event that never leaves.
var dropped = Classification{Sent: false}

func sent(fields ...string) Classification { return Classification{Fields: fields, Sent: true} }

// allowList is docs/TELEMETRY.md, as code. Every event type the product emits
// appears exactly once.
var allowList = map[string]Classification{
	// --------------------------------------------------------------- sent
	"run.stopped": sent(
		"reason", "turns",
		"total_ms", "model_ms", "tool_ms", "waiting_ms", "compaction_ms",
		"prompt_ms", "generation_ms",
		"retries", "compactions", "empty_replies", "repeated_calls",
		// Item 2lx: the two counts item 2ls's reflection floor reads are the two
		// counts telemetry sends. They are COUNTS -- how many model calls, how
		// many tool results -- and carry no tool name, no model name and no
		// argument content, which is what makes them sendable under 2jg's
		// contract at all.
		//
		// They come from the same map the floor read, so a disagreement between
		// what the floor did and what telemetry reported is not possible.
		"model_calls", "tool_calls",
		"model_class",
	),
	"tool.result": sent("name", "ok", "ms", "class"),
	"error":       sent("where", "class"),

	// ------------------------------------------------------------ dropped
	"model.delta": dropped, "model.progress": dropped, "model.request": dropped,
	"model.response": dropped, "model.retry": dropped, "model.unreachable": dropped,
	"model.reachable": dropped, "model.busy": dropped, "delegate.usage": dropped,
	"stage": dropped, "budget": dropped,
	"message.appended": dropped, "message.updated": dropped, "message.removed": dropped,
	"message.queued": dropped, "messages.reminted": dropped,
	"tool.call": dropped, "tool.toggled": dropped,
	"compaction": dropped, "compaction.summary": dropped,
	"session.created": dropped, "session.closed": dropped, "session.reopened": dropped,
	"session.renamed": dropped, "session.reset": dropped, "session.updated": dropped,
	"session.restore_failed": dropped, "chat.named": dropped, "chat.exported": dropped,
	"run.queued": dropped, "run.started": dropped, "run.stopping": dropped,
	"run.resumed": dropped, "run.labeled": dropped, "run.aborted": dropped,
	"approval.required": dropped, "approval.decided": dropped, "cycle.detected": dropped,
	"policy.approved": dropped, "policy.denied": dropped, "policy.revoked": dropped,
	"workspace.conflict": dropped, "workspace.bound": dropped,
	"memory.noted": dropped, "memory.cleared": dropped, "memory.flushed": dropped,
	"project.instructions_loaded": dropped,
	"connection.probed":           dropped, "probe.request": dropped, "config.changed": dropped,
	// Item 2nb (b): one per address an endpoint walk tries. It names the operator's own
	// server and what it answered, which is exactly the kind of thing that never leaves.
	"connection.discovering": dropped,
	"shell.identity":         dropped, "shell.credential": dropped, "shell.grant": dropped,
	"shell.grant_lapsed": dropped, "service.identity_unavailable": dropped,
	"file.grant": dropped, "file.grant_lapsed": dropped, "signing.applied": dropped,
	"operator.context": dropped, "ui.error": dropped, "subscriber.dropped": dropped,
	"log.retention": dropped,
	"item.done":     dropped, "item.stuck": dropped, "plan.done": dropped, "c.job": dropped,
	"stats.cleared": dropped, "files.delivered": dropped,
	"navigation.started": dropped, "navigation.measured": dropped,
	"navigation.suppressed": dropped, "navigation.document_started": dropped,
	"navigation.document_completed": dropped,
	"notification.failed":           dropped, "notification.changed": dropped,
	"update.changed": dropped, "progress.shadow": dropped, "progress.aux": dropped,
	"speech": dropped, "agent.connection_change": dropped,
	// projection.patch and snapshot are the same material: the whole conversation
	// as a client reads it. Dropped, emphatically.
	"projection.patch": dropped, "snapshot": dropped,
	// Item 2ls's accounting line. Dropped: it is two counts about one of the
	// operator's own runs and nothing outside this machine needs it.
	"reflection.skipped": dropped,
}

// Classify answers for one event type. The second result is false when the type
// has not been classified at all, which is the state the suite refuses to ship.
func Classify(eventType string) (Classification, bool) {
	value, ok := allowList[eventType]
	return value, ok
}

// ClassifiedTypes is every type the allow-list knows, for the test that compares
// it against the product's own list.
func ClassifiedTypes() []string {
	out := make([]string, 0, len(allowList))
	for name := range allowList {
		out = append(out, name)
	}
	return out
}

// ------------------------------------------------------------------ redaction

// The order matters: a URL contains a host, and a path can contain an email, so
// the wider pattern runs first and the narrower one cleans up what is left.
var redactions = []struct {
	pattern *regexp.Regexp
	with    string
}{
	// A URL, with or without a scheme's slashes.
	{regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"']+`), "<host>"},
	// A Windows absolute path, and a POSIX one.
	{regexp.MustCompile(`[A-Za-z]:[\\/][^\s"']*`), "<path>"},
	{regexp.MustCompile(`(?:^|\s)(/[^\s"']{2,})`), " <path>"},
	// A UNC path.
	{regexp.MustCompile(`\\\\[^\s"']+`), "<path>"},
	{regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.-]+`), "<email>"},
	// A bare hostname: at least two labels, a known-ish tail, no spaces.
	{regexp.MustCompile(`\b[a-zA-Z0-9-]+(?:\.[a-zA-Z0-9-]+)+\.[a-zA-Z]{2,}\b`), "<host>"},
	// A dotted-quad address.
	{regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`), "<host>"},
	// A token-shaped run: long, mixed, and not a word anyone writes.
	{regexp.MustCompile(`\b[A-Za-z0-9_\-]{28,}\b`), "<token>"},
}

// MaxStringLength is the cut every surviving string takes.
const MaxStringLength = 200

// Redact scrubs one string. It is applied to every string that leaves, including
// ones the allow-list believes are already safe — a class name is a fixed
// vocabulary today and this is what keeps that true tomorrow.
func Redact(value string) string {
	value = RedactFull(value)
	if len(value) > MaxStringLength {
		value = value[:MaxStringLength]
	}
	return value
}

// RedactFull applies exactly the same rules WITHOUT the length cut. Item 2mv's
// diagnostics export needs it for log lines: a cut stack trace is worth nothing,
// and duplicating these patterns anywhere else is how two redactors drift apart.
// Every caller that sends data off the machine wants Redact, not this.
func RedactFull(value string) string {
	for _, rule := range redactions {
		value = rule.pattern.ReplaceAllString(value, rule.with)
	}
	return strings.TrimSpace(value)
}

// Pick applies one classification to one event's data: the allow-listed fields,
// redacted, and nothing else. A field the event does not carry is ABSENT rather
// than zero — item 2ji's prompt_ms and generation_ms depend on that, and a
// receiver cannot tell a measured zero from a missing one otherwise.
func Pick(class Classification, data map[string]any) map[string]any {
	if !class.Sent {
		return nil
	}
	out := map[string]any{}
	for _, field := range class.Fields {
		value, present := data[field]
		if !present || value == nil {
			continue
		}
		if text, ok := value.(string); ok {
			out[field] = Redact(text)
			continue
		}
		out[field] = value
	}
	return out
}
