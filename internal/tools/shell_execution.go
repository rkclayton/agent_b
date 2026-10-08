package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"harness/internal/config"
	"harness/internal/session"
)

type outputObserverKey struct{}

// WithOutputObserver adds a live-only shell output observer. The runner uses it
// for the status strip; tool results and their durable journal shape are unchanged.
func WithOutputObserver(ctx context.Context, observer func(string)) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, outputObserverKey{}, observer)
}

func outputObserver(ctx context.Context) func(string) {
	observer, _ := ctx.Value(outputObserverKey{}).(func(string))
	return observer
}

// Shell executes commands without a workspace jail or OS sandbox. The workspace is
// only its initial working directory; identity, approval, timeout, and deny-list
// controls do not make it workspace-confined.
type Shell struct {
	mu                sync.RWMutex
	cfg               config.Shell
	operatorCommands  []string
	workspace         string
	sandbox           config.Sandbox
	sandboxStatus     SandboxStatus
	sandboxCreateMu   sync.Mutex
	fileCoordinator   *FileCoordinator
	credential        shellCredentialReader
	startService      serviceProcessStarter
	startServiceInput serviceProcessInputStarter
	identityMu        sync.RWMutex
	identity          ShellIdentityStatus
	identityReporter  func(ShellIdentityStatus)
	// alarmedCondition is the last service-account condition this launch alarmed
	// on, so the same condition is reported once rather than once per trigger
	// (item 2l2 (c)).
	alarmedCondition atomic.Value
}

func NewShell(cfg config.Shell) *Shell {
	return &Shell{cfg: cfg, startService: startServiceAccountProcess, startServiceInput: startServiceAccountProcessWithInput}
}
func (*Shell) Name() string { return "shell" }
func (s *Shell) Description() string {
	cfg, operatorCommands := s.configWithOperatorCommands()
	// Item 2gb: the description names the dialect the command actually runs in.
	syntax := shellHostFor(cfg).Dialect()
	description := "Run an unconfined inline command from the folder root. "
	if cfg.ServiceAccount.Enabled {
		description += "Shell has no public network (enforced outside the tool layer); use web_search or fetch_url for public network reads. "
	}
	description += "Agent-written Windows host scripts cannot be executed; use run_script for multi-line source. This is PowerShell, not cmd.exe: use Set-Location, not `cd /d`. " + syntax
	if cfg.ServiceAccount.Enabled && len(operatorCommands) > 0 {
		description += " Git and other configured commands need one decision per run; expect one prompt, not one per call."
	}
	return description
}
func (s *Shell) Schema() map[string]any {
	cfg := s.config()
	return map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}, "timeout_s": map[string]any{"type": "integer", "default": cfg.TimeoutS, "maximum": cfg.MaxTimeoutS}}, "required": []string{"command"}}
}
func (s *Shell) Call(ctx context.Context, item *session.Session, args map[string]any) (string, error) {
	detail := s.CallDetailed(ctx, item, args)
	return detail.Content, detail.Err
}

func (s *Shell) CallDetailed(ctx context.Context, item *session.Session, args map[string]any) CallDetail {
	return s.call(ctx, item, args, false)
}

// CallAsOperator is intentionally absent from the Tool interface and schema.
// Only the dispatcher invokes it after a separate, unconditional approval.
func (s *Shell) CallAsOperator(ctx context.Context, item *session.Session, args map[string]any) (string, error) {
	detail := s.call(ctx, item, args, true)
	return detail.Content, detail.Err
}

