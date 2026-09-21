//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestSessionEndIsRecordedBeforeWindowsEndsTheProcess(t *testing.T) {
	root := t.TempDir()
	life := newLifetime(root, time.Now)
	life.begin()
	if _, err := os.Stat(life.markerPath); err != nil {
		t.Fatalf("run marker: %v", err)
	}
	closes := make(chan struct{}, 1)
	watchSessionEnd(life.stopped, func() { closes <- struct{}{} })

	findWindow := user32.NewProc("FindWindowW")
	sendMessage := user32.NewProc("SendMessageW")
	className, _ := syscall.UTF16PtrFromString(sessionEndClassName)
	var hwnd uintptr
	for deadline := time.Now().Add(5 * time.Second); hwnd == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		hwnd, _, _ = findWindow.Call(uintptr(unsafe.Pointer(className)), 0)
	}
	if hwnd == 0 {
		t.Fatal("the session-end window was never created")
	}
	// Item 2eq: nothing that can merely address the window stops production or
	// writes a session-end line. WM_CLOSE is ignored; a posted session end is
	// not Windows'.
	postMessage := user32.NewProc("PostMessageW")
	sendMessage.Call(hwnd, wmClose, 0, 0)
	postMessage.Call(hwnd, wmQueryEndSession, 0, endSessionLogoff)
	postMessage.Call(hwnd, wmEndSession, 1, endSessionLogoff)
	select {
	case <-closes:
		t.Fatal("WM_CLOSE was acted on")
	case <-time.After(500 * time.Millisecond):
	}
	if data, _ := os.ReadFile(filepath.Join(root, "logs", launcherLogName)); len(data) != 0 {
		t.Fatalf("a posted session end was recorded: %q", data)
	}
	// The graceful stop's own channel.
	openEvent := syscall.NewLazyDLL("kernel32.dll").NewProc("OpenEventW")
	eventName, _ := syscall.UTF16PtrFromString(stopEventName(os.Getpid()))
	event, _, openErr := openEvent.Call(0x0002 /* EVENT_MODIFY_STATE */, 0, uintptr(unsafe.Pointer(eventName)))
	if event == 0 {
		t.Fatalf("the stop event does not exist: %v", openErr)
	}
	defer syscall.CloseHandle(syscall.Handle(event))
	syscall.NewLazyDLL("kernel32.dll").NewProc("SetEvent").Call(event)
	select {
	case <-closes:
	case <-time.After(2 * time.Second):
		t.Fatal("the stop event was not delivered as a close request")
	}
	if answer, _, _ := sendMessage.Call(hwnd, wmQueryEndSession, 0, endSessionLogoff); answer != 1 {
		t.Fatalf("WM_QUERYENDSESSION answered %d", answer)
	}
	sendMessage.Call(hwnd, wmEndSession, 1, endSessionLogoff)
	life.stopped("signal terminated") // the signal that can follow is not a second line

	data, err := os.ReadFile(filepath.Join(root, "logs", launcherLogName))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "stopped: the Windows session is logging off") {
		t.Fatalf("launcher log = %q", data)
	}
	if _, err := os.Stat(life.markerPath); !os.IsNotExist(err) {
		t.Fatalf("marker left behind: %v", err)
	}
}

func TestAnUnrecordedEndIsReportedAtTheNextStart(t *testing.T) {
	root := t.TempDir()
	child := exec.Command("cmd", "/c", "exit", "0")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	pid := child.Process.Pid
	created := processCreated(pid)
	_ = child.Wait()

	earlier := &lifetime{logPath: filepath.Join(root, "logs", launcherLogName), markerPath: filepath.Join(root, "agent_b-run.json"), pid: pid, created: created, now: time.Now}
	earlier.begin() // then the process is killed: no stopped()

	running := newLifetime(root, time.Now)
	running.begin() // a live marker for this very process is not stale
	running.begin()
	data, _ := os.ReadFile(running.logPath)
	if strings.Count(string(data), "ended without recording a reason") != 1 || !strings.Contains(string(data), "PID "+strconv.Itoa(pid)) {
		t.Fatalf("launcher log = %q", data)
	}
	if processRunning(os.Getpid(), processCreated(os.Getpid())) != true || processRunning(os.Getpid(), 1) != false {
		t.Fatal("processRunning must match the creation time, not only the PID")
	}
}

// Item 2hg (v1.3.0/W6): the guard window exists the moment watchSessionEnd
// returns. No polling here, deliberately -- the test above waits up to five
// seconds for the window, and that wait was the defect written down: while the
// window is merely "coming", the process's only top-level window is the console
// one a hidden start gives a console application, and a WM_CLOSE that reaches
// the console becomes CTRL_CLOSE_EVENT and then SIGTERM. Killing at 100, 300,
// 600 and 1200 ms ended the server 12 times out of 12 before this changed.
func TestTheSessionEndWindowExistsAsSoonAsTheWatchReturns(t *testing.T) {
	life := newLifetime(t.TempDir(), time.Now)
	life.begin()
	watchSessionEnd(life.stopped, func() {})

	className, err := syscall.UTF16PtrFromString(sessionEndClassName)
	if err != nil {
		t.Fatalf("class name: %v", err)
	}
	hwnd, _, _ := user32.NewProc("FindWindowW").Call(uintptr(unsafe.Pointer(className)), 0)
	if hwnd == 0 {
		t.Fatal("watchSessionEnd returned before the guard window existed; a WM_CLOSE arriving now would reach the console window instead and stop the server")
	}
}
