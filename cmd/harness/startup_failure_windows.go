//go:build windows

package main

import (
	"runtime"
	"syscall"
	"unsafe"
)

const (
	startupFailureCopyID = 1
	wmCommand            = 0x0111
	wmSetFont            = 0x0030
	wmCopy               = 0x0301
	emSetSel             = 0x00B1
	wsChild              = 0x40000000
	wsVisible            = 0x10000000
	wsVScroll            = 0x00200000
	wsBorder             = 0x00800000
	esMultiline          = 0x0004
	esReadOnly           = 0x0800
	esAutoVScroll        = 0x0040
	defaultGUIFont       = 17
)

var (
	procMoveWindow     = user32.NewProc("MoveWindow")
	procGetStockObject = syscall.NewLazyDLL("gdi32.dll").NewProc("GetStockObject")
)

// showStartupFailure shows the failure window and returns when it is closed.
func showStartupFailure(report string) {
	hwnd := createStartupFailureWindow(report, true)
	if hwnd == 0 {
		showInstallFailure("Agent_b could not start", report)
		return
	}
	var message windowMessage
	for {
		result, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(result) <= 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&message)))
	}
}

// createStartupFailureWindow builds the window: the report in a read-only text
// box and one Copy button. Shown is false only in its test, which never puts it
// on the screen.
func createStartupFailureWindow(report string, shown bool) uintptr {
	runtime.LockOSThread()
	className, _ := syscall.UTF16PtrFromString("Agent_b-startup-failure")
	instance, _, _ := procGetModuleHandle.Call(0)
	var edit, button uintptr
	layout := func(hwnd uintptr) {
		var client struct{ left, top, right, bottom int32 }
		procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&client)))
		procMoveWindow.Call(edit, 8, 8, uintptr(client.right-16), uintptr(client.bottom-52), 1)
		procMoveWindow.Call(button, 8, uintptr(client.bottom-36), 80, 28, 1)
	}
	procedure := syscall.NewCallback(func(hwnd, message, wParam, lParam uintptr) uintptr {
		switch message {
		case wmSize:
			if edit != 0 {
				layout(hwnd)
			}
		case wmCommand:
			if wParam&0xFFFF == startupFailureCopyID {
				procSendMessage.Call(edit, emSetSel, 0, ^uintptr(0))
				procSendMessage.Call(edit, wmCopy, 0, 0)
			}
		case wmDestroy:
			procPostQuitMessage.Call(0)
		}
		result, _, _ := procDefWindowProc.Call(hwnd, message, wParam, lParam)
		return result
	})
	cursor, _, _ := procLoadCursor.Call(0, 32512)                                                                                                                     // IDC_ARROW
	class := wndClassEx{wndProc: procedure, instance: syscall.Handle(instance), className: className, cursor: syscall.Handle(cursor), background: syscall.Handle(16)} // COLOR_BTNFACE + 1
	class.size = uint32(unsafe.Sizeof(class))
	procRegisterClassEx.Call(uintptr(unsafe.Pointer(&class)))
	title, _ := syscall.UTF16PtrFromString("Agent_b could not start")
	hwnd, _, _ := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow, cwUseDefault, cwUseDefault, 760, 460, 0, 0, instance, 0)
	if hwnd == 0 {
		return 0
	}
	editClass, _ := syscall.UTF16PtrFromString("EDIT")
	text, _ := syscall.UTF16PtrFromString(report)
	edit, _, _ = procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(editClass)), uintptr(unsafe.Pointer(text)),
		wsChild|wsVisible|wsVScroll|wsBorder|esMultiline|esReadOnly|esAutoVScroll, 0, 0, 0, 0, hwnd, 0, instance, 0)
	buttonClass, _ := syscall.UTF16PtrFromString("BUTTON")
	label, _ := syscall.UTF16PtrFromString("Copy")
	button, _, _ = procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(buttonClass)), uintptr(unsafe.Pointer(label)),
		wsChild|wsVisible, 0, 0, 0, 0, hwnd, startupFailureCopyID, instance, 0)
	font, _, _ := procGetStockObject.Call(defaultGUIFont)
	procSendMessage.Call(edit, wmSetFont, font, 1)
	procSendMessage.Call(button, wmSetFont, font, 1)
	layout(hwnd)
	if shown {
		procShowWindow.Call(hwnd, swShowNormal)
		procShowWindow.Call(hwnd, swShowNormal)
	}
	return hwnd
}
