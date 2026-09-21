//go:build windows

package main

// Item 2hc (v1.3.0/W2): our own window, so the title bar is ours.
//
// THE FRAME. The window keeps Windows' own left, right and bottom borders and
// loses only the caption: WM_NCCALCSIZE gives the client area the band the
// title bar used to occupy, so the page's 32 px strip becomes the window's top
// edge. Measured before: an --app= window puts 36 px of Edge chrome above a
// 32 px page header, 68 px above content. After: 32 px, and that 32 px is ours.
//
// WHAT WINDOWS STILL OWNS, deliberately. Minimise, maximise, close, snap,
// double-click-to-maximise and the system menu are not reimplemented: the top
// right 108 px (three 36 px buttons, measured in W1) answer WM_NCHITTEST with
// HTMINBUTTON, HTMAXBUTTON and HTCLOSE, so Windows performs the action and
// Windows 11 shows its snap layouts on the maximise button. The page paints the
// glyphs; it never handles the clicks.
//
// NO HOST OBJECT, NO INJECTED SCRIPT, NO CUSTOM SCHEME. The page is the same
// page the server already serves over loopback. Dragging works through
// IsNonClientRegionSupportEnabled, which makes the CSS `app-region: drag` that
// web/css/tokens.css already carries on `.window-titlebar` mean what it says --
// a browser feature, not a channel we opened.

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	hostStripHeight   = 32 // the page's own strip; W1 measured the native caption at 31 px
	hostButtonWidth   = 36 // SM_CXSIZE, measured in W1
	hostButtonCount   = 3
	hostResizeBorder  = 8 // SM_CXFRAME + SM_CXPADDEDBORDER, measured in W1
	hostDefaultWidth  = 1280
	hostDefaultHeight = 860

	wmDestroy       = 0x0002
	wmSize          = 0x0005
	wmNCCalcSize    = 0x0083
	wmNCHitTest     = 0x0084
	wmGetMinMaxInfo = 0x0024
	swShowNormal    = 1

	htClient      = 1
	htCaption     = 2
	htLeft        = 10
	htRight       = 11
	htTop         = 12
	htTopLeft     = 13
	htTopRight    = 14
	htBottom      = 15
	htBottomLeft  = 16
	htBottomRight = 17
	htMinButton   = 8
	htMaxButton   = 9
	htClose       = 20

	wsOverlappedWindow = 0x00CF0000
	cwUseDefault       = ^uintptr(0x7FFFFFFF) // CW_USEDEFAULT

	smCXScreen = 0
	smCYScreen = 1
)

var (
	procDestroyWindow   = user32.NewProc("DestroyWindow")
	procPostQuitMessage = user32.NewProc("PostQuitMessage")
	procShowWindow      = user32.NewProc("ShowWindow")
	procUpdateWindow    = user32.NewProc("UpdateWindow")
	procGetClientRect   = user32.NewProc("GetClientRect")
	procGetWindowRect   = user32.NewProc("GetWindowRect")
	procScreenToClient  = user32.NewProc("ScreenToClient")
	procIsZoomed        = user32.NewProc("IsZoomed")
	procGetSystemMetric = user32.NewProc("GetSystemMetrics")
	procLoadCursor      = user32.NewProc("LoadCursorW")
	procSetWindowText   = user32.NewProc("SetWindowTextW")
)

type rect struct{ left, top, right, bottom int32 }

type point struct{ x, y int32 }

// hostWindow is the whole of our side of the window. One per process: the
// product is one process and one binary, and the window is a mode of it.
type hostWindow struct {
	hwnd       uintptr
	controller unsafe.Pointer
	webview    unsafe.Pointer

	environmentHandler *comHandler
	controllerHandler  *comHandler

	url         string
	userDataDir string
	ready       chan error
	classAtom   uintptr
	windowProc  uintptr
}

// comHandler is a COM object implemented in Go: a pointer to a vtable, followed
// by nothing the caller may touch. The vtable and the object are kept alive by
// the hostWindow that owns them, so neither is collected while WebView2 holds a
// reference.
type comHandler struct {
	vtable *[4]uintptr
	// keepalive holds the syscall trampolines so they outlive the call.
	keepalive []uintptr
}

