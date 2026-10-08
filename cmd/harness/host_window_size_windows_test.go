//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func TestWindowsCallbacksKeepIntegerParametersOutOfPointerSlots2p5(t *testing.T) {
	source, err := os.ReadFile("host_window_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if strings.Contains(text, "func (w *hostWindow) windowProcedure(hwnd, message, wParam uintptr, lParam unsafe.Pointer)") ||
		strings.Contains(text, "func(hwnd, message, wParam uintptr, lParam unsafe.Pointer)") {
		t.Fatal("lParam is an integer for most window messages but is held in a pointer-typed callback slot")
	}
	for _, want := range []string{
		"func(this uintptr, iid unsafe.Pointer, object *uintptr)",
		"func(this, errorCode uintptr, result unsafe.Pointer)",
		"func(hwnd, message, wParam, lParam uintptr)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("callback inventory is missing %q", want)
		}
	}
}

// Item 2nm: "i still can't drag the bottom chat window all the way down to collapse the
// window ... i want to be able to shrink it down."
//
// W0 measured that the frame stated nothing: WM_GETMINMAXINFO was declared in this file
// and never answered, so the window carried Windows' own default track size. These are
// the two halves of the fix — the stated minimum, and the size being remembered.

// (b): the frame answers WM_GETMINMAXINFO with the minimum the page needs, and that
// number is the only clamp the window has.
func TestTheFrameStatesItsMinimum2nm(t *testing.T) {
	window := &hostWindow{}
	class, _ := syscall.UTF16PtrFromString("AgentBMinimumProbe")
	title, _ := syscall.UTF16PtrFromString("probe")
	instance, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(0)
	proc := syscall.NewCallback(func(hwnd, message, wParam, lParam uintptr) uintptr {
		if message == wmGetMinMaxInfo {
			return window.windowProcedure(hwnd, message, wParam, lParam)
		}
		result, _, _ := procDefWindowProc.Call(hwnd, message, wParam, lParam)
		return result
	})
	registration := wndClassEx{wndProc: proc, instance: syscall.Handle(instance), className: class}
	registration.size = uint32(unsafe.Sizeof(registration))
	if atom, _, _ := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&registration))); atom == 0 {
		t.Skip("the probe class could not be registered")
	}
	var away int32 = -32000
	offscreen := uintptr(uint32(away))
	// Off the screen and never shown: this measures the clamp, not the operator's
	// desktop, and the standing rule is that nothing takes his screen.
	hwnd, _, _ := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow, offscreen, offscreen, 800, 600, 0, 0, instance, 0)
	if hwnd == 0 {
		t.Skip("the probe window could not be created")
	}
	defer procDestroyWindow.Call(hwnd)

	var info minMaxInfo
	procSendMessage.Call(hwnd, wmGetMinMaxInfo, 0, uintptr(unsafe.Pointer(&info)))
	wantX, wantY := hostMinimumTrack(hwnd)
	if info.minTrackSize.x != wantX || info.minTrackSize.y != wantY {
		t.Fatalf("the frame states %dx%d, not %dx%d", info.minTrackSize.x, info.minTrackSize.y, wantX, wantY)
	}
	// And the stated minimum is small: the complaint is that the window would not
	// shrink, so a "minimum" near the default size would be the same defect again.
	if hostMinWidth > 640 || hostMinHeight > 480 {
		t.Fatalf("the stated minimum %dx%d is not a minimum", hostMinWidth, hostMinHeight)
	}
}

func TestNativeTitleFollowsSwitchAndRenameOffscreen2sh(t *testing.T) {
	class, _ := syscall.UTF16PtrFromString("AgentBTitleProbe")
	title, _ := syscall.UTF16PtrFromString("Agent_b")
	instance, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(0)
	proc := syscall.NewCallback(func(hwnd, message, wParam, lParam uintptr) uintptr {
		result, _, _ := procDefWindowProc.Call(hwnd, message, wParam, lParam)
		return result
	})
	registration := wndClassEx{wndProc: proc, instance: syscall.Handle(instance), className: class}
	registration.size = uint32(unsafe.Sizeof(registration))
	if atom, _, _ := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&registration))); atom == 0 {
		t.Skip("the title probe class could not be registered")
	}
	var away int32 = -32000
	offscreen := uintptr(uint32(away))
	hwnd, _, _ := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)), wsOverlappedWindow, offscreen, offscreen, 800, 600, 0, 0, instance, 0)
	if hwnd == 0 {
		t.Skip("the offscreen title probe window could not be created")
	}
	defer procDestroyWindow.Call(hwnd)
	hostWindowState.Lock()
	previous := hostWindowState.hwnd
	hostWindowState.hwnd = hwnd
	hostWindowState.Unlock()
	defer func() { hostWindowState.Lock(); hostWindowState.hwnd = previous; hostWindowState.Unlock() }()

	getWindowText := user32.NewProc("GetWindowTextW")
	readTitle := func() string {
		buffer := make([]uint16, 128)
		length, _, _ := getWindowText.Call(hwnd, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
		return syscall.UTF16ToString(buffer[:length])
	}
	for _, want := range []string{"Agent_b - Switched chat", "Agent_b - Renamed chat"} {
		if !requestHostWindowAction("title", want) {
			t.Fatalf("setting %q was refused", want)
		}
		if got := readTitle(); got != want {
			t.Fatalf("native title=%q want %q", got, want)
		}
	}
}

// (d): a small size survives a relaunch, and a placement that is nonsense or off every
// screen does not come back.
func TestASmallSizeIsRemembered2nm(t *testing.T) {
	root := t.TempDir()
	window := &hostWindow{userDataDir: filepath.Join(root, "webview2")}
	path := window.placementPath()
	if filepath.Dir(path) != root {
		t.Fatalf("the placement is written to %q, which is not the data root", path)
	}

	write := func(placement hostPlacement) {
		data, err := json.Marshal(placement)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(hostPlacement{X: 100, Y: 120, Width: hostMinWidth, Height: hostMinHeight})
	if got := window.readPlacement(); got.Width != hostMinWidth || got.Height != hostMinHeight || got.X != 100 {
		t.Fatalf("the smallest allowed size did not come back: %+v", got)
	}
	// Smaller than the stated minimum is not a size this window can open at.
	write(hostPlacement{X: 100, Y: 120, Width: 40, Height: 30})
	if got := window.readPlacement(); got.Width != 0 {
		t.Fatalf("a size below the minimum came back: %+v", got)
	}
	// A monitor that is no longer there.
	write(hostPlacement{X: -30000, Y: -30000, Width: 800, Height: 600})
	if got := window.readPlacement(); got.Width != 0 {
		t.Fatalf("a placement off every screen came back: %+v", got)
	}
	// Nothing written at all is not an error, it is the default size.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := window.readPlacement(); got.Width != 0 || got.Height != 0 {
		t.Fatalf("a missing placement invented one: %+v", got)
	}
	// And a window with no data root writes nothing rather than guessing a path.
	if (&hostWindow{}).placementPath() != "" {
		t.Fatal("a window with no data root still names a placement file")
	}
}
