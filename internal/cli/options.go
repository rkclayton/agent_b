// Package cli is the terminal front end for the engine: argument parsing, the
// stream renderer, and approvals answered at a prompt.
//
// Item 2iz. It links the ENGINE only. No web server, no window, no reflection,
// no updater, no signing, no notifications. That is not a convention here — the
// linkage is asserted by a test over `go list -deps`, so the independence is
// enforced rather than hoped.
package cli

import (
	"fmt"
	"strings"
)

// Options is one invocation of `agentb`.
type Options struct {
	Task string
	// Profile names the connection the run uses. Empty means the configured b
	// role, which is what the app would have used.
	Profile string
	// JSON emits the event stream as JSONL — the same events the journal holds —
	// so a script reads what the app reads.
	JSON bool
	// Verbose prints reasoning in full instead of as a token count.
	Verbose bool
	// Continue reuses the last run's session in this folder, for a follow-up.
	Continue bool
	// Unattended applies item 5f's rule: nothing asks, and a boundary hit fails
	// the run.
	Unattended bool
	// Memory allows the memory layers to be written. Without it they are read
	// only, because a one-shot task in somebody's repository should not silently
	// change what the agent remembers.
	Memory bool
	// Replay prints a previous run from this folder's journal and exits.
	Replay bool
	// Help and Version short-circuit everything.
	Help    bool
	Version bool
}

// Usage is the whole of it. A CLI that needs a manual to run one task has
// already failed.
const Usage = `agentb — run one task in this folder

  agentb "<task>"              run the task here, streaming to the terminal
  agentb --replay              print this folder's last run
  agentb --version             print the build identity

  --profile <label>   the connection to use (default: the configured b role)
  --json              emit the event stream as JSONL for scripts
  --verbose           print reasoning in full rather than as a token count
  --continue          follow up in this folder's last session
  --unattended        nothing asks; a boundary hit fails the run
  --memory            allow the run to write the memory layers
  -h, --help          this

The workspace is the current directory, its subfolders, and any registered
plans. Anything else needs an approval, exactly as in the app.`

// ParseArgs reads one invocation. It is deliberately strict: an unknown flag is
// an error rather than a silently ignored word, because "agentb --jsom task"
// quietly running without JSON is worse than refusing.
func ParseArgs(argv []string) (Options, error) {
	options := Options{}
	tasks := []string{}
	for index := 0; index < len(argv); index++ {
		argument := argv[index]
		switch argument {
		case "-h", "--help":
			options.Help = true
		case "--version":
			options.Version = true
		case "--json":
			options.JSON = true
		case "--verbose":
			options.Verbose = true
		case "--continue":
			options.Continue = true
		case "--unattended":
			options.Unattended = true
		case "--memory":
			options.Memory = true
		case "--replay":
			options.Replay = true
		case "--profile":
			if index+1 >= len(argv) {
				return options, fmt.Errorf("--profile needs a label")
			}
			index++
			options.Profile = argv[index]
		default:
			if strings.HasPrefix(argument, "--profile=") {
				options.Profile = strings.TrimPrefix(argument, "--profile=")
				continue
			}
			if strings.HasPrefix(argument, "-") {
				return options, fmt.Errorf("unknown option %q; agentb --help", argument)
			}
			tasks = append(tasks, argument)
		}
	}
	options.Task = strings.TrimSpace(strings.Join(tasks, " "))
	if !options.Help && !options.Version && !options.Replay && options.Task == "" {
		return options, fmt.Errorf("agentb needs a task; agentb --help")
	}
	// Item 5f: unattended means nothing asks. A terminal prompt is asking, so the
	// two cannot both be true and saying so beats discovering it at the boundary.
	if options.Unattended && options.Continue && options.Task == "" {
		return options, fmt.Errorf("--continue without a task has nothing to run")
	}
	return options, nil
}

// ExitCode maps a terminal stop reason to a process exit code. Item 2iz (a):
// zero for done, non-zero per reason, so a script can branch on WHY rather than
// only on whether.
func ExitCode(reason string) int {
	switch reason {
	case "done":
		return 0
	case "user_stop", "aborted_mid_run", "aborted_mid_tool", "aborted_mid_model", "cancellation_requested":
		return 130 // the shell's convention for an interrupted job
	case "wall_clock", "turn_ceiling", "tool_budget", "context_ceiling", "context_exhausted":
		return 3 // a limit was reached; the work may be resumable
	case "tool_errors":
		return 4
	case "model_error", "model_unreachable", "length":
		return 5
	case "connection_not_runnable":
		return 6
	case "":
		return 1
	default:
		return 2 // a harness reason, which is a defect rather than an outcome
	}
}
