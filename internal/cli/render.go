package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"harness/internal/events"
)

// Renderer turns the event stream into what a person reads at a prompt.
//
// Item 2iz (a): one line per tool call — name, target, ok or error, ms —
// reasoning as a token count unless --verbose, the reply as text, and the
// terminal reason last. Item 2iz (b): with --json it is the event stream as
// JSONL instead, the same events the journal holds, so a script and the app
// read the same thing.
type Renderer struct {
	out     io.Writer
	errOut  io.Writer
	options Options
	// reply accumulates the assistant's text so the terminal shows one reply
	// rather than a stream of fragments.
	reply strings.Builder
	// runID is the run this renderer is following; events from any other run in
	// the same process are not this invocation's business.
	runID string
}

func NewRenderer(out, errOut io.Writer, options Options) *Renderer {
	return &Renderer{out: out, errOut: errOut, options: options}
}

// Follow reads events until the run stops, and answers with the stop reason.
func (r *Renderer) Follow(channel <-chan events.Event, runID string) (reason, detail string) {
	r.runID = runID
	for event := range channel {
		if event.RunID != "" && runID != "" && event.RunID != runID {
			continue
		}
		if r.options.JSON {
			r.emitJSON(event)
		} else {
			r.emit(event)
		}
		if event.Type == events.RunStopped {
			data, _ := event.Data.(map[string]any)
			reason, _ = data["reason"].(string)
			detail, _ = data["detail"].(string)
			return reason, detail
		}
	}
	return "", "the event stream ended before the run stopped"
}

// emitJSON writes the event as one JSONL line, unchanged. Item 2iz (b): the
// same events the journal holds, which is what makes `agentb --json` output
// replayable in the app.
func (r *Renderer) emitJSON(event events.Event) {
	encoded, err := json.Marshal(event)
	if err != nil {
		return
	}
	fmt.Fprintf(r.out, "%s\n", encoded)
}

func (r *Renderer) emit(event events.Event) {
	data, _ := event.Data.(map[string]any)
	switch event.Type {
	case events.ToolResult:
		fmt.Fprintln(r.out, ToolLine(data))
	case events.ApprovalRequired:
		// The prompt itself is the approver's business; this only marks the pause
		// so the terminal does not look hung.
		name, _ := data["name"].(string)
		fmt.Fprintf(r.errOut, "  … waiting on you: %s\n", name)
	case events.ModelResponse:
		if text, ok := data["content"].(string); ok && strings.TrimSpace(text) != "" {
			r.reply.WriteString(text)
		}
		r.emitReasoning(data)
	case events.ModelRetry:
		reason, _ := data["reason"].(string)
		fmt.Fprintf(r.errOut, "  … retrying (%s)\n", strings.ReplaceAll(reason, "_", " "))
	case events.Compaction:
		fmt.Fprintln(r.errOut, "  … summarising the conversation")
	case events.ModelUnreachable:
		host, _ := data["host"].(string)
		fmt.Fprintf(r.errOut, "  … model unreachable: %s\n", host)
	case events.RunStopped:
		if reply := strings.TrimSpace(r.reply.String()); reply != "" {
			fmt.Fprintf(r.out, "\n%s\n", reply)
		}
	}
}

func (r *Renderer) emitReasoning(data map[string]any) {
	reasoning, _ := data["reasoning"].(string)
	tokens, _ := data["reasoning_tokens"].(float64)
	if r.options.Verbose && strings.TrimSpace(reasoning) != "" {
		fmt.Fprintf(r.errOut, "  thought: %s\n", strings.TrimSpace(reasoning))
		return
	}
	if tokens > 0 {
		fmt.Fprintf(r.errOut, "  thought %d tokens\n", int(tokens))
	}
}

// ToolLine is one tool call's line: name, target, outcome, duration. It is
// exported because it is the format the acceptance reads, and a format only its
// author can check is not a format.
func ToolLine(data map[string]any) string {
	name, _ := data["name"].(string)
	ok, _ := data["ok"].(bool)
	milliseconds := numberOf(data["ms"])
	outcome := "ok"
	if !ok {
		outcome = "error"
		if class, _ := data["class"].(string); class != "" {
			outcome = "error:" + class
		}
	}
	target := ToolTarget(name, data)
	if target != "" {
		target = " " + target
	}
	return fmt.Sprintf("  %s%s → %s %s", name, target, outcome, formatMS(milliseconds))
}

// ToolTarget is the one argument worth showing for each tool: the path, the
// command, the query. Anything else is noise at a prompt.
func ToolTarget(name string, data map[string]any) string {
	args, _ := data["args"].(map[string]any)
	if args == nil {
		if preview, _ := data["preview"].(string); preview != "" && name == "" {
			return ""
		}
		return ""
	}
	for _, key := range []string{"path", "command", "query", "url", "pattern", "service", "source"} {
		if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
			return truncate(value, 60)
		}
	}
	return ""
}

func numberOf(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int:
		return int64(typed)
	case int64:
		return typed
	}
	return 0
}

func formatMS(milliseconds int64) string {
	if milliseconds < 1000 {
		return fmt.Sprintf("%d ms", milliseconds)
	}
	return fmt.Sprintf("%.1f s", float64(milliseconds)/1000)
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

// StopLine is the last thing printed: the terminal reason (item 2iw), in the
// plain words the chat uses, with the detail when there is one.
func StopLine(reason, detail string, elapsed time.Duration) string {
	words := strings.ReplaceAll(reason, "_", " ")
	if reason == "done" {
		return fmt.Sprintf("done in %s", formatMS(elapsed.Milliseconds()))
	}
	if strings.TrimSpace(detail) != "" {
		return fmt.Sprintf("stopped: %s — %s", words, detail)
	}
	return fmt.Sprintf("stopped: %s", words)
}
