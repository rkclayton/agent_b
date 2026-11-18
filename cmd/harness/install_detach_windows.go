//go:build windows

package main

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os/exec"
	"runtime"
	"syscall"
)

// the installer gets its own process group, so a Ctrl+C in
// the terminal this was started from reaches this wrapper and not the install
// behind it. Closing the window is already survivable once the output is a
// file rather than a pipe; this covers the other way a window ends a child.
func detachChild(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

func lockInstalledLaunch() (func(), error) {
	runtime.LockOSThread()
	name, _ := windows.UTF16PtrFromString(`Local\Agent_b-Launch`)
	handle, err := windows.CreateMutex(nil, false, name)
	if err != nil && err != windows.ERROR_ALREADY_EXISTS {
		runtime.UnlockOSThread()
		return nil, err
	}
	state, err := windows.WaitForSingleObject(handle, 15000)
	if err != nil || (state != windows.WAIT_OBJECT_0 && state != windows.WAIT_ABANDONED) {
		windows.CloseHandle(handle)
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("another Agent_b launch is still being resolved: wait=%d error=%v", state, err)
	}
	return func() { windows.ReleaseMutex(handle); windows.CloseHandle(handle); runtime.UnlockOSThread() }, nil
}