func newCOMHandler(invoke func(errorCode uintptr, result unsafe.Pointer) uintptr) *comHandler {
	handler := &comHandler{vtable: new([4]uintptr)}
	queryInterface := syscall.NewCallback(func(this uintptr, iid unsafe.Pointer, object *uintptr) uintptr {
		// The only interfaces we claim are IUnknown and the one handler
		// interface this object was made for; WebView2 asks for nothing else.
		if object != nil {
			*object = this
		}
		return 0
	})
	addRef := syscall.NewCallback(func(this uintptr) uintptr { return 1 })
	release := syscall.NewCallback(func(this uintptr) uintptr { return 1 })
	invokeCallback := syscall.NewCallback(func(this, errorCode uintptr, result unsafe.Pointer) uintptr {
		return invoke(errorCode, result)
	})
	handler.vtable[0] = queryInterface
	handler.vtable[1] = addRef
	handler.vtable[2] = release
	handler.vtable[3] = invokeCallback
	handler.keepalive = []uintptr{queryInterface, addRef, release, invokeCallback}
	return handler
}

// pointer is the address the COM caller sees: a pointer to a pointer to the
// vtable, which is exactly what a COM interface pointer is.
func (h *comHandler) pointer() uintptr { return uintptr(unsafe.Pointer(&h.vtable)) }

// comCall invokes method `index` on a COM interface pointer. An interface
// pointer points at the vtable pointer, so the method is two dereferences away.
// A COM interface pointer is kept as unsafe.Pointer rather than uintptr all the
// way through: the vtable walk is then pointer-to-pointer arithmetic that go vet
// accepts, instead of a uintptr round trip it rightly distrusts.
func comCall(iface unsafe.Pointer, index int, args ...uintptr) uintptr {
	if iface == nil {
		return 0x80004003 // E_POINTER
	}
	vtable := *(**[64]uintptr)(iface)
	method := vtable[index]
	result, _, _ := syscall.SyscallN(method, append([]uintptr{uintptr(iface)}, args...)...)
	return result
}

func comRelease(iface unsafe.Pointer) {
	if iface != nil {
		comCall(iface, 2)
	}
}

func systemMetric(index int) int32 {
	value, _, _ := procGetSystemMetric.Call(uintptr(index))
	return int32(value)
}

// hostFrameDebug is on only with AGENTB_HOST_FRAME_DEBUG, so the smoke can see
// which frame messages arrive without the product logging them in service.
func hostFrameDebug(format string, args ...any) {
	if os.Getenv("AGENTB_HOST_FRAME_DEBUG") == "" {
		return
	}
	log.Printf("host frame: "+format, args...)
}

func isMaximized(hwnd uintptr) bool {
	zoomed, _, _ := procIsZoomed.Call(hwnd)
	return zoomed != 0
}

// windowProcedure is the whole of the frame. Everything it does is either
// "give the client area the caption's band" or "tell Windows which part of the
// frame this point is", and Windows does the rest.
// lParam arrives as unsafe.Pointer, not uintptr, because WM_NCCALCSIZE hands
// us a NCCALCSIZE_PARAMS to write through. Taking it as a pointer and deriving
// the integer where coordinates are wanted keeps every conversion in the
// direction go vet accepts; the reverse would be a uintptr the GC never saw.
func (w *hostWindow) windowProcedure(hwnd, message, wParam uintptr, lParam unsafe.Pointer) uintptr {
	switch message {
	case wmNCCalcSize:
		// BOTH forms matter. With wParam TRUE lParam is an NCCALCSIZE_PARAMS,
		// with wParam FALSE it is a bare RECT -- and since NCCALCSIZE_PARAMS
		// begins with rgrc[0], the same pointer serves for both. The first
		// draft handled only the TRUE form and let the FALSE one fall through
		// to DefWindowProc, which put the caption straight back: the frame
		// measured 31 px of chrome above the page, exactly the title bar the
		// window was supposed to have lost.
		//
		// Leaving the rectangle unmodified makes the client area the whole
		// window, so the page's 32 px strip IS the top edge.
		hostFrameDebug("NCCALCSIZE wParam=%d", wParam)
		params := (*rect)(lParam)
		// A MAXIMISED window is the exception: without insetting by the frame
		// it spills over the screen edge and the taskbar, which is the classic
		// borderless-window bug.
		if isMaximized(hwnd) {
			border := systemMetric(32) + systemMetric(92) // SM_CXFRAME + SM_CXPADDEDBORDER
			borderY := systemMetric(33) + systemMetric(92)
			params.left += border
			params.right -= border
			params.top += borderY
			params.bottom -= borderY
		}
		return 0

	case wmNCHitTest:
		// Windows keeps every caption behaviour; it is only told where things
		// are. The page paints the button glyphs and never sees these clicks.
		var window rect
		procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&window)))
		position := uintptr(lParam)
		x := int32(int16(position & 0xFFFF))
		y := int32(int16((position >> 16) & 0xFFFF))
		relativeX, relativeY := x-window.left, y-window.top
		width := window.right - window.left

		if !isMaximized(hwnd) {
			atTop := relativeY < hostResizeBorder
			atBottom := y >= window.bottom-hostResizeBorder
			atLeft := relativeX < hostResizeBorder
			atRight := x >= window.right-hostResizeBorder
			switch {
			case atTop && atLeft:
				return htTopLeft
			case atTop && atRight:
				return htTopRight
			case atBottom && atLeft:
				return htBottomLeft
			case atBottom && atRight:
				return htBottomRight
			case atTop:
				return htTop
			case atBottom:
				return htBottom
			case atLeft:
				return htLeft
			case atRight:
				return htRight
			}
		}

		if relativeY < hostStripHeight {
			buttons := int32(hostButtonWidth * hostButtonCount)
			if relativeX >= width-buttons {
				switch (relativeX - (width - buttons)) / hostButtonWidth {
				case 0:
					return htMinButton
				case 1:
					return htMaxButton
				default:
					return htClose
				}
			}
			// Everything else in the strip is the page's. Dragging comes from
			// the CSS app-region the WebView reports, not from claiming the
			// whole strip as caption here -- claiming it would take the clicks
			// away from the tabs and the gear.
			return htClient
		}
		return htClient

	case wmSize:
		w.resizeController()
		return 0

	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	result, _, _ := procDefWindowProc.Call(hwnd, message, wParam, uintptr(lParam))
	return result
}

