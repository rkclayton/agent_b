package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"harness/internal/session"
)

type RunScript struct{ shell *Shell }

func NewRunScript(shell *Shell) *RunScript { return &RunScript{shell: shell} }
func (*RunScript) Name() string            { return "run_script" }
func (t *RunScript) Description() string {
	// Item 2gb: powershell source runs in the same interpreter the shell tool
	// resolves, and the description says which. Source is not rewritten: only
	// the shell tool's one-line command is.
	dialect := "PowerShell"
	if t.shell != nil {
		if host := shellHostFor(t.shell.config()); host.Version != "" {
			dialect = "PowerShell " + host.Version
		}
	}
	return "Run source from standard input without creating a script file. Use powershell for multi-line " + dialect + " (chain operators are not rewritten in script source; use `if`), python/node for host interpreter source, or bash when the global Docker Sandbox setting is enabled; script files are not created or executed."
}
func (*RunScript) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"language":  map[string]any{"type": "string", "enum": []string{"powershell", "python", "node", "bash"}},
			"source":    map[string]any{"type": "string"},
			"timeout_s": map[string]any{"type": "integer"},
		},
		"required": []string{"language", "source"},
	}
}
func (t *RunScript) Call(ctx context.Context, item *session.Session, args map[string]any) (string, error) {
	detail := t.CallDetailed(ctx, item, args)
	return detail.Content, detail.Err
}
func (t *RunScript) CallDetailed(ctx context.Context, item *session.Session, args map[string]any) CallDetail {
	return t.call(ctx, item, args, false)
}
func (t *RunScript) CallAsOperator(ctx context.Context, item *session.Session, args map[string]any) (string, error) {
	detail := t.call(ctx, item, args, true)
	return detail.Content, detail.Err
}
func (t *RunScript) call(ctx context.Context, item *session.Session, args map[string]any, forceOperator bool) (detail CallDetail) {
	if t == nil || t.shell == nil {
		return CallDetail{Err: fmt.Errorf("run_script runtime is unavailable")}
	}
	cfg := t.shell.config()
	defer func() { detail.OperatorContext = cfg.OperatorContext && !forceOperator }()
	language, _ := args["language"].(string)
	source, sourceOK := args["source"].(string)
	if !sourceOK || strings.TrimSpace(source) == "" {
		return CallDetail{Err: fmt.Errorf("source is required")}
	}
	if len(source) > 512*1024 {
		return CallDetail{Err: fmt.Errorf("source exceeds the 512 KB limit")}
	}
	if reason := forbiddenSigningCommand(source); reason != "" {
		return CallDetail{Err: fmt.Errorf("source blocked: %s", reason)}
	}
	if denied := deniedCommand(source, cfg.Deny); denied != "" {
		return CallDetail{Err: fmt.Errorf("source blocked by deny list entry %q", denied)}
	}
	if strings.EqualFold(strings.TrimSpace(language), "bash") {
		sandboxID, sandboxStatus, sandboxed := t.shell.sandboxExecution(item)
		if !sandboxed {
			return CallDetail{Err: fmt.Errorf("bash requires the global Docker Sandbox setting")}
		}
		if !sandboxStatus.Available {
			return CallDetail{Err: fmt.Errorf("Docker Sandbox is enabled but inert: %s", sandboxStatus.Reason)}
		}
		if !forceOperator && !cfg.OperatorContext {
			reason := "sandbox execution uses the user's Docker session, outside the agentb-svc identity and firewall boundary"
			return CallDetail{Content: reason, OperatorOverrideReason: reason, Metadata: map[string]any{"target": "sandbox " + sandboxID}}
		}
		timeout := number(args["timeout_s"], cfg.TimeoutS)
		if timeout <= 0 {
			timeout = cfg.TimeoutS
		}
		if timeout > cfg.MaxTimeoutS {
			timeout = cfg.MaxTimeoutS
		}
		return t.shell.callSandbox(ctx, item, sandboxStatus.Executable, sandboxID, []string{"-i", sandboxID, "bash", "-s"}, []byte(source), timeout, cfg)
	}
	var executable string
	var argv []string
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "powershell":
		// Item 2gb: the same interpreter the shell tool resolves.
		executable = shellHostFor(cfg).Executable
		// -Command - consumes standard input as an interactive command stream.
		// Windows PowerShell accepts a line at a time there and silently leaves a
		// multiline construct unexecuted at EOF. Read the complete source first,
		// compile it as one script block, and invoke that block. The source itself
		// still travels on stdin and no script file is created.
		argv = []string{"-NoProfile", "-NonInteractive", "-Command", `$source = [Console]::In.ReadToEnd(); & ([scriptblock]::Create($source))`}
		if reason := forbiddenShellCommand(source, item, t.shell.fileCoordinatorSnapshot()); reason != "" {
			return CallDetail{Err: fmt.Errorf("source blocked: %s", reason)}
		}
	case "python":
		resolved, reason := usableInterpreter("python")
		if reason != "" {
			return CallDetail{Err: fmt.Errorf("%s", reason)}
		}
		executable, argv = resolved, []string{"-"}
	case "node":
		resolved, reason := usableInterpreter("node")
		if reason != "" {
			return CallDetail{Err: fmt.Errorf("%s", reason)}
		}
		executable, argv = resolved, []string{"-"}
	default:
		return CallDetail{Err: fmt.Errorf("language must be powershell, python, node, or bash")}
	}
	timeout := number(args["timeout_s"], cfg.TimeoutS)
	if timeout <= 0 {
		timeout = cfg.TimeoutS
	}
	if timeout > cfg.MaxTimeoutS {
		timeout = cfg.MaxTimeoutS
	}
	output := newLockedBuffer(ctx)
	outside := outsideCommandDecision(source, item, cfg.TrustedFolders)
	if !forceOperator && outside.trusted && outside.card == "" && outside.missing == "" && cfg.ServiceAccount.Enabled && !cfg.OperatorContext {
		forceOperator = true
	}
	if !forceOperator && !cfg.ServiceAccount.Enabled && !cfg.OperatorContext {
		if outside.card != "" {
			return CallDetail{Content: "script was not started: " + outside.card, OperatorOverrideReason: outside.card, Metadata: outsideMetadata(outside.folders, true)}
		}
		if outside.missing != "" {
			return CallDetail{Err: fmt.Errorf("script was not started: %s; there is nothing for the user to allow — check the path, or use a file inside your folder", outside.missing)}
		}
	}
	process, usedService, err := t.shell.startInput(cfg, executable, argv, []byte(source), item.Workspace, output, forceOperator)
	if err != nil {
		var required *operatorOverrideRequired
		if errors.As(err, &required) {
			return CallDetail{Content: "service-account script was not started: " + required.reason, OperatorOverrideReason: required.reason, Metadata: outsideMetadata(outside.folders, false)}
		}
		return CallDetail{Err: err}
	}
	return waitShellProcess(ctx, process, usedService, timeout, cfg, output, executable)
}
