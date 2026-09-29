//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Item 2o0: one test per launch path, each ending with the window created or
// with its logged reason and a successful retry. The window runner is a stub,
// so nothing here reaches the operator's desktop.
func stubHostWindow(t *testing.T, wanted bool, failures int) (opened chan struct{}, dataRoot string) {
	t.Helper()
	dataRoot = t.TempDir()
	opened = make(chan struct{}, 4)
	saved := []any{hostWindowMode, hostWindowDataRoot, hostWindowRetryDelays}
	savedRunner, savedAvailability := hostWindowRunner, hostWindowAvailability
	t.Cleanup(func() {
		hostWindowMode, hostWindowDataRoot, hostWindowRetryDelays = saved[0].(bool), saved[1].(string), saved[2].([]time.Duration)
		hostWindowRunner, hostWindowAvailability = savedRunner, savedAvailability
	})
	hostWindowMode, hostWindowDataRoot = wanted, dataRoot
	hostWindowRetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	hostWindowAvailability = func() (string, error) { return "stub", nil }
	hostWindowRunner = func(string, string, string, string) error {
		if failures > 0 {
			failures--
			return errors.New("stub WebView2 controller failed")
		}
		recordWindowFate("host window: opened")
		opened <- struct{}{}
		return nil
	}
	return opened, dataRoot
}

func waitOpened(t *testing.T, opened chan struct{}) {
	t.Helper()
	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("the host window was never created")
	}
}

func launcherLines(t *testing.T, dataRoot string) string {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(dataRoot, "logs", "launcher.log"))
	return string(data)
}

func waitLine(t *testing.T, dataRoot, text string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if strings.Contains(launcherLines(t, dataRoot), text) {
			return
		}
	}
	t.Fatalf("launcher.log never said %q:\n%s", text, launcherLines(t, dataRoot))
}

func TestColdLaunchOpensTheWindow(t *testing.T) {
	opened, dataRoot := stubHostWindow(t, true, 0)
	closeRequests := make(chan struct{}, 1)
	startHostWindow("127.0.0.1:1", t.TempDir(), "b", closeRequests)
	waitOpened(t, opened)
	<-closeRequests // closing the window still stops the server
	if !strings.Contains(launcherLines(t, dataRoot), "host window: opened") {
		t.Fatalf("launcher.log has no window line:\n%s", launcherLines(t, dataRoot))
	}
}

func TestHostStartFailingOnceIsRetried(t *testing.T) {
	opened, dataRoot := stubHostWindow(t, true, 1)
	startHostWindow("127.0.0.1:1", t.TempDir(), "b", make(chan struct{}, 1))
	waitOpened(t, opened)
	lines := launcherLines(t, dataRoot)
	if !strings.Contains(lines, "host window: attempt 1 failed (stub WebView2 controller failed); retrying") || !strings.Contains(lines, "host window: opened") {
		t.Fatalf("a failed host start was not logged and retried:\n%s", lines)
	}
}

func TestHostStartThatKeepsFailingSaysSoAndTheNextLaunchRetries(t *testing.T) {
	opened, dataRoot := stubHostWindow(t, true, 3)
	requests := make(chan struct{}, 1)
	go superviseHostWindow("u", t.TempDir(), true, requests, make(chan struct{}, 1))
	waitLine(t, dataRoot, "could not open after 3 attempts, using the browser instead (stub WebView2 controller failed); the next launch tries again")
	requests <- struct{}{}
	waitOpened(t, opened)
}

// The five dated silent starts: the sign-in shortcut starts the server with
// -Detached -NoBrowser, so without -window. Then a second launch (the Start
// menu handoff, or any launch while a server left running from an earlier
// session answers) must reach that server and get a window created.
func TestSignInStartThenSecondLaunchCreatesTheWindow(t *testing.T) {
	opened, dataRoot := stubHostWindow(t, false, 0)
	applicationRoot := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/state" {
			_ = json.NewEncoder(w).Encode(map[string]int{"process_id": os.Getpid()})
		}
	}))
	defer server.Close()
	marker, _ := json.Marshal(runMarker{PID: os.Getpid(), Created: processCreated(os.Getpid()), Application: applicationRoot, Listen: strings.TrimPrefix(server.URL, "http://")})
	if err := os.WriteFile(filepath.Join(dataRoot, "agent_b-run.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	startHostWindow("127.0.0.1:1", applicationRoot, "b", make(chan struct{}, 1))
	waitLine(t, dataRoot, "host window: not requested by this launch")
	select {
	case <-opened:
		t.Fatal("a background start opened a window before any launch asked for one")
	case <-time.After(50 * time.Millisecond):
	}
	if pid, activated := activateExistingInstance(dataRoot, applicationRoot); !activated || pid != os.Getpid() {
		t.Fatalf("the second launch could not reach the windowless server: pid=%d activated=%v", pid, activated)
	}
	waitOpened(t, opened)
}
