package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"harness/internal/events"
)

// Item 2iz (a): the arguments. Strict on purpose — `agentb --jsom "task"`
// quietly running without JSON is worse than refusing, because a script would
// never find out.
func TestParseArgs2iz(t *testing.T) {
	options, err := ParseArgs([]string{"--profile", "acmeholding", "--json", "add a test", "and run it"})
	if err != nil {
		t.Fatal(err)
	}
	if options.Profile != "acmeholding" || !options.JSON {
		t.Fatalf("options=%+v", options)
	}
	// Loose words join into one task, so the shell's quoting is forgiving.
	if options.Task != "add a test and run it" {
		t.Fatalf("task=%q", options.Task)
	}

	if _, err := ParseArgs([]string{"--jsom", "task"}); err == nil {
		t.Error("an unknown flag was accepted; a typo would silently change behaviour")
	}
	if _, err := ParseArgs([]string{"--profile"}); err == nil {
		t.Error("--profile with no label was accepted")
	}
	if _, err := ParseArgs([]string{}); err == nil {
		t.Error("no task was accepted")
	}
	// --help and --version answer without a task, because needing a task to ask
	// for help is a small cruelty.
	for _, argument := range []string{"--help", "-h", "--version"} {
		if _, err := ParseArgs([]string{argument}); err != nil {
			t.Errorf("%s needed a task: %v", argument, err)
		}
	}
	if options, err := ParseArgs([]string{"--profile=local", "task"}); err != nil || options.Profile != "local" {
		t.Errorf("--profile=value form: %+v %v", options, err)
	}
}

// Item 2iz (a): zero for done, non-zero per reason — so a script branches on WHY
// a run stopped, not merely on whether.
func TestExitCodesDistinguishReasons2iz(t *testing.T) {
	if got := ExitCode("done"); got != 0 {
		t.Errorf("done exited %d", got)
	}
	distinct := map[int]string{}
	for _, reason := range []string{"done", "user_stop", "wall_clock", "tool_errors", "model_error", "connection_not_runnable"} {
		code := ExitCode(reason)
		if previous, clash := distinct[code]; clash {
			t.Errorf("%s and %s both exit %d, so a script cannot tell them apart", reason, previous, code)
		}
		distinct[code] = reason
	}
	// An unknown reason is a harness defect, not an outcome, and says so.
	if got := ExitCode("something_new"); got != 2 {
		t.Errorf("an unknown reason exited %d, want 2", got)
	}
}

// One line per tool call: name, target, ok or error, ms. The class comes from
// item 2jg (e), which put it on the event.
func TestToolLine2iz(t *testing.T) {
	ok := ToolLine(map[string]any{
		"name": "read_file", "ok": true, "ms": float64(3),
		"args": map[string]any{"path": "internal/agent/run.go"},
	})
	if ok != "  read_file internal/agent/run.go → ok 3 ms" {
		t.Errorf("ok line: %q", ok)
	}
	failed := ToolLine(map[string]any{
		"name": "shell", "ok": false, "ms": float64(1500), "class": "permission",
		"args": map[string]any{"command": "sqlcmd -?"},
	})
	if failed != "  shell sqlcmd -? → error:permission 1.5 s" {
		t.Errorf("error line: %q", failed)
	}
}

func TestStopLine2iz(t *testing.T) {
	if got := StopLine("done", "", 2*time.Second); got != "done in 2.0 s" {
		t.Errorf("done: %q", got)
	}
	if got := StopLine("wall_clock", "", time.Second); got != "stopped: wall clock" {
		t.Errorf("limit: %q", got)
	}
	if got := StopLine("tool_errors", "three in a row", time.Second); !strings.Contains(got, "three in a row") {
		t.Errorf("detail dropped: %q", got)
	}
}

// Item 2iz (b): --json emits the event stream as JSONL, the SAME events the
// journal holds. That is what makes the output replayable in the app, so the
// test's real assertion is that nothing is reshaped on the way out.
func TestJSONIsTheEventStreamUnchanged2iz(t *testing.T) {
	out := &bytes.Buffer{}
	renderer := NewRenderer(out, &bytes.Buffer{}, Options{JSON: true})
	stream := make(chan events.Event, 3)
	stream <- events.New(events.ToolResult, "s", "r1", map[string]any{"name": "read_file", "ok": true, "ms": 3})
	stream <- events.New(events.RunStopped, "s", "r1", map[string]any{"reason": "done"})
	close(stream)
	reason, _ := renderer.Follow(stream, "r1")
	if reason != "done" {
		t.Fatalf("reason=%q", reason)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two JSONL lines, got %d:\n%s", len(lines), out.String())
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, `{"`) || !strings.Contains(line, `"type"`) {
			t.Errorf("not a journal-shaped event: %s", line)
		}
	}
	// The human renderer's furniture must not have leaked into the machine one.
	if strings.Contains(out.String(), "→") {
		t.Error("the JSON stream carries the terminal's formatting")
	}
}