func (w *hostWindow) resizeController() {
	if w.controller == nil {
		return
	}
	var client rect
	procGetClientRect.Call(w.hwnd, uintptr(unsafe.Pointer(&client)))
	comCall(w.controller, 6, uintptr(unsafe.Pointer(&client))) // put_Bounds
}

// hostFrameInsets reports what the window costs above the document: the
// difference between the window's top edge and the client area's. It is the
// number the order asks for, read from Windows rather than asserted.
func hostFrameInsets(hwnd uintptr) (top int32, err error) {
	var window, client rect
	if ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&window))); ok == 0 {
		return 0, fmt.Errorf("GetWindowRect failed")
	}
	if ok, _, _ := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&client))); ok == 0 {
		return 0, fmt.Errorf("GetClientRect failed")
	}
	origin := point{}
	procScreenToClient.Call(hwnd, uintptr(unsafe.Pointer(&origin)))
	// origin is the window's client origin expressed in client coordinates of
	// the point (0,0) on screen; the top inset is the client top in screen
	// space minus the window top.
	var clientOrigin point
	procClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&clientOrigin)))
	return clientOrigin.y - window.top, nil
}

var procClientToScreen = user32.NewProc("ClientToScreen")

// runHostWindow opens the window and does not return until it closes. It runs
// on the calling goroutine's OS thread: a window and its message pump belong to
// one thread, and WebView2's completion handlers arrive on that pump.
//
// Any failure before the page is showing returns an error, and the caller falls
// back to the browser window with the reason logged. The product never loses
// its way in: the host is an improvement on the window, not a new dependency
// for running at all.
func runHostWindow(url, userDataDir, title string) (err error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if _, probeErr := hostWindowAvailable(); probeErr != nil {
		return probeErr
	}

	// Apartment-threaded: WebView2 requires an STA on the UI thread.
	procCoInitializeEx.Call(0, 2)
	defer procCoUninitialize.Call()

	window := &hostWindow{url: url, userDataDir: userDataDir, ready: make(chan error, 1)}
	defer window.dispose()

	if err := window.create(title); err != nil {
		return err
	}
	if err := window.startWebView(); err != nil {
		return err
	}

	var message windowMessage
	for {
		result, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(result) <= 0 {
			return nil
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&message)))
	}
}

var procTranslateMessage = user32.NewProc("TranslateMessage")

func (w *hostWindow) create(title string) error {
	className, err := syscall.UTF16PtrFromString("Agent_b-host-window")
	if err != nil {
		return err
	}
	windowTitle, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return err
	}
	instance, _, _ := procGetModuleHandle.Call(0)
	cursor, _, _ := procLoadCursor.Call(0, 32512) // IDC_ARROW
	w.windowProc = syscall.NewCallback(func(hwnd, message, wParam uintptr, lParam unsafe.Pointer) uintptr {
		return w.windowProcedure(hwnd, message, wParam, lParam)
	})
	class := wndClassEx{wndProc: w.windowProc, instance: syscall.Handle(instance), className: className, cursor: syscall.Handle(cursor)}
	class.size = uint32(unsafe.Sizeof(class))
	atom, _, _ := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&class)))
	if atom == 0 {
		return fmt.Errorf("registering the host window class failed")
	}
	w.classAtom = atom
	hwnd, _, _ := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(windowTitle)),
		wsOverlappedWindow, cwUseDefault, cwUseDefault, hostDefaultWidth, hostDefaultHeight, 0, 0, instance, 0)
	if hwnd == 0 {
		return fmt.Errorf("creating the host window failed")
	}
	w.hwnd = hwnd
	procShowWindow.Call(hwnd, swShowNormal)
	procUpdateWindow.Call(hwnd)
	return nil
}

