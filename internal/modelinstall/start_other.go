//go:build !windows

package modelinstall

import (
	"context"
	"errors"
	"harness/internal/quietproc"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"
)

func startDetached(string, []string, string) (int, error) {
	return 0, errors.New("local model installation is supported only on Windows")
}

func launchManaged(s Spec) (*managedProcess, error) {
	if s.StartCommand != "" {
		return launchCommand(s)
	}
	if !insideRoot(s.Root, s.Executable) {
		return nil, errors.New("installed executable ownership could not be proved")
	}
	cmd := quietproc.Quiet(exec.Command(s.Executable, s.Args...))
	cmd.Dir = s.Root
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var exited atomic.Bool
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); exited.Store(true); close(done) }()
	return &managedProcess{exited: exited.Load, stop: func() error {
		if !exited.Load() {
			if err := cmd.Process.Kill(); err != nil {
				return err
			}
			<-done
		}
		return nil
	}}, nil
}
func findManaged(s Spec) (*managedProcess, error) {
	if s.StartCommand != "" {
		return findCommand(s)
	}
	return nil, nil
}
func runStopCommand(s Spec) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", s.StopCommand)
	cmd.Dir = s.Root
	if cmd.Run() != nil {
		return errors.New("stop command failed")
	}
	return nil
}
func launchCommand(s Spec) (*managedProcess, error) {
	cmd := exec.Command("/bin/sh", "-c", s.StartCommand)
	cmd.Dir = s.Root
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if cmd.Start() != nil {
		return nil, errors.New("start command failed")
	}
	var exited atomic.Bool
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); exited.Store(true); close(done) }()
	stop := func(command string) error {
		if command != "" {
			copy := s
			copy.StopCommand = command
			if err := runStopCommand(copy); err != nil {
				return err
			}
		} else if syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) != nil && !exited.Load() {
			return errors.New("command tree did not stop")
		}
		select {
		case <-done:
			return nil
		case <-time.After(10 * time.Second):
			return errors.New("command tree did not stop")
		}
	}
	return &managedProcess{exited: exited.Load, stop: func() error { return stop(s.StopCommand) }, commandStop: stop}, nil
}
func claimInstalled(int, Spec) (*managedProcess, error) {
	return nil, errors.New("installed process adoption is Windows-only")
}
