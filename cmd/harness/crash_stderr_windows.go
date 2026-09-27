//go:build windows

package main

import (
	"os"
	"syscall"
)

// GetConsoleWindow returns 0 when the process has no console, which is exactly
// what a detached launch looks like. The other user32 and kernel32 bindings live
// in lifetime_windows.go; this is the only one this file needs.
var procSetStdHandle = syscall.NewLazyDLL("kernel32.dll").NewProc("SetStdHandle")

// STD_ERROR_HANDLE, from the Windows headers.
const stdErrorHandle = ^uintptr(11) + 1

// Item 2mt (a): KEEP THE RUNTIME'S OWN TRACEBACK.
//
// A Go panic that nothing recovers prints its traceback to stderr and exits. The
// launcher detaches the process and does not persist stderr — AGENTS.md states
// that plainly — so on the operator's machine that traceback went nowhere, which
// is the whole reason his crash at 03:10:50 left no line naming a cause.
//
// A recover() cannot cover everything: a native fault inside WebView2 or a runtime
// throw is not recoverable in Go. The one thing that still records those is the
// runtime's own output, so stderr is given a file to land in.
//
// The CALLER decides when to do this, and only the detached host-window launch
// does. An earlier draft inferred it from "no console attached", which is also
// true under `go test`: it redirected the test binary's own stderr into a temp
// directory and held the handle, which broke cleanup. Silently swallowing a
// developer's output would be a worse bug than the one this fixes.
func keepRuntimeTraceback(path string) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	// BOTH handles matter. os.Stderr is what Go's own printing uses; the process's
	// STD_ERROR_HANDLE is what the runtime writes a fatal traceback through and
	// what any child inherits. Setting one and not the other loses half of it.
	procSetStdHandle.Call(stdErrorHandle, file.Fd())
	os.Stderr = file
}
