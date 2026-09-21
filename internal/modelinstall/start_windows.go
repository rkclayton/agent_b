//go:build windows

package modelinstall

import (
	"os/exec"
	"syscall"
)

func startDetached(path string, args []string, directory string) (int, error) {
	command := exec.Command(path, args...)
	command.Dir = directory
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
	if err := command.Start(); err != nil {
		return 0, err
	}
	pid := command.Process.Pid
	if err := command.Process.Release(); err != nil {
		return 0, err
	}
	return pid, nil
}