func (s *Shell) call(ctx context.Context, item *session.Session, args map[string]any, forceOperator bool) (detail CallDetail) {
	cfg := s.config()
	defer func() { detail.OperatorContext = cfg.OperatorContext && !forceOperator }()
	command, ok := args["command"].(string)
	if !ok || strings.TrimSpace(command) == "" {
		return CallDetail{Err: fmt.Errorf("command is required")}
	}
	if reason := forbiddenShellCommand(command, item, s.fileCoordinatorSnapshot()); reason != "" {
		return CallDetail{Err: fmt.Errorf("command blocked: %s", reason)}
	}
	sandboxID, sandboxStatus, sandboxed := s.sandboxExecution(item)
	if sandboxed && !sandboxStatus.Available {
		sandboxed = false
	}
	if sandboxed && !forceOperator && !cfg.OperatorContext {
		reason := "sandbox execution uses the user's Docker session, outside the agentb-svc identity and firewall boundary"
		return CallDetail{Content: reason, OperatorOverrideReason: reason, Metadata: map[string]any{"target": "sandbox " + sandboxID}}
	}
	if sandboxed {
		if denied := deniedCommand(command, cfg.Deny); denied != "" {
			return CallDetail{Err: fmt.Errorf("command blocked by deny list entry %q", denied), Metadata: map[string]any{"target": "sandbox " + sandboxID}}
		}
		timeout := number(args["timeout_s"], cfg.TimeoutS)
		if timeout <= 0 {
			timeout = cfg.TimeoutS
		}
		if timeout > cfg.MaxTimeoutS {
			timeout = cfg.MaxTimeoutS
		}
		return s.callSandbox(ctx, item, sandboxStatus.Executable, sandboxID, []string{sandboxID, "bash", "-lc", command}, nil, timeout, cfg)
	}
	if cfg.FileRoutingGuardEnabled() {
		refusal, ambiguous := inspectShellFileRouting(command)
		if refusal != nil {
			if cfg.ServiceAccount.Enabled && refusal.Replacement.Tool == "search" && routingReplacementOutsideWorkspace(item.Workspace, refusal) {
				refusal.Reason = "direct file discovery path is outside the folder while the service-account split is enabled"
				refusal.Replacement = nil
				refusal.Guidance = "paths outside the folder require a user decision; state the need once and stop rather than retrying paths"
			}
			result, _ := json.Marshal(refusal)
			replacement := "none"
			if refusal.Replacement != nil {
				replacement = refusal.Replacement.Tool
			}
			log.Printf("shell file-routing refusal: session=%s tool=%s command=%q", item.ID, replacement, command)
			return CallDetail{Err: fmt.Errorf("note: command was not executed; %s", result)}
		}
		if ambiguous {
			log.Printf("debug: shell file-routing guard allowed ambiguous compound command: %q", command)
		}
	}
	if denied := deniedCommand(command, cfg.Deny); denied != "" {
		return CallDetail{Err: fmt.Errorf("command blocked by deny list entry %q", denied)}
	}
	timeout := number(args["timeout_s"], cfg.TimeoutS)
	if timeout <= 0 {
		timeout = cfg.TimeoutS
	}
	if timeout > cfg.MaxTimeoutS {
		timeout = cfg.MaxTimeoutS
	}
	// Item 2gb: PowerShell 7 when the host has it, else 5.1 with the model's
	// top-level chain operators rewritten, so `a && b` works either way.
	host := shellHostFor(cfg)
	executed := shellCommandForHost(host, command)
	if executed != command {
		// The user's log shows what the interpreter was actually given,
		// not only what the model wrote (v1.0.1/W4 cold review).
		log.Printf("shell rewrote the model's chain operators for PowerShell 5.1: as run: %q", executed)
	}
	argv := append(append([]string(nil), cfg.Command[1:]...), executed)
	output := newLockedBuffer(ctx)
	var process runningShellProcess
	var usedService bool
	var err error
	outside := outsideCommandDecision(command, item, cfg.TrustedFolders)
	if !forceOperator && outside.trusted && outside.card == "" && outside.missing == "" && cfg.ServiceAccount.Enabled && !cfg.OperatorContext {
		forceOperator = true
	}
	// Item 2fi: with no service identity a command naming a path outside the
	// folder raises the same operator decision as read_file, so a file cannot be
	// read around the card.
	if !forceOperator && !cfg.ServiceAccount.Enabled && !cfg.OperatorContext {
		// Item 2fz: directory changes and listings run; an existing outside
		// read raises the card; a missing one is a plain error.
		decision := outside
		if decision.card != "" {
			return CallDetail{Content: "command was not started: " + decision.card, OperatorOverrideReason: decision.card, Metadata: outsideMetadata(decision.folders, true)}
		}
		if decision.missing != "" {
			return CallDetail{Err: fmt.Errorf("command was not started: %s; there is nothing for the user to allow — check the path, or use a file inside your folder", decision.missing)}
		}
	}
	if !forceOperator && cfg.ServiceAccount.Enabled && !cfg.OperatorContext {
		if name := operatorOnlyInterpreter(command, exec.LookPath, operatorHome()); name != "" {
			reason := name + " runs only as you; needs Run as you"
			return CallDetail{Content: reason, OperatorOverrideReason: reason}
		}
	}
	if forceOperator {
		process, err = startHarnessProcess(host.Executable, argv, item.Workspace, output)
	} else {
		process, usedService, err = s.start(cfg, item.Workspace, argv, output)
	}
	if err != nil {
		var required *operatorOverrideRequired
		if errors.As(err, &required) {
			return CallDetail{
				Content:                "service-account shell was not started: " + required.reason,
				OperatorOverrideReason: required.reason,
				Metadata:               outsideMetadata(outside.folders, false),
			}
		}
		return CallDetail{Err: err}
	}
	return waitShellProcess(ctx, process, usedService, timeout, cfg, output, command)
}

