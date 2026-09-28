//go:build windows

// Package quietproc starts child processes without putting a window on the
// operator's screen.
//
// Item 2na: he works on this machine while Agent_b and its gates run, and a child
// started with the platform's defaults takes whatever window the platform gives it —
// a console for every PowerShell, a real window for the installer. Item 2nf (d) is the
// same rule met from the other side: his Update attempt left a console behind after the
// installer had already exited, with nothing readable in it.
//
// One helper rather than a flag per site, so a site added later cannot forget.
package quietproc

import (
	"os/exec"
	"syscall"
)

// createNoWindow is CREATE_NO_WINDOW. A console process started with it gets no
// console at all, so there is no window to linger and none to steal focus.
const createNoWindow = 0x08000000

// Quiet prepares a command to run with no window. It is safe to call on a command
// that already carries SysProcAttr: the flags are added, nothing is replaced.
func Quiet(command *exec.Cmd) *exec.Cmd {
	if command == nil {
		return command
	}
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.HideWindow = true
	command.SysProcAttr.CreationFlags |= createNoWindow
	return command
}
