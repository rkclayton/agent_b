package cli

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"harness/internal/events"
)

// Item 2iz (c): the two cards, as terminal prompts, with the same scopes.
//
// There are exactly two card kinds in this product and a third is a hard stop,
// so there are exactly two prompts here. The scopes are the app's — once, this
// run, this session — and the words are the app's words, because an operator
// who has read the card in the app should not have to learn a second vocabulary
// at a prompt.
//
// --unattended does not route through here at all. Item 5f's rule is that
// NOTHING ASKS, and the gate already enforces it; a prompt that appeared and
// then answered itself would be theatre.

// Decision is what the gate accepts back.
const (
	DecideOnce    = "once"
	DecideRun     = "run"
	DecideSession = "session"
	DecideDeny    = "deny"
)

// Approver answers approval cards at a prompt.
type Approver struct {
	in     *bufio.Reader
	out    io.Writer
	decide func(sessionID, callID, decision string) error
}

func NewApprover(in io.Reader, out io.Writer, decide func(sessionID, callID, decision string) error) *Approver {
	return &Approver{in: bufio.NewReader(in), out: out, decide: decide}
}

// Handle answers one approval.required event. It returns false when the event is
// not an approval, so a caller can pass the whole stream through it.
func (a *Approver) Handle(event events.Event) bool {
	if event.Type != events.ApprovalRequired {
		return false
	}
	data, _ := event.Data.(map[string]any)
	callID, _ := data["call_id"].(string)
	if callID == "" {
		return false
	}
	fmt.Fprint(a.out, Card(data))
	decision := a.read()
	if err := a.decide(event.SessionID, callID, decision); err != nil {
		fmt.Fprintf(a.out, "  the decision was not accepted: %v\n", err)
	}
	return true
}

func (a *Approver) read() string {
	for {
		fmt.Fprint(a.out, "  [o]nce  this [r]un  this [s]ession  [n]o: ")
		line, err := a.in.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			// A closed stdin is not consent. Anything that cannot be answered is
			// refused, which is the same way the unattended rule falls.
			fmt.Fprintln(a.out, "n (nothing to read from)")
			return DecideDeny
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "o", "once", "y", "yes":
			return DecideOnce
		case "r", "run":
			return DecideRun
		case "s", "session":
			return DecideSession
		case "n", "no", "d", "deny":
			return DecideDeny
		}
	}
}

// Card renders one approval as the two-card vocabulary: what it wants to do, as
// whom, and under which rule.
func Card(data map[string]any) string {
	name, _ := data["name"].(string)
	args, _ := data["args"].(map[string]any)
	var builder strings.Builder
	builder.WriteString("\n")
	if isOperatorCard(name, args) {
		builder.WriteString("  RUN AS YOU\n")
	} else {
		builder.WriteString("  ALLOW THIS\n")
	}
	builder.WriteString(fmt.Sprintf("  %s", name))
	if target := ToolTarget(name, map[string]any{"args": args}); target != "" {
		builder.WriteString("  " + target)
	}
	builder.WriteString("\n")
	for _, key := range []string{"identity", "reason", "rule", "scope"} {
		if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
			builder.WriteString(fmt.Sprintf("  %-9s %s\n", key, truncate(value, 90)))
		}
	}
	return builder.String()
}

// isOperatorCard distinguishes the two kinds. "Run as you" is the one that
// crosses the identity boundary; everything else is "Allow this".
func isOperatorCard(name string, args map[string]any) bool {
	if strings.Contains(name, "operator") {
		return true
	}
	rule, _ := args["rule"].(string)
	return strings.Contains(rule, "operator")
}
