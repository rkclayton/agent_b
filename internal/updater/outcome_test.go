package updater

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Item 2mr (a) and (c): the operator's own failure, reproduced from the exact
// bytes his data root carried, and then reported.
const operatorRefusal = `INSTALLATION FAILED: WorkspaceDirectory must name a dedicated Agent_b or workspace directory: C:\work\acme\AppData\Local\Agent_b\profiles\acme\scratch`

func writeProgress(t *testing.T, root string, lines ...installProgressLine) {
	t.Helper()
	body := ""
	for _, line := range lines {
		raw, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		body += string(raw) + "\n"
	}
	if err := os.WriteFile(filepath.Join(root, installProgressName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTheInstallersOwnRefusalIsRead(t *testing.T) {
	root := t.TempDir()
	// Nothing written yet is not a failure: the installer may not have started.
	if reason := readInstallFailure(root, time.Time{}); reason != "" {
		t.Fatalf("an absent progress file reported %q", reason)
	}
	writeProgress(t, root,
		installProgressLine{Phase: "starting", Text: "Installing Agent_b v1.24.0"},
		installProgressLine{Phase: "failed", Text: operatorRefusal, Done: true},
		installProgressLine{Phase: "failed", Text: "The install stopped during failed (exit 1). It is safe to run again.", Done: true},
	)
	// The installer writes its reason first and a generic line after it. The
	// reason is the one that says anything, so it is the one reported.
	if reason := readInstallFailure(root, time.Time{}); reason != operatorRefusal {
		t.Fatalf("reason %q", reason)
	}
	// A progress file that records no failure reports none.
	writeProgress(t, root,
		installProgressLine{Phase: "starting", Text: "Installing Agent_b v1.25.0"},
		installProgressLine{Phase: "installing", Text: "copying files"},
	)
	if reason := readInstallFailure(root, time.Time{}); reason != "" {
		t.Fatalf("a running install reported %q", reason)
	}
}

// The whole path: a release is available, Install launches the setup, the setup
// refuses, and the refusal reaches the state the control renders.
func TestAFailedInstallPutsTheReasonOnTheState(t *testing.T) {
	root := t.TempDir()
	setup := filepath.Join(root, "Agent_b-setup.exe")
	if err := os.WriteFile(setup, []byte("setup"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.ServeFile(writer, request, setup)
	}))
	defer server.Close()

	published := make(chan State, 8)
	manager := &Manager{
		dataRoot:      root,
		client:        server.Client(),
		now:           time.Now,
		checkInterval: time.Hour,
		launch:        func(string, string) error { return nil },
		changed:       func(state State) { published <- state },
	}
	manager.state = State{Enabled: true, Available: true, Version: "v1.25.0", CurrentVersion: "v1.24.0"}
	manager.release = availableRelease{Version: "v1.25.0"}

	// The setup refuses the moment it runs, exactly as it did on his machine.
	writeProgress(t, root, installProgressLine{Phase: "failed", Text: operatorRefusal, Done: true})
	installOutcomePoll = 5 * time.Millisecond
	defer func() { installOutcomePoll = 500 * time.Millisecond }()

	// download is not exercised here: the launch half is what this asserts, so the
	// watcher is started directly with the same call Install makes.
	go manager.watchInstallOutcome(manager.now())
	deadline := time.After(3 * time.Second)
	for {
		select {
		case state := <-published:
			if state.Error == operatorRefusal {
				// And the version stays offered, so the control can say BOTH that an
				// update is available and why the last attempt did not take.
				if !state.Available || state.Version != "v1.25.0" {
					t.Fatalf("the available update was lost: %+v", state)
				}
				return
			}
		case <-deadline:
			t.Fatal("a refused install never reached the state")
		}
	}
}
