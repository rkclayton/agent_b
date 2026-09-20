//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// Item 2gv: a failure early enough that there is no Setup page to show it gets
// a message box, because a process started from Explorer has no console to
// print to. MB_ICONERROR | MB_OK | MB_SETFOREGROUND, so it is seen.
const (
	messageBoxOK            = 0x00000000
	messageBoxIconError     = 0x00000010
	messageBoxSetForeground = 0x00010000
)

func showInstallFailure(title, message string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	messageBox := user32.NewProc("MessageBoxW")
	titleUTF16, titleErr := syscall.UTF16PtrFromString(title)
	messageUTF16, messageErr := syscall.UTF16PtrFromString(message)
	if titleErr != nil || messageErr != nil {
		return
	}
	_, _, _ = messageBox.Call(
		0,
		uintptr(unsafe.Pointer(messageUTF16)),
		uintptr(unsafe.Pointer(titleUTF16)),
		uintptr(messageBoxOK|messageBoxIconError|messageBoxSetForeground),
	)
}