// startWebView runs the two-step asynchronous creation WebView2 requires: an
// environment, then a controller parented to our window. Both completions
// arrive on this thread's message pump, which is why the caller must pump.
func (w *hostWindow) startWebView() error {
	userDataFolder, err := syscall.UTF16PtrFromString(w.userDataDir)
	if err != nil {
		return err
	}
	w.environmentHandler = newCOMHandler(func(errorCode uintptr, environment unsafe.Pointer) uintptr {
		if errorCode != 0 || environment == nil {
			w.finish(fmt.Errorf("creating the WebView2 environment failed (0x%X)", uint32(errorCode)))
			return 0
		}
		w.controllerHandler = newCOMHandler(func(errorCode uintptr, controller unsafe.Pointer) uintptr {
			if errorCode != 0 || controller == nil {
				w.finish(fmt.Errorf("creating the WebView2 controller failed (0x%X)", uint32(errorCode)))
				return 0
			}
			comCall(controller, 1) // AddRef: the handler's reference is ours to keep
			w.controller = controller
			w.finish(w.attach())
			return 0
		})
		// ICoreWebView2Environment::CreateCoreWebView2Controller is index 3.
		if result := comCall(environment, 3, w.hwnd, w.controllerHandler.pointer()); result != 0 {
			w.finish(fmt.Errorf("CreateCoreWebView2Controller failed (0x%X)", uint32(result)))
		}
		return 0
	})
	result, _, _ := procCreateEnvironment.Call(0, uintptr(unsafe.Pointer(userDataFolder)), 0, w.environmentHandler.pointer())
	if result != 0 {
		return fmt.Errorf("CreateCoreWebView2EnvironmentWithOptions failed (0x%X)", uint32(result))
	}
	return nil
}

// attach settles the controller: the page fills the client area, the CSS
// app-region regions become draggable, and the one URL we ever load is the
// loopback page the server is already serving.
func (w *hostWindow) attach() error {
	var webview unsafe.Pointer
	if result := comCall(w.controller, 25, uintptr(unsafe.Pointer(&webview))); result != 0 || webview == nil {
		return fmt.Errorf("get_CoreWebView2 failed (0x%X)", uint32(result))
	}
	w.webview = webview

	// Non-client region support is what makes `app-region: drag` in
	// web/css/tokens.css mean something. It is a browser setting, not a channel
	// to the page: no host object, no injected script, no custom scheme.
	var settings unsafe.Pointer
	if result := comCall(webview, 3, uintptr(unsafe.Pointer(&settings))); result == 0 && settings != nil {
		defer comRelease(settings)
		var settings9 unsafe.Pointer
		iid := guid{0x0528a73b, 0xe92d, 0x49f4, [8]byte{0x92, 0x7a, 0xe5, 0x47, 0xdd, 0xda, 0xa3, 0x7d}}
		if result := comCall(settings, 0, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&settings9))); result == 0 && settings9 != nil {
			defer comRelease(settings9)
			comCall(settings9, 38, 1) // put_IsNonClientRegionSupportEnabled(TRUE)
		}
	}

	w.resizeController()
	comCall(w.controller, 4, 1) // put_IsVisible(TRUE)

	target, err := syscall.UTF16PtrFromString(w.url)
	if err != nil {
		return err
	}
	if result := comCall(webview, 5, uintptr(unsafe.Pointer(target))); result != 0 { // Navigate
		return fmt.Errorf("Navigate failed (0x%X)", uint32(result))
	}
	return nil
}

type guid struct {
	data1 uint32
	data2 uint16
	data3 uint16
	data4 [8]byte
}

func (w *hostWindow) finish(err error) {
	select {
	case w.ready <- err:
	default:
	}
	if err != nil && w.hwnd != 0 {
		procDestroyWindow.Call(w.hwnd)
	}
}

func (w *hostWindow) dispose() {
	if w.controller != nil {
		comCall(w.controller, 24) // Close
		comRelease(w.controller)
		w.controller = nil
	}
	if w.hwnd != 0 {
		procDestroyWindow.Call(w.hwnd)
		w.hwnd = 0
	}
}