// The human stream: the reply is printed once at the end rather than in
// fragments, and reasoning is a token count unless asked for in full.
func TestTheTerminalStreamReadsAsOneReply2iz(t *testing.T) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	renderer := NewRenderer(out, errOut, Options{})
	stream := make(chan events.Event, 4)
	stream <- events.New(events.ToolResult, "s", "r1", map[string]any{"name": "list_dir", "ok": true, "ms": 1, "args": map[string]any{"path": "."}})
	stream <- events.New(events.ModelResponse, "s", "r1", map[string]any{"content": "Here is ", "reasoning": "long private thinking", "reasoning_tokens": float64(74)})
	stream <- events.New(events.ModelResponse, "s", "r1", map[string]any{"content": "the answer."})
	stream <- events.New(events.RunStopped, "s", "r1", map[string]any{"reason": "done"})
	close(stream)
	renderer.Follow(stream, "r1")

	if !strings.Contains(out.String(), "Here is the answer.") {
		t.Errorf("the reply did not arrive as one line:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "thought 74 tokens") {
		t.Errorf("reasoning was not summarised:\n%s", errOut.String())
	}
	if strings.Contains(out.String(), "long private thinking") || strings.Contains(errOut.String(), "long private thinking") {
		t.Error("reasoning was printed in full without --verbose")
	}
	// Tool lines go to stdout; the running commentary goes to stderr, so
	// `agentb "task" > answer.txt` captures the answer and not the noise.
	if !strings.Contains(out.String(), "list_dir") {
		t.Error("the tool line did not reach stdout")
	}
}

func TestVerbosePrintsReasoning2iz(t *testing.T) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	renderer := NewRenderer(out, errOut, Options{Verbose: true})
	stream := make(chan events.Event, 2)
	stream <- events.New(events.ModelResponse, "s", "r1", map[string]any{"content": "x", "reasoning": "long private thinking", "reasoning_tokens": float64(74)})
	stream <- events.New(events.RunStopped, "s", "r1", map[string]any{"reason": "done"})
	close(stream)
	renderer.Follow(stream, "r1")
	if !strings.Contains(errOut.String(), "long private thinking") {
		t.Errorf("--verbose did not print reasoning:\n%s", errOut.String())
	}
}

// Item 2iz (c): the two cards, and only two, with the app's scopes.
func TestApprovalPromptsUseTheTwoCards2iz(t *testing.T) {
	operator := Card(map[string]any{"name": "shell.operator_command", "args": map[string]any{
		"command": "git diff", "identity": "Agent_b operator (not Administrator)", "rule": "operator_command",
	}})
	if !strings.Contains(operator, "RUN AS YOU") || !strings.Contains(operator, "git diff") {
		t.Errorf("operator card:\n%s", operator)
	}
	ordinary := Card(map[string]any{"name": "write_file", "args": map[string]any{"path": "NOTES.md"}})
	if !strings.Contains(ordinary, "ALLOW THIS") || strings.Contains(ordinary, "RUN AS YOU") {
		t.Errorf("ordinary card:\n%s", ordinary)
	}
}

func TestApproverAnswersWithTheAppsScopes2iz(t *testing.T) {
	for _, probe := range []struct{ typed, want string }{
		{"o\n", DecideOnce}, {"once\n", DecideOnce}, {"y\n", DecideOnce},
		{"r\n", DecideRun}, {"s\n", DecideSession}, {"n\n", DecideDeny},
	} {
		got := ""
		approver := NewApprover(strings.NewReader(probe.typed), &bytes.Buffer{}, func(_, _, decision string) error {
			got = decision
			return nil
		})
		handled := approver.Handle(events.New(events.ApprovalRequired, "s", "r1", map[string]any{"call_id": "c1", "name": "shell"}))
		if !handled || got != probe.want {
			t.Errorf("%q gave %q, want %q (handled=%v)", probe.typed, got, probe.want, handled)
		}
	}
}

// A closed stdin is not consent. Anything that cannot be answered is refused,
// the same way the unattended rule falls.
func TestAnUnanswerablePromptIsARefusal2iz(t *testing.T) {
	got := ""
	approver := NewApprover(strings.NewReader(""), &bytes.Buffer{}, func(_, _, decision string) error {
		got = decision
		return nil
	})
	approver.Handle(events.New(events.ApprovalRequired, "s", "r1", map[string]any{"call_id": "c1", "name": "shell"}))
	if got != DecideDeny {
		t.Fatalf("a closed stdin answered %q; silence is not approval", got)
	}
}

func TestApproverIgnoresEverythingElse2iz(t *testing.T) {
	approver := NewApprover(strings.NewReader("o\n"), &bytes.Buffer{}, func(string, string, string) error {
		t.Fatal("a non-approval event reached the decider")
		return nil
	})
	if approver.Handle(events.New(events.ToolResult, "s", "r1", map[string]any{"name": "read_file"})) {
		t.Fatal("a tool result was treated as an approval")
	}
}
