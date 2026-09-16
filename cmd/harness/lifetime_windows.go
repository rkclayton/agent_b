//go:build windows

package main

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
	wmQueryEndSession              = 0x0011
	wmEndSession                   = 0x0016
	wmClose                        = 0x0010
	endSessionCloseApp             = 0x00000001
	endSessionCritical             = 0x40000000
	endSessionLogoff               = 0x80000000
)

var (
	user32              = syscall.NewLazyDLL("user32.dll")
	procRegisterClassEx = user32.NewProc("RegisterClassExW")
	procCreateWindowEx  = user32.NewProc("CreateWindowExW")
	procDefWindowProc   = user32.NewProc("DefWindowProcW")
	procGetMessage      = user32.NewProc("GetMessageW")
	procDispatchMessage = user32.NewProc("DispatchMessageW")
	procGetModuleHandle = syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW")
	sessionEndClassName = fmt.Sprintf("Agent_b-session-end-%d", os.Getpid())
)

type wndClassEx struct {
	size       uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   syscall.Handle
	icon       syscall.Handle
	cursor     syscall.Handle
	background syscall.Handle
	menuName   *uint16
	className  *uint16
	iconSm     syscall.Handle
}

type windowMessage struct {
	hwnd    syscall.Handle
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	x, y    int32
	private uint32
}

func processCreated(pid int) int64 {
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return 0
	}
	defer syscall.CloseHandle(handle)
	var creation, exit, kernel, user syscall.Filetime
	if syscall.GetProcessTimes(handle, &creation, &exit, &kernel, &user) != nil {
		return 0
	}
	return creation.Nanoseconds()
}

// processRunning is true only for the same process: a live PID with a different
// creation time is a reused PID, and the earlier process has ended.
func processRunning(pid int, created int64) bool {
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)
	var code uint32
	if syscall.GetExitCodeProcess(handle, &code) != nil || code != stillActive {
		return false
	}
	var creation, exit, kernel, user syscall.Filetime
	if syscall.GetProcessTimes(handle, &creation, &exit, &kernel, &user) != nil {
		return true
	}
	return created == 0 || creation.Nanoseconds() == created
}

// watchSessionEnd owns a hidden top-level window so the process receives
// WM_ENDSESSION when Windows ends the logon session (logoff, shutdown, a Fast
// Startup shutdown). A process with no window is terminated without notice, and
// console control events are not delivered to an interactive user's process at
// logoff. The reason is written synchronously: Windows may end the process as
// soon as the handler returns.
// WM_CLOSE (taskkill without /F, the installer's graceful stop) is a stop
// request, answered like the console close signal rather than by destroying the
// window and leaving the process running.
func watchSessionEnd(record func(string), closeRequested func()) {
	go func() {
		runtime.LockOSThread()
		className, _ := syscall.UTF16PtrFromString(sessionEndClassName)
		instance, _, _ := procGetModuleHandle.Call(0)
		callback := syscall.NewCallback(func(hwnd syscall.Handle, message uint32, wParam, lParam uintptr) uintptr {
			switch message {
			case wmQueryEndSession:
				return 1
			case wmClose:
				closeRequested()
				return 0
			case wmEndSession:
				if wParam != 0 {
					record(sessionEndReason(lParam))
				}
				return 0
			}
			result, _, _ := procDefWindowProc.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
			return result
		})
		class := wndClassEx{wndProc: callback, instance: syscall.Handle(instance), className: className}
		class.size = uint32(unsafe.Sizeof(class))
		if atom, _, _ := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&class))); atom == 0 {
			return
		}
		hwnd, _, _ := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(className)), 0, 0, 0, 0, 0, 0, 0, instance, 0)
		if hwnd == 0 {
			return
		}
		var message windowMessage
		for {
			result, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
			if int32(result) <= 0 {
				return
			}
			procDispatchMessage.Call(uintptr(unsafe.Pointer(&message)))
		}
	}()
}

func sessionEndReason(flags uintptr) string {
	switch {
	case flags&endSessionLogoff != 0:
		return "the Windows session is logging off (this includes a Fast Startup shutdown)"
	case flags&endSessionCritical != 0:
		return "Windows is shutting down (critical)"
	case flags&endSessionCloseApp != 0:
		return "Windows asked applications to close (installer or restart manager)"
	default:
		return "Windows is shutting down or restarting"
	}
}
