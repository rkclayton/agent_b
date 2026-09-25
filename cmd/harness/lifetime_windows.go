//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
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
	ismexSend                      = 0x00000001
)

// stopEventName is the installer's graceful-stop channel for one process in
// one application root. The PID prevents one instance addressing another;
// the canonical-root digest prevents a disposable installer that was handed a
// different root from addressing the installed product even if it knows the
// production PID.
func stopEventName(applicationRoot string, pid int) string {
	root, err := filepath.Abs(applicationRoot)
	if err == nil {
		applicationRoot = root
	}
	canonical := strings.ToUpper(filepath.Clean(applicationRoot))
	digest := sha256.Sum256([]byte(canonical))
	return fmt.Sprintf(`Local\Agent_b-stop-%x-%d`, digest[:12], pid)
}

var (
	user32                        = syscall.NewLazyDLL("user32.dll")
	procRegisterClassEx           = user32.NewProc("RegisterClassExW")
	procCreateWindowEx            = user32.NewProc("CreateWindowExW")
	procDefWindowProc             = user32.NewProc("DefWindowProcW")
	procGetMessage                = user32.NewProc("GetMessageW")
	procDispatchMessage           = user32.NewProc("DispatchMessageW")
	procGetModuleHandle           = syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW")
	procCreateEvent               = syscall.NewLazyDLL("kernel32.dll").NewProc("CreateEventW")
	procOpenEvent                 = syscall.NewLazyDLL("kernel32.dll").NewProc("OpenEventW")
	procSetEvent                  = syscall.NewLazyDLL("kernel32.dll").NewProc("SetEvent")
	procAllowSetForegroundWindow  = user32.NewProc("AllowSetForegroundWindow")
	procInSendMessageEx           = user32.NewProc("InSendMessageEx")
	procConvertSecurityDescriptor = syscall.NewLazyDLL("advapi32.dll").NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	sessionEndClassName           = fmt.Sprintf("Agent_b-session-end-%d", os.Getpid())
)

func activateEventName(applicationRoot string, pid int) string {
	root, err := filepath.Abs(applicationRoot)
	if err == nil {
		applicationRoot = root
	}
	digest := sha256.Sum256([]byte(strings.ToUpper(filepath.Clean(applicationRoot))))
	return fmt.Sprintf(`Local\Agent_b-activate-%x-%d`, digest[:12], pid)
}