// deniedCommand matches entries only at shell command positions. Separators in
// quoted arguments are data, and words in options never become commands.
func deniedCommand(source string, entries []string) string {
	for _, segment := range commandSegments(source) {
		words := strings.Fields(segment)
		if len(words) == 0 {
			continue
		}
		command := strings.Trim(words[0], "\"'")
		command = strings.TrimSuffix(strings.ToLower(command), ".exe")
		for _, entry := range entries {
			want := strings.Fields(strings.TrimSpace(entry))
			if len(want) == 0 || command != strings.TrimSuffix(strings.ToLower(strings.Trim(want[0], "\"'")), ".exe") || len(words) < len(want) {
				continue
			}
			matched := true
			for index := 1; index < len(want); index++ {
				if !strings.EqualFold(strings.Trim(words[index], "\"'"), strings.Trim(want[index], "\"'")) {
					matched = false
					break
				}
			}
			if matched {
				return entry
			}
		}
	}
	return ""
}

func commandSegments(source string) []string {
	segments, start := []string{}, 0
	var quote rune
	for index, char := range source {
		if quote != 0 {
			if char == quote {
				quote = 0
			}
			continue
		}
		if char == '\'' || char == '"' || char == '`' {
			quote = char
			continue
		}
		if char == ';' || char == '|' || char == '&' || char == '\n' || char == '\r' {
			segments = append(segments, strings.TrimSpace(source[start:index]))
			start = index + 1
		}
	}
	segments = append(segments, strings.TrimSpace(source[start:]))
	return segments
}

func outsideMetadata(folders []string, boundary bool) map[string]any {
	if len(folders) == 0 {
		return nil
	}
	return map[string]any{"outside_folders": folders, "outside_folder_card": boundary}
}

