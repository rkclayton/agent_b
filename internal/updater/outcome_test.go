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
const operatorRefusal = `INSTALLATION FAILED: WorkspaceDirectory must name a dedicated Agent_b or workspace directory: C:\Users\Randy\AppData\Local\Agent_b\profiles\Randy\scratch`

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

// Item 2nh (b) and (c): THE OPERATOR'S OWN 23:22 FILE, REPLAYED.
//
// His update to v1.29.0 worked and he reported it as a failure. These are the
// bytes his data root carried, in the order the build he was on wrote them: the
// real finish, and then the migration warning written as a second "finished
// ok:true" line, which made "MIGRATION LEFT IN PLACE: access denied" the last
// line of a successful update. It must read as a success with one note.
const operatorMigrationWarning = `MIGRATION LEFT IN PLACE: access denied — remove it from an elevated shell: C:\Program Files\Agent_b`

// writePhases writes the same file through the encoder, for lines whose text
// carries Windows paths: a hand-written JSON string with a single backslash in it
// is not JSON, and a line that will not parse is silently skipped, which would
// make a test pass for the wrong reason.
func writePhases(t *testing.T, root string, phases ...progressPhase) {
	t.Helper()
	body := ""
	for _, phase := range phases {
		raw, err := json.Marshal(phase)
		if err != nil {
			t.Fatal(err)
		}
		body += string(raw) + "\n"
	}
	if err := os.WriteFile(filepath.Join(root, installProgressName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeRawProgress(t *testing.T, root string, lines ...string) {
	t.Helper()
	body := ""
	for _, line := range lines {
		body += line + "\n"
	}
	if err := os.WriteFile(filepath.Join(root, installProgressName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTheOperatorsSuccessfulUpdateReadsAsASuccessWithANote2nh(t *testing.T) {
	root := t.TempDir()
	writePhases(t, root,
		progressPhase{At: "2026-09-28T04:22:21Z", Phase: "starting", Text: "Installing Agent_b v1.29.0"},
		progressPhase{At: "2026-09-28T04:22:23Z", Phase: "preflight", Text: "Installing Agent_b v1.29.0"},
		progressPhase{At: "2026-09-28T04:22:25Z", Phase: "copying the application", Text: `Application: C:\Users\Randy\AppData\Local\Programs\Agent_b`},
		progressPhase{At: "2026-09-28T04:22:30Z", Phase: "stopping the running application", Text: "STOPPING: Agent_b PID 29488"},
		progressPhase{At: "2026-09-28T04:22:38Z", Phase: "finished", Text: "Agent_b v1.29.0 is installed.", Done: true, OK: true},
		progressPhase{At: "2026-09-28T04:23:02Z", Phase: "finished", Text: operatorMigrationWarning, Done: true, OK: true},
	)
	outcome := outcomeFor(root, "1.29.0")
	if outcome == nil {
		t.Fatal("his file reached no outcome at all, which is the silence the item exists to end")
	}
	if !outcome.OK {
		t.Fatalf("a successful update read as a failure: %+v", outcome)
	}
	if outcome.Version != "v1.29.0" {
		t.Fatalf("version %q, want v1.29.0", outcome.Version)
	}
	if len(outcome.Warnings) != 1 || outcome.Warnings[0] != operatorMigrationWarning {
		t.Fatalf("warnings %q, want the one migration note", outcome.Warnings)
	}
	if outcome.At != "2026-09-28T04:23:02Z" {
		t.Fatalf("outcome time %q, want the last finish", outcome.At)
	}
}

func TestTheWarningPhaseIsANoteAndTheFinishIsTheResult2nh(t *testing.T) {
	root := t.TempDir()
	writeRawProgress(t, root,
		`{"phase":"preflight","text":"Installing Agent_b v1.30.0"}`,
		`{"phase":"restarting","text":"Starting Agent_b v1.30.0"}`,
		`{"phase":"warning","text":"MIGRATION LEFT IN PLACE: access denied","ok":true}`,
		`{"at":"2026-09-28T05:00:00Z","phase":"finished","text":"Agent_b v1.30.0 is installed.","done":true,"ok":true}`,
	)
	outcome := outcomeFor(root, "v1.30.0")
	if outcome == nil || !outcome.OK || outcome.Version != "v1.30.0" {
		t.Fatalf("outcome %+v, want a v1.30.0 success", outcome)
	}
	if len(outcome.Warnings) != 1 {
		t.Fatalf("warnings %q, want the one note", outcome.Warnings)
	}
	// Item 2mk (a): a finish for a NEWER version than this process is running is not
	// claimed as this process's success — it is reported as what it is, an install
	// that happened while this window went on serving something else.
	other := outcomeFor(root, "v1.29.0")
	if other == nil || other.Running != "v1.29.0" || other.Version != "v1.30.0" {
		t.Fatalf("a finished install this process did not become was not reported: %+v", other)
	}
	// And a finish OLDER than what is running is a stale file, not a fact about now.
	if stale := outcomeFor(root, "v1.31.0"); stale != nil {
		t.Fatalf("a stale progress file was claimed: %+v", stale)
	}
}

func TestAFailedInstallOutcomeNamesThePhaseAndTheTranscript2nh(t *testing.T) {
	root := t.TempDir()
	transcript := `C:\Users\Randy\AppData\Local\Agent_b\logs\installer-20260927-190511.log`
	writePhases(t, root,
		progressPhase{Phase: "starting", Text: "Installing Agent_b v1.30.0"},
		progressPhase{At: "2026-09-28T05:10:00Z", Phase: "copying the application", Text: operatorRefusal + " It is safe to run again. Transcript: " + transcript, Done: true},
	)
	outcome := outcomeFor(root, "v1.29.0")
	if outcome == nil || outcome.OK {
		t.Fatalf("outcome %+v, want a failure", outcome)
	}
	if outcome.Phase != "copying the application" {
		t.Fatalf("phase %q, want the phase it stopped in", outcome.Phase)
	}
	if outcome.Transcript != transcript {
		t.Fatalf("transcript %q, want %q", outcome.Transcript, transcript)
	}
	if outcome.Version != "v1.30.0" {
		t.Fatalf("version %q, want the version it was installing", outcome.Version)
	}
}

// Item 2nh (a): the five stages, named from the installer's own phase words.
func TestEveryInstallerPhaseNamesAStage2nh(t *testing.T) {
	for phase, want := range map[string]string{
		"starting":                         "installing",
		"preflight":                        "installing",
		"copying the application":          "installing",
		"stopping the running application": "stopping",
		"restarting":                       "restarting",
		"finished":                         "restarting",
		"something this build has never heard of": "installing",
	} {
		step, line := stepFor(phase)
		if step != want {
			t.Fatalf("phase %q mapped to step %q, want %q", phase, step, want)
		}
		if line == "" {
			t.Fatalf("phase %q produced no line, and a wait element without one is decoration", phase)
		}
	}
	if step, _ := stepFor("warning"); step != "" {
		t.Fatalf("a warning became the stage in hand (%q); it is a note", step)
	}
}
