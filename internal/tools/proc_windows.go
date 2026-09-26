//go:build windows

package tools

import (
	"os/exec"
	"strconv"
	"syscall"
)

// Item 2la (b): no tool call ever surfaces UI. A child gets its own process group
// so it can be stopped as a tree, no window of its own, and no console it could
// prompt on -- the operator desktop is not part of the agent execution
// environment. A child that would block on input reads EOF and fails instead.
func setupProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | createNoWindow, HideWindow: true}
}
func killProcessTree(pid int) {
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = kill.Run()
}