func waitShellProcess(ctx context.Context, process runningShellProcess, usedService bool, timeout int, cfg config.Shell, output *lockedBuffer, operatorCommand string) CallDetail {
	type waitResult struct {
		code int
		err  error
	}
	done := make(chan waitResult, 1)
	go func() {
		code, err := process.Wait()
		done <- waitResult{code: code, err: err}
	}()
	timer := time.NewTimer(time.Duration(timeout) * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		process.KillTree()
		<-done
		return CallDetail{Err: ctx.Err()}
	case <-timer.C:
		process.KillTree()
		<-done
		partial := cutOutput(cleanConsoleOutput(output.String()), cfg.MaxOutputLinesHead, cfg.MaxOutputLinesTail)
		if partial != "" {
			return CallDetail{Err: fmt.Errorf("timed out after %ds; partial output:\n%s", timeout, partial)}
		}
		return CallDetail{Err: fmt.Errorf("timed out after %ds; partial output:", timeout)}
	case result := <-done:
		if result.err != nil {
			return CallDetail{Err: result.err}
		}
		raw := output.String()
		body := cutOutput(cleanConsoleOutput(raw), cfg.MaxOutputLinesHead, cfg.MaxOutputLinesTail)
		metadata := map[string]any{"raw_output": cutOutput(raw, cfg.MaxOutputLinesHead, cfg.MaxOutputLinesTail)}
		if result.code != 0 {
			content := fmt.Sprintf("exit=%d", result.code)
			if body != "" {
				content += "\n" + body
			}
			if usedService {
				if reason := serviceBoundaryReason(operatorCommand, body); reason != "" {
					return CallDetail{Content: content, OperatorOverrideReason: reason, Metadata: metadata}
				}
			}
			return CallDetail{Err: fmt.Errorf("command failed\n%s", content), Metadata: metadata}
		}
		if body == "" {
			return CallDetail{Content: "exit=0, no output", Metadata: metadata}
		}
		return CallDetail{Content: "exit=0\n" + body, Metadata: metadata}
	}
}

// cleanConsoleOutput removes terminal control sequences and repairs invalid
// encoding before output enters model context. The unmodified stream is kept
// separately in tool-result metadata for the durable journal.
func cleanConsoleOutput(value string) string {
	value = strings.ToValidUTF8(value, "�")
	var out strings.Builder
	for i := 0; i < len(value); {
		if value[i] != 0x1b {
			out.WriteByte(value[i])
			i++
			continue
		}
		i++
		if i >= len(value) {
			break
		}
		switch value[i] {
		case '[': // CSI: parameters/intermediates followed by a final byte.
			i++
			for i < len(value) {
				b := value[i]
				i++
				if b >= 0x40 && b <= 0x7e {
					break
				}
			}
		case ']': // OSC: terminated by BEL or ST.
			i++
			for i < len(value) {
				if value[i] == 0x07 {
					i++
					break
				}
				if value[i] == 0x1b && i+1 < len(value) && value[i+1] == '\\' {
					i += 2
					break
				}
				i++
			}
		default:
			i++
		}
	}
	return out.String()
}

type lockedBuffer struct {
	mu       sync.Mutex
	b        bytes.Buffer
	partial  string
	observer func(string)
}

func newLockedBuffer(ctx context.Context) *lockedBuffer {
	return &lockedBuffer{observer: outputObserver(ctx)}
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	n, err := b.b.Write(p)
	observer := b.observer
	line := ""
	if observer != nil {
		chunk := strings.ReplaceAll(strings.ToValidUTF8(string(p), "�"), "\r", "")
		parts := strings.Split(b.partial+chunk, "\n")
		b.partial = parts[len(parts)-1]
		if len(b.partial) > 512 {
			b.partial = b.partial[len(b.partial)-512:]
		}
		for index := len(parts) - 1; index >= 0; index-- {
			if candidate := strings.TrimSpace(parts[index]); candidate != "" {
				line = candidate
				break
			}
		}
		if len(line) > 240 {
			line = line[len(line)-240:]
		}
	}
	b.mu.Unlock()
	if line != "" {
		observer(line)
	}
	return n, err
}
func (b *lockedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }
func cutOutput(value string, head, tail int) string {
	value = strings.TrimRight(normalizeLF(value), "\n")
	if value == "" {
		return ""
	}
	lines := strings.Split(value, "\n")
	if len(lines) <= head+tail {
		return value
	}
	omitted := len(lines) - head - tail
	kept := append([]string(nil), lines[:head]...)
	kept = append(kept, fmt.Sprintf("[… %d lines omitted …]", omitted))
	kept = append(kept, lines[len(lines)-tail:]...)
	return strings.Join(kept, "\n")
}
