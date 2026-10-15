//go:build windows

package main

import (
	"errors"
	"sync"
	"syscall"
	"unsafe"

	"harness/internal/notifications"
)

const (
	wmDesktopNotice     = 0x8000 + 91
	ninBalloonHide      = 0x403
	ninBalloonTimeout   = 0x404
	ninBalloonUserClick = 0x405
	nimAdd              = 0
	nimModify           = 1
	nimDelete           = 2
	nifMessage          = 1
	nifIcon             = 2
	nifTip              = 4
	nifInfo             = 0x10
	niifInfo            = 1
	desktopNoticeID     = 1
)

type notifyIconData struct {
	Size                uint32
	Window              uintptr
	ID, Flags, Callback uint32
	Icon                syscall.Handle
	Tip                 [128]uint16
	State, StateMask    uint32
	Info                [256]uint16
	Timeout             uint32
	InfoTitle           [64]uint16
	InfoFlags           uint32
	GUID                guid
	BalloonIcon         syscall.Handle
}

var (
	shellNotifyIcon  = syscall.NewLazyDLL("shell32.dll").NewProc("Shell_NotifyIconW")
	foregroundWindow = func() uintptr {
		value, _, _ := syscall.NewLazyDLL("user32.dll").NewProc("GetForegroundWindow").Call()
		return value
	}
	desktopNotifyCall = func(command uintptr, data *notifyIconData) uintptr {
		value, _, _ := shellNotifyIcon.Call(command, uintptr(unsafe.Pointer(data)))
		return value
	}
	desktopNoticeIcon  = func() syscall.Handle { return iconForSize(systemMetric(smCXSmIcon), systemMetric(smCYSmIcon)) }
	desktopDestroyIcon = func(icon syscall.Handle) { syscall.NewLazyDLL("user32.dll").NewProc("DestroyIcon").Call(uintptr(icon)) }
	desktopNavigate    = func(window *hostWindow, link string) bool {
		if window.webview == nil {
			return false
		}
		target, err := syscall.UTF16PtrFromString(link)
		return err == nil && comCall(window.webview, 5, uintptr(unsafe.Pointer(target))) == 0
	}
	desktopActivate    = func(hwnd uintptr) { procSetForegroundWindow.Call(hwnd) }
	desktopNoticeState struct {
		sync.Mutex
		selected string
		links    map[uint32]string
		window   uintptr
		icon     syscall.Handle
		added    bool
	}
)

type desktopNotifier struct{}

func (desktopNotifier) Select(session string) {
	desktopNoticeState.Lock()
	desktopNoticeState.selected = session
	desktopNoticeState.Unlock()
}
func (desktopNotifier) Viewing(session string) bool {
	desktopNoticeState.Lock()
	selected := desktopNoticeState.selected
	desktopNoticeState.Unlock()
	hostWindowState.RLock()
	hwnd := hostWindowState.hwnd
	hostWindowState.RUnlock()
	foreground := foregroundWindow()
	return hwnd != 0 && foreground == hwnd && selected == session
}
func (desktopNotifier) Show(notice notifications.DesktopNotice) error {
	hostWindowState.RLock()
	hwnd := hostWindowState.hwnd
	hostWindowState.RUnlock()
	if hwnd == 0 {
		return errors.New("Agent_b window is unavailable")
	}
	desktopNoticeState.Lock()
	if desktopNoticeState.links == nil {
		desktopNoticeState.links = map[uint32]string{}
	}
	clear(desktopNoticeState.links)
	desktopNoticeState.links[desktopNoticeID] = notice.Link
	desktopNoticeState.window = hwnd
	if desktopNoticeState.icon == 0 {
		desktopNoticeState.icon = desktopNoticeIcon()
	}
	command := uintptr(nimAdd)
	if desktopNoticeState.added {
		command = nimModify
	}
	data := notifyIconData{Window: hwnd, ID: desktopNoticeID, Flags: nifMessage | nifIcon | nifTip | nifInfo, Callback: wmDesktopNotice, Icon: desktopNoticeState.icon, InfoFlags: niifInfo}
	data.Size = uint32(unsafe.Sizeof(data))
	copy(data.Tip[:], utf16Units("Agent_b", len(data.Tip)))
	copy(data.InfoTitle[:], utf16Units(notice.Title, len(data.InfoTitle)))
	copy(data.Info[:], utf16Units(notice.Body, len(data.Info)))
	result := desktopNotifyCall(command, &data)
	if result == 0 {
		if !desktopNoticeState.added {
			clear(desktopNoticeState.links)
			desktopNoticeState.window = 0
			if desktopNoticeState.icon != 0 {
				desktopDestroyIcon(desktopNoticeState.icon)
				desktopNoticeState.icon = 0
			}
		}
		desktopNoticeState.Unlock()
		return errors.New("Windows refused the notification")
	}
	desktopNoticeState.added = true
	desktopNoticeState.Unlock()
	return nil
}

func utf16Units(value string, limit int) []uint16 {
	units, _ := syscall.UTF16FromString(value)
	if len(units) > limit {
		units = units[:limit]
		units[limit-1] = 0
	}
	return units
}

func desktopNotificationClicked(window *hostWindow, id uint32) {
	link := desktopNotificationDismissed(id)
	if link == "" {
		return
	}
	if desktopNavigate(window, link) {
		desktopActivate(window.hwnd)
	}
}

func desktopNotificationDismissed(id uint32) string {
	desktopNoticeState.Lock()
	defer desktopNoticeState.Unlock()
	if id != desktopNoticeID || !desktopNoticeState.added {
		return ""
	}
	link := desktopNoticeState.links[id]
	clear(desktopNoticeState.links)
	data := notifyIconData{Window: desktopNoticeState.window, ID: desktopNoticeID}
	data.Size = uint32(unsafe.Sizeof(data))
	desktopNotifyCall(nimDelete, &data)
	desktopNoticeState.added = false
	desktopNoticeState.window = 0
	if desktopNoticeState.icon != 0 {
		desktopDestroyIcon(desktopNoticeState.icon)
		desktopNoticeState.icon = 0
	}
	return link
}
