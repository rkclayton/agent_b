package tools

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"harness/internal/session"
)

const processOutputLimit = 1 << 20

type managedProcess struct {
	id, sessionID, runID, command string
	process                       runningShellProcess
	output                        *lockedBuffer
	done                          chan struct{}
	mu                            sync.Mutex
	exit                          int
	err                           error
}

type Process struct {
	shell *Shell
	mu    sync.Mutex
	next  uint64
	items map[string]*managedProcess
}

func NewProcess(shell *Shell) *Process {
	return &Process{shell: shell, items: map[string]*managedProcess{}}
}
func (*Process) Name() string { return "process" }
func (*Process) Description() string {
	return "Start and manage a long-running command. Processes end with the current run."
}
func (*Process) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"action":  map[string]any{"type": "string", "enum": []string{"start", "list", "read", "write", "wait", "kill"}},
		"command": map[string]any{"type": "string"}, "id": map[string]any{"type": "string"},
		"offset": map[string]any{"type": "integer", "minimum": 0}, "input": map[string]any{"type": "string"},
		"timeout_s": map[string]any{"type": "integer", "minimum": 0, "maximum": 600},
	}, "required": []string{"action"}}
}
func (p *Process) Call(ctx context.Context, s *session.Session, args map[string]any) (string, error) {
	d := p.CallDetailed(ctx, s, args)
	return d.Content, d.Err
}
func (p *Process) CallDetailed(ctx context.Context, s *session.Session, args map[string]any) CallDetail {
	return p.call(ctx, s, args, false)
}
func (p *Process) CallAsOperator(ctx context.Context, s *session.Session, args map[string]any) (string, error) {
	d := p.call(ctx, s, args, true)
	return d.Content, d.Err
}
func (p *Process) OperatorCommandWith(args map[string]any, additional []string) (OperatorCommand, bool) {
	if args["action"] != "start" {
		return OperatorCommand{}, false
	}
	return p.shell.OperatorCommandWith(args, additional)
}

func (p *Process) call(ctx context.Context, s *session.Session, args map[string]any, operator bool) CallDetail {
	action, _ := args["action"].(string)
	if action != "start" {
		content, err := p.control(s, args, action)
		return CallDetail{Content: content, Err: err}
	}
	command, _ := args["command"].(string)
	if strings.TrimSpace(command) == "" {
		return CallDetail{Err: errors.New("command is required")}
	}
	cfg := p.shell.config()
	sandboxID, sandboxStatus, sandboxed, notice, disableErr := p.shell.sandboxExecution(s)
	if disableErr != nil {
		return CallDetail{Err: disableErr}
	}
	if sandboxed && !operator && !cfg.OperatorContext {
		reason := "sandbox execution uses the user's Docker session, outside the agentb-svc identity and firewall boundary"
		return CallDetail{Content: reason, OperatorOverrideReason: reason, Metadata: map[string]any{"target": "sandbox " + sandboxID}}
	}
	if denied := deniedCommand(command, cfg.Deny); denied != "" {
		return CallDetail{Err: fmt.Errorf("command blocked by deny list entry %q", denied)}
	}
	outside := outsideCommandDecision(command, s, cfg.TrustedFolders)
	if !operator && outside.trusted && outside.card == "" && outside.missing == "" && cfg.ServiceAccount.Enabled && !cfg.OperatorContext {
		operator = true
	}
	if !operator && !cfg.ServiceAccount.Enabled && !cfg.OperatorContext && outside.card != "" {
		return CallDetail{Content: "command was not started: " + outside.card, OperatorOverrideReason: outside.card, Metadata: outsideMetadata(outside.folders, true)}
	}
	if !operator && cfg.ServiceAccount.Enabled && !cfg.OperatorContext {
		if name := operatorOnlyInterpreter(command, exec.LookPath, operatorHome()); name != "" {
			reason := name + " runs only as you; needs Run as you"
			return CallDetail{Content: reason, OperatorOverrideReason: reason}
		}
	}
	p.mu.Lock()
	runID := s.Snapshot().Run.RunID
	running := 0
	for _, item := range p.items {
		if item.sessionID == s.ID {
			select {
			case <-item.done:
			default:
				running++
			}
		}
	}
	if running >= 4 {
		p.mu.Unlock()
		return CallDetail{Err: errors.New("at most 4 processes may run in one chat")}
	}
	p.next++
	id := fmt.Sprintf("p%d", p.next)
	p.mu.Unlock()
	output := newProcessWindow(processOutputLimit)
	var proc runningShellProcess
	var err error
	if sandboxed {
		proc, err = startHarnessBackgroundProcess(sandboxStatus.Executable, []string{sandboxID, "bash", "-lc", command}, s.Workspace, output)
	} else {
		host := shellHostFor(cfg)
		executed := shellCommandForHost(host, command)
		argv := append(append([]string(nil), cfg.Command[1:]...), executed)
		proc, _, err = p.shell.startBackground(cfg, s.Workspace, argv, output, operator)
	}
	if err != nil {
		var required *operatorOverrideRequired
		if errors.As(err, &required) {
			return CallDetail{Content: "service-account process was not started: " + required.reason, OperatorOverrideReason: required.reason}
		}
		return CallDetail{Err: err}
	}
	item := &managedProcess{id: id, sessionID: s.ID, runID: runID, command: command, process: proc, output: output, done: make(chan struct{})}
	p.mu.Lock()
	p.items[id] = item
	p.mu.Unlock()
	go func() { item.exit, item.err = proc.Wait(); close(item.done) }()
	content := "started " + id
	if notice != "" {
		content = notice + "\n" + content
	}
	return CallDetail{Content: content, OperatorContext: operator || cfg.OperatorContext}
}

