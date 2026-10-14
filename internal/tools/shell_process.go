package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"harness/internal/config"
)

func RunOperatorHook(ctx context.Context, cfg config.Shell, command string, input []byte, stdout, stderr io.Writer) (int, error) {
	if len(cfg.Command) == 0 {
		return -1, errors.New("shell command is not configured")
	}
	cmd := exec.Command(cfg.Command[0], append(append([]string(nil), cfg.Command[1:]...), command)...)
	setupProcess(cmd)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(input), stdout, stderr
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), nil
		}
		return 0, err
	case <-ctx.Done():
		killProcessTree(cmd.Process.Pid)
		<-done
		return -1, ctx.Err()
	}
}

var (
	ErrServiceCredentialRejected = errors.New("service-account credential rejected")
	ErrServiceAccountLocked      = errors.New("service account locked out")
)

type runningShellProcess interface {
	Wait() (int, error)
	KillTree()
}

type interactiveShellProcess interface{ WriteInput([]byte) error }

type execShellProcess struct {
	cmd   *exec.Cmd
	input io.WriteCloser
}

func startHarnessProcess(executable string, argv []string, workspace string, output *lockedBuffer) (runningShellProcess, error) {
	return startHarnessProcessWithInput(executable, argv, workspace, nil, output)
}

func startHarnessProcessWithInput(executable string, argv []string, workspace string, input []byte, output *lockedBuffer) (runningShellProcess, error) {
	cmd := exec.Command(executable, argv...)
	cmd.Dir = workspace
	setupProcess(cmd)
	cmd.Stdout, cmd.Stderr = output, output
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execShellProcess{cmd: cmd}, nil
}

func startHarnessBackgroundProcess(executable string, argv []string, workspace string, output *lockedBuffer) (runningShellProcess, error) {
	cmd := exec.Command(executable, argv...)
	cmd.Dir = workspace
	setupProcess(cmd)
	cmd.Stdout, cmd.Stderr = output, output
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = input.Close()
		return nil, err
	}
	return &execShellProcess{cmd: cmd, input: input}, nil
}

func (p *execShellProcess) Wait() (int, error) {
	err := p.cmd.Wait()
	if err == nil {
		return 0, nil
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), nil
	}
	return 0, err
}

func (p *execShellProcess) KillTree() { killProcessTree(p.cmd.Process.Pid) }
func (p *execShellProcess) WriteInput(value []byte) error {
	if p.input == nil {
		return errors.New("process input is closed")
	}
	_, err := p.input.Write(value)
	return err
}

type serviceProcessStarter func(string, []string, string, []string, config.ShellServiceAccount, []byte, *lockedBuffer) (runningShellProcess, error)
type serviceProcessInputStarter func(string, []string, string, []string, config.ShellServiceAccount, []byte, []byte, *lockedBuffer) (runningShellProcess, error)

type serviceSpawnError struct {
	kind string
	err  error
}

func (e *serviceSpawnError) Error() string {
	if e.err == nil {
		return e.kind
	}
	return fmt.Sprintf("%s: %v", e.kind, e.err)
}

func (e *serviceSpawnError) Unwrap() error { return e.err }

func serviceSpawnReason(err error) string {
	if typed, ok := err.(*serviceSpawnError); ok {
		return typed.Error()
	}
	return fmt.Sprintf("service-account process API error: %v", err)
}
