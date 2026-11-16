//go:build !windows

package modelinstall

import (
	"errors"
	"harness/internal/quietproc"
	"os/exec"
	"sync/atomic"
)

func startDetached(string, []string, string) (int, error) {
	return 0, errors.New("local model installation is supported only on Windows")
}

func launchManaged(s Spec) (*managedProcess, error) {
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
func findManaged(Spec) (*managedProcess, error) { return nil, nil }
func claimInstalled(int, Spec) (*managedProcess, error) {
	return nil, errors.New("installed process adoption is Windows-only")
}
