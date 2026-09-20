//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// Item 2gl (v1.2.6): the installer gets its own process group, so a Ctrl+C in
// the terminal this was started from reaches this wrapper and not the install
// behind it. Closing the window is already survivable once the output is a
// file rather than a pipe; this covers the other way a window ends a child.
func detachChild(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