// activateExistingInstance trusts neither the marker nor a port by itself.
// The marker must name this exact install root, the same process incarnation
// must still be alive, and that process must answer on the recorded loopback
// port with its PID before its scoped activation event is signalled.
func activateExistingInstance(dataRoot, applicationRoot string) (int, bool) {
	data, err := readMarker(filepath.Join(dataRoot, "agent_b-run.json"))
	if err != nil {
		return 0, false
	}
	var marker runMarker
	if json.Unmarshal(data, &marker) != nil || marker.PID <= 0 || strings.TrimSpace(marker.Application) == "" || strings.TrimSpace(marker.Listen) == "" {
		return 0, false
	}
	want, wantErr := filepath.Abs(applicationRoot)
	got, gotErr := filepath.Abs(marker.Application)
	if wantErr != nil || gotErr != nil || !strings.EqualFold(filepath.Clean(want), filepath.Clean(got)) || !processRunning(marker.PID, marker.Created) {
		return 0, false
	}
	host, _, err := net.SplitHostPort(marker.Listen)
	if err != nil {
		return 0, false
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return 0, false
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return 0, false
	}
	client := http.Client{Timeout: time.Second, Jar: jar}
	bootstrap, err := client.Get("http://" + marker.Listen + "/chat")
	if err != nil {
		return 0, false
	}
	bootstrap.Body.Close()
	if bootstrap.StatusCode != http.StatusOK {
		return 0, false
	}
	response, err := client.Get("http://" + marker.Listen + "/api/state")
	if err != nil {
		return 0, false
	}
	defer response.Body.Close()
	var state struct {
		ProcessID int `json:"process_id"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&state) != nil || state.ProcessID != marker.PID {
		return 0, false
	}
	// The verified loopback server owns update policy and the 15-minute gate.
	// This request is only an attach signal; it returns immediately while any
	// allowed check runs in the serving process.
	updateResponse, updateErr := client.Get("http://" + marker.Listen + "/api/update?attach=1")
	if updateErr == nil {
		updateResponse.Body.Close()
	}
	name, _ := syscall.UTF16PtrFromString(activateEventName(applicationRoot, marker.PID))
	const eventModifyState = 0x0002
	// Let the already-running process take foreground permission inherited by
	// this user-initiated second launch before it handles the event.
	procAllowSetForegroundWindow.Call(uintptr(marker.PID))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		event, _, _ := procOpenEvent.Call(eventModifyState, 0, uintptr(unsafe.Pointer(name)))
		if event != 0 {
			ok, _, _ := procSetEvent.Call(event)
			syscall.CloseHandle(syscall.Handle(event))
			return marker.PID, ok != 0
		}
		time.Sleep(25 * time.Millisecond)
	}
	return 0, false
}

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
//
// Item 2eq: any process on the desktop can address a top-level window, so the
// window acts on nothing it cannot attribute to Windows. WM_CLOSE is ignored.
// A session end is recorded only when WM_QUERYENDSESSION and then WM_ENDSESSION
// were both sent (the way Windows delivers them), never posted. The graceful
// stop has its own channel instead: a named event in this session that only
// the operator's account and SYSTEM may signal (stopEventName).
// Item 2hg (v1.3.0/W6): watchSessionEnd RETURNS ONLY ONCE THE GUARD WINDOW
// EXISTS. It used to start the goroutine and return immediately, so there was a
// window of time in which the process was listening but had no window to ignore
// WM_CLOSE with -- and a console application started hidden owns a
// ConsoleWindowClass window from birth. taskkill without /F posts WM_CLOSE to
// every top-level window it finds; reaching the console instead raises
// CTRL_CLOSE_EVENT, which Go delivers as SIGTERM, and the server stops with
// "signal terminated" despite 2eq.
//
// Measured before the fix: killing at 100, 300, 600 and 1200 ms after start
// ended the process 12 times out of 12, with ConsoleWindowClass the only
// top-level window; killing after the window existed left it running 5 of 5.
// The launcher no longer gives a background server a console window at all,
// which removes the door; this closes the gap behind it, so that "the listener
// is up" implies "the guard is up" rather than merely "the guard is coming".
func watchSessionEnd(applicationRoot string, record func(string), closeRequested func()) {
	watchStopEvent(applicationRoot, closeRequested)
	ready := make(chan struct{})
	go func() {
		// Whatever happens below -- window created, class refused, creation
		// failed -- the caller is released exactly once.
		readyOnce := sync.OnceFunc(func() { close(ready) })
		defer readyOnce()
		runtime.LockOSThread()
		className, _ := syscall.UTF16PtrFromString(sessionEndClassName)
		instance, _, _ := procGetModuleHandle.Call(0)
		queried := false
		callback := syscall.NewCallback(func(hwnd syscall.Handle, message uint32, wParam, lParam uintptr) uintptr {
			switch message {
			case wmQueryEndSession:
				queried = sentMessage()
				return 1
			case wmClose:
				return 0
			case wmEndSession:
				// ENDSESSION_CLOSEAPP alone is Restart Manager asking applications to
				// close; this process does not act on it, so it is not recorded as an end.
				closeAppOnly := lParam&endSessionCloseApp != 0 && lParam&(endSessionLogoff|endSessionCritical) == 0
				if wParam != 0 && queried && sentMessage() && !closeAppOnly {
					record(sessionEndReason(lParam))
				}
				queried = false
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
		// The window is addressable from here, so the caller may proceed.
		readyOnce()
		var message windowMessage
		for {
			result, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
			if int32(result) <= 0 {
				return
			}
			procDispatchMessage.Call(uintptr(unsafe.Pointer(&message)))
		}
	}()
	// Bounded: a machine that cannot give us a window must still serve. The
	// wait is for the ordinary case, not a precondition for running.
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
	}
}

// sentMessage is true while the window procedure handles a message another
// thread sent with SendMessage; a posted message is not one.
func sentMessage() bool {
	flags, _, _ := procInSendMessageEx.Call(0)
	return flags&ismexSend != 0
}

// watchStopEvent creates the graceful-stop channel. The installer opens the
// event by name and sets it. Its DACL admits only this process's user and
// SYSTEM, so a service-account tool process cannot signal it; if the name
// already exists (someone created it first), the channel is not used, since
// its creator would control it.
func watchStopEvent(applicationRoot string, closeRequested func()) {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return
	}
	user, err := token.GetTokenUser()
	token.Close()
	if err != nil {
		return
	}
	sid, err := user.User.Sid.String()
	if err != nil {
		return
	}
	sddl, _ := syscall.UTF16PtrFromString("D:P(A;;GA;;;" + sid + ")(A;;GA;;;SY)")
	var descriptor uintptr
	if ok, _, _ := procConvertSecurityDescriptor.Call(uintptr(unsafe.Pointer(sddl)), 1, uintptr(unsafe.Pointer(&descriptor)), 0); ok == 0 {
		return
	}
	defer syscall.LocalFree(syscall.Handle(descriptor))
	attributes := syscall.SecurityAttributes{Length: uint32(unsafe.Sizeof(syscall.SecurityAttributes{})), SecurityDescriptor: descriptor}
	name, _ := syscall.UTF16PtrFromString(stopEventName(applicationRoot, os.Getpid()))
	event, _, createErr := procCreateEvent.Call(uintptr(unsafe.Pointer(&attributes)), 1, 0, uintptr(unsafe.Pointer(name)))
	if event == 0 {
		return
	}
	if createErr == syscall.ERROR_ALREADY_EXISTS {
		syscall.CloseHandle(syscall.Handle(event))
		return
	}
	go func() {
		if wait, _ := syscall.WaitForSingleObject(syscall.Handle(event), syscall.INFINITE); wait == syscall.WAIT_OBJECT_0 {
			closeRequested()
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