func (p *Process) control(s *session.Session, args map[string]any, action string) (string, error) {
	runID := s.Snapshot().Run.RunID
	if action == "list" {
		p.mu.Lock()
		lines := []string{}
		for _, v := range p.items {
			if v.sessionID == s.ID && v.runID == runID {
				state := "running"
				select {
				case <-v.done:
					state = fmt.Sprintf("exit=%d", v.exit)
				default:
				}
				lines = append(lines, v.id+" "+state+" "+v.command)
			}
		}
		p.mu.Unlock()
		sort.Strings(lines)
		if len(lines) == 0 {
			return "no processes", nil
		}
		return strings.Join(lines, "\n"), nil
	}
	id, _ := args["id"].(string)
	p.mu.Lock()
	item := p.items[id]
	p.mu.Unlock()
	if item == nil || item.sessionID != s.ID || item.runID != runID {
		return "", errors.New("process not found")
	}
	switch action {
	case "read":
		offset := int64(number(args["offset"], 0))
		value, base, next := item.output.Read(offset)
		return fmt.Sprintf("offset=%d next=%d\n%s", base, next, cleanConsoleOutput(value)), nil
	case "write":
		input, _ := args["input"].(string)
		interactive, ok := item.process.(interactiveShellProcess)
		if !ok {
			return "", errors.New("process input is closed")
		}
		if err := interactive.WriteInput([]byte(input)); err != nil {
			return "", err
		}
		return "input written", nil
	case "kill":
		item.process.KillTree()
		select {
		case <-item.done:
			return "killed " + id, nil
		case <-time.After(5 * time.Second):
			return "", errors.New("process tree did not stop within 5 seconds")
		}
	case "wait":
		timeout := time.Duration(number(args["timeout_s"], 30)) * time.Second
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-item.done:
			if item.err != nil {
				return "", item.err
			}
			return fmt.Sprintf("exit=%d", item.exit), nil
		case <-timer.C:
			return "still running", nil
		}
	default:
		return "", errors.New("action must be start, list, read, write, wait, or kill")
	}
}

func (p *Process) KillRun(sessionID, runID string) {
	p.mu.Lock()
	list := []*managedProcess{}
	for _, v := range p.items {
		if v.sessionID == sessionID && v.runID == runID {
			list = append(list, v)
			delete(p.items, v.id)
		}
	}
	p.mu.Unlock()
	for _, v := range list {
		select {
		case <-v.done:
		default:
			v.process.KillTree()
		}
	}
	deadline := time.After(5 * time.Second)
	for _, v := range list {
		select {
		case <-v.done:
		case <-deadline:
			return
		}
	}
}
func (p *Process) KillAll() {
	p.mu.Lock()
	list := []*managedProcess{}
	for _, v := range p.items {
		list = append(list, v)
	}
	p.items = map[string]*managedProcess{}
	p.mu.Unlock()
	for _, v := range list {
		v.process.KillTree()
	}
	deadline := time.After(5 * time.Second)
	for _, v := range list {
		select {
		case <-v.done:
		case <-deadline:
			return
		}
	}
}
