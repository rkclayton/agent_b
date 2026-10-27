//go:build windows

package main

import (
	"syscall"
	"testing"
	"unsafe"
)

// Item 2x0: the failure window holds the report and one Copy button. It is built
// hidden and destroyed, so the test never puts it on the screen.
func TestStartupFailureWindowHoldsReportAndCopy2x0(t *testing.T) {
	findChild := user32.NewProc("FindWindowExW")
	getText := user32.NewProc("GetWindowTextW")
	hwnd := createStartupFailureWindow("Agent_b could not start.\r\n\r\nconfig: broken\r\n", false)
	if hwnd == 0 {
		t.Fatal("the failure window was not created")
	}
	defer procDestroyWindow.Call(hwnd)
	text := func(class string) string {
		name, _ := syscall.UTF16PtrFromString(class)
		child, _, _ := findChild.Call(hwnd, 0, uintptr(unsafe.Pointer(name)), 0)
		if child == 0 {
			t.Fatalf("no %s control", class)
		}
		buffer := make([]uint16, 512)
		getText.Call(child, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
		return syscall.UTF16ToString(buffer)
	}
	if got := text("EDIT"); got != "Agent_b could not start.\r\n\r\nconfig: broken\r\n" {
		t.Fatalf("report = %q", got)
	}
	if got := text("BUTTON"); got != "Copy" {
		t.Fatalf("button = %q", got)
	}
}
